// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"crypto/rand"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

func TestAccOrganizationCircleCiResource(t *testing.T) {
	organizationName := rand.Text()
	slugRegex := regexp.MustCompile(`^circleci/[a-zA-Z0-9._-]+$`)
	idRegex := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccOrganizationResourceConfig(
					organizationName,
					"circleci",
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_organization.test_organization",
						tfjsonpath.New("name"),
						knownvalue.StringExact(organizationName),
					),
					statecheck.ExpectKnownValue(
						"circleci_organization.test_organization",
						tfjsonpath.New("slug"),
						knownvalue.StringRegexp(slugRegex),
					),
					statecheck.ExpectKnownValue(
						"circleci_organization.test_organization",
						tfjsonpath.New("id"),
						knownvalue.StringRegexp(idRegex),
					),
					statecheck.ExpectKnownValue(
						"circleci_organization.test_organization",
						tfjsonpath.New("vcs_type"),
						knownvalue.StringExact("circleci"),
					),
				},
			},
			// ImportState testing
			{
				ResourceName:      "circleci_organization.test_organization",
				ImportState:       true,
				ImportStateVerify: true,
			},
			// Delete testing automatically occurs in TestCase
		},
	})
}

// TestOrganizationResourceVCSTypeIsGatedAtPlanTime pins the plan-time
// enumeration on vcs_type, by running the schema's own validators.
//
// The create route validates this field against the exact set
// {github, bitbucket, circleci} and answers 400 for anything else — including
// the slug abbreviations "gh" and "bb", which are valid in an organization slug
// and so are the obvious thing for a practitioner to reach for. Without a
// validator that is an apply-time failure: `terraform plan` succeeds, a CI plan
// check passes, and the apply dies on the first request.
//
// This asserts through the schema rather than calling stringvalidator.OneOf
// directly, so that deleting the validator from the schema fails the test.
func TestOrganizationResourceVCSTypeIsGatedAtPlanTime(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	schemaResp := &fwresource.SchemaResponse{}
	NewOrganizationResource().Schema(ctx, fwresource.SchemaRequest{}, schemaResp)

	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", schemaResp.Diagnostics)
	}

	attribute, ok := schemaResp.Schema.Attributes["vcs_type"]
	if !ok {
		t.Fatal("schema is missing the vcs_type attribute")
	}
	stringAttribute, ok := attribute.(schema.StringAttribute)
	if !ok {
		t.Fatalf("vcs_type is %T, want schema.StringAttribute", attribute)
	}
	if len(stringAttribute.Validators) == 0 {
		t.Fatal("vcs_type has no validators, so an invalid VCS type only fails during apply")
	}

	cases := map[string]bool{
		// Accepted by the create route's input spec.
		"github":    true,
		"bitbucket": true,
		"circleci":  true,
		// Valid in an organization *slug*, and rejected here. This is the pair the
		// gate exists for.
		"gh": false,
		"bb": false,
		// Case matters to an exact-set spec.
		"GitHub": false,
		"":       false,
		"gitlab": false,
	}

	for value, wantValid := range cases {
		t.Run(value, func(t *testing.T) {
			t.Parallel()

			resp := &validator.StringResponse{}
			for _, v := range stringAttribute.Validators {
				v.ValidateString(ctx, validator.StringRequest{
					Path:        path.Root("vcs_type"),
					ConfigValue: types.StringValue(value),
				}, resp)
			}

			switch gotValid := !resp.Diagnostics.HasError(); {
			case wantValid && !gotValid:
				t.Errorf("vcs_type = %q was rejected at plan time but the API accepts it: %v",
					value, resp.Diagnostics.Errors())
			case !wantValid && gotValid:
				t.Errorf("vcs_type = %q passed plan-time validation, but the create route answers 400 "+
					"for it — the failure would land mid-apply instead", value)
			}
		})
	}
}

// TestAccOrganizationGithubOAuthAdoptDoesNotDeleteRealOrg is the real-network
// sibling of the fake-backed TestAccOrganizationDestroyDoesNotDeleteAdoptedOrg
// in organization_destroy_test.go, run against an actual GitHub OAuth-backed
// organization rather than a stand-in server.
//
// It proves two things end to end, against the real API: Create against an
// already-existing classic organization is a genuine adopt (POST
// /api/v2/organization is find-or-create, so this never creates anything new
// on the VCS side), and `terraform destroy` on that adopted resource releases
// it from state without ever issuing DELETE — verified below by re-fetching
// the organization straight from the API after the test's own destroy step
// and confirming it still answers 200, not 404.
func TestAccOrganizationGithubOAuthAdoptDoesNotDeleteRealOrg(t *testing.T) {
	organizationID := testGithubOrgID(t)
	organizationSlug := testGithubOrgSlug(t)
	organizationName := strings.TrimPrefix(organizationSlug, "gh/")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationResourceConfig(organizationName, "github"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_organization.test_organization",
						tfjsonpath.New("id"),
						knownvalue.StringExact(organizationID),
					),
					statecheck.ExpectKnownValue(
						"circleci_organization.test_organization",
						tfjsonpath.New("slug"),
						knownvalue.StringExact(organizationSlug),
					),
				},
			},
			// An empty config destroys the resource. Because this organization is
			// adopted rather than created, that destroy must release it from state
			// rather than call DELETE — verified in the check below, after
			// resource.Test's own destroy step has already run.
			{Config: `provider "circleci" {}`},
		},
		CheckDestroy: func(*terraform.State) error {
			client := circleci.New(circleci.Config{Token: os.Getenv("CIRCLE_TOKEN")})

			if _, err := client.GetOrganization(t.Context(), organizationID); err != nil {
				return fmt.Errorf(
					"the adopted organization %s could no longer be read after destroy (%v); "+
						"destroy must release an adopted organization from state, never delete it",
					organizationID, err,
				)
			}

			return nil
		},
	})
}

func testAccOrganizationResourceConfig(name, vcs_type string) string {
	return fmt.Sprintf(`
resource "circleci_organization" "test_organization" {
  name 		= %[1]q
  vcs_type 	= %[2]q
}
`, name, vcs_type)
}
