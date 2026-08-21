// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccContextResource(t *testing.T) {
	dateRegex, err := regexp.Compile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z$`)
	if err != nil {
		t.Fatal("Could not create Date Regex for testing.")
	}
	organizationID := testOrgID(t)
	randName := rand.Text()
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccContextResourceConfig(organizationID, randName),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_context.test_context",
						tfjsonpath.New("name"),
						knownvalue.StringExact(randName),
					),
					statecheck.ExpectKnownValue(
						"circleci_context.test_context",
						tfjsonpath.New("organization_id"),
						knownvalue.StringExact(organizationID),
					),
					statecheck.ExpectKnownValue(
						"circleci_context.test_context",
						tfjsonpath.New("created_at"),
						knownvalue.StringRegexp(dateRegex),
					),
				},
			},
			// ImportState testing, composite "ORGANIZATION_ID/CONTEXT_ID" form.
			// Still supported because it is documented, but the organization in
			// it is now verified against the API rather than stored blindly.
			{
				ResourceName:      "circleci_context.test_context",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					// 1. Get the computed 'id' (context ID)
					contextID, found := s.RootModule().Resources["circleci_context.test_context"].Primary.Attributes["id"]
					if !found {
						return "", errors.New("attribute circleci_context.test_context.id not found")
					}

					// 2. Get the known 'organization_id'
					organizationID, found := s.RootModule().Resources["circleci_context.test_context"].Primary.Attributes["organization_id"]
					if !found {
						return "", errors.New("attribute circleci_context.test_context.organization_id not found")
					}

					// 3. Return the composite id, organization first.
					return fmt.Sprintf("%s/%s", organizationID, contextID), nil
				},
			},
			// A BARE context id is enough against the real API: the provider
			// reads org_id off GET /api/v2/context/{id} rather than taking the
			// practitioner's word for it. ImportStateVerify is what proves the
			// organization arrived — it compares the imported state against the
			// state from the create step, whose organization_id and org_id are
			// both the real organization, and the import id carries neither.
			{
				ResourceName:      "circleci_context.test_context",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					contextID, found := s.RootModule().Resources["circleci_context.test_context"].Primary.Attributes["id"]
					if !found {
						return "", errors.New("attribute circleci_context.test_context.id not found")
					}

					return contextID, nil
				},
			},
			// A composite id whose organization is wrong must be refused rather
			// than stored. Storing it used to make the next plan destroy the
			// context and everything on it, because org_id forces replacement.
			{
				ResourceName:      "circleci_context.test_context",
				ImportState:       true,
				ImportStateVerify: false,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					contextID, found := s.RootModule().Resources["circleci_context.test_context"].Primary.Attributes["id"]
					if !found {
						return "", errors.New("attribute circleci_context.test_context.id not found")
					}

					// A well-formed organization UUID that does not own this
					// context.
					return "00000000-0000-4000-8000-000000000000/" + contextID, nil
				},
				ExpectError: regexp.MustCompile(
					`(?s)Import ID organization does not match the API.*00000000-0000-4000-8000-000000000000`,
				),
			},
			// Delete testing automatically occurs in TestCase
		},
	})
}

func testAccContextResourceConfig(organizationID, name string) string {
	return fmt.Sprintf(`
  resource "circleci_context" "test_context" {
  name            = %[1]q
  organization_id = %[2]q
}
`, name, organizationID)
}
