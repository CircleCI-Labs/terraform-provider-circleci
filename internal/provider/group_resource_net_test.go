// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

// This file is the real-API ([NET]) counterpart to group_resource_test.go,
// which is entirely fake-backed. group.go's routes are plain /api/v2 (unlike
// group_membership.go's private-origin ones), so these run through a normal
// `provider "circleci" {}` block and a real `resource.Test` against whichever
// organization CIRCLECI_TEST_VCS_TYPE names.
//
// Groups only create successfully on a standalone ("circleci/<uuid>")
// organization -- confirmed [NET] on gh-app-cci-1 and gitlab-test, both 201 --
// and are rejected with 403 on a classic one, confirmed [NET] on
// gh-oauth-cci-1 and gh-oauth-cci-2. TestAccGroupResourceNet_Lifecycle and
// TestAccGroupResourceNet_RejectedOnClassicOrganization gate on organization
// class rather than VCS type for exactly that reason: the two standalone
// fixtures are on two different VCS integrations (GitHub App and GitLab), and
// the two classic ones likewise.

// testAccGroupNetProviderConfig is deliberately just an empty provider block:
// the provider defaults to CircleCI Cloud and CIRCLE_TOKEN, which is exactly
// what testAccPreCheck arranges to have set for the active integration.
const testAccGroupNetProviderConfig = `
provider "circleci" {}
`

func testAccGroupNetConfig(orgID, name, description string) string {
	return testAccGroupNetProviderConfig + fmt.Sprintf(`
resource "circleci_group" "net_test" {
  org_id      = %[1]q
  name        = %[2]q
  description = %[3]q
}

data "circleci_group" "net_test" {
  org_id = %[1]q
  id     = circleci_group.net_test.id
}
`, orgID, name, description)
}

// TestAccGroupResourceNet_Lifecycle exercises circleci_group's full lifecycle
// against a real standalone organization: create, the automatic empty-plan
// check resource.Test performs after every apply, reading the same group back
// through circleci_group, import, a rename (which the API can only satisfy by
// destroying and recreating -- there is no update endpoint), and finally
// destroy.
func TestAccGroupResourceNet_Lifecycle(t *testing.T) {
	testAccPreCheck(t)
	testRequireStandaloneOrg(t, testOrgSlug(t))

	orgID := testOrgID(t)
	name := testUniqueGroupName(t, "tfacc-group")
	renamed := name + "-renamed"

	var createdID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and read, through both the resource and the data source.
			{
				Config: testAccGroupNetConfig(orgID, name, "tf acceptance test"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_group.net_test", tfjsonpath.New("org_id"), knownvalue.StringExact(orgID),
					),
					statecheck.ExpectKnownValue(
						"circleci_group.net_test", tfjsonpath.New("name"), knownvalue.StringExact(name),
					),
					statecheck.ExpectKnownValue(
						"circleci_group.net_test", tfjsonpath.New("description"),
						knownvalue.StringExact("tf acceptance test"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_group.net_test", tfjsonpath.New("name"), knownvalue.StringExact(name),
					),
				},
				Check: func(s *terraform.State) error {
					rs, ok := s.RootModule().Resources["circleci_group.net_test"]
					if !ok {
						return fmt.Errorf("circleci_group.net_test not found in state")
					}

					createdID = rs.Primary.ID
					if createdID == "" {
						return fmt.Errorf("created group has no id recorded in state")
					}

					return nil
				},
			},
			// Import, using the "org_id/group_id" form.
			{
				ResourceName:      "circleci_group.net_test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs, ok := s.RootModule().Resources["circleci_group.net_test"]
					if !ok {
						return "", fmt.Errorf("circleci_group.net_test not found in state")
					}

					return rs.Primary.Attributes["org_id"] + "/" + rs.Primary.ID, nil
				},
			},
			// A changed name replaces the group -- there is no update endpoint --
			// and the new group must get a new id.
			{
				Config: testAccGroupNetConfig(orgID, renamed, "tf acceptance test"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("circleci_group.net_test", plancheck.ResourceActionReplace),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_group.net_test", tfjsonpath.New("name"), knownvalue.StringExact(renamed),
					),
				},
				Check: func(s *terraform.State) error {
					rs, ok := s.RootModule().Resources["circleci_group.net_test"]
					if !ok {
						return fmt.Errorf("circleci_group.net_test not found in state")
					}

					if rs.Primary.ID == createdID {
						return fmt.Errorf("group id %s survived a rename, want a new group with a new id", rs.Primary.ID)
					}

					return nil
				},
			},
		},
		// resource.Test's own destroy step removes the (renamed) group left in
		// state; this independently confirms against the live API that both the
		// original and the renamed group are actually gone, rather than trusting
		// that Terraform's state bookkeeping matches reality.
		CheckDestroy: func(*terraform.State) error {
			client := circleci.New(circleci.Config{Token: os.Getenv("CIRCLE_TOKEN")})

			for _, id := range []string{createdID} {
				if id == "" {
					continue
				}

				if _, err := client.Groups().Get(context.Background(), orgID, id); err == nil {
					return fmt.Errorf("group %s in organization %s still exists after destroy", id, orgID)
				} else if !circleci.IsNotFound(err) && !circleci.IsUnauthorized(err) {
					// The single-group GET route answers 403 for a missing group
					// (see group_resource.go's Read), not 404, so IsUnauthorized is
					// the branch that actually fires; IsNotFound is kept in case some
					// other path answers that instead. Any other error means the
					// destroy check itself could not run, which must be reported
					// rather than silently treated as success.
					return fmt.Errorf("checking whether group %s was deleted: %w", id, err)
				}
			}

			return nil
		},
	})
}

// TestAccGroupResourceNet_RejectedOnClassicOrganization proves, against a
// real classic (VCS-backed) organization, the 403 that
// requireStandaloneCapable's own comment says it cannot catch: a CircleCI
// Cloud organization that is not standalone. [NET] confirmed on gh-oauth-cci-1
// and gh-oauth-cci-2, both 403 "Permission denied.".
func TestAccGroupResourceNet_RejectedOnClassicOrganization(t *testing.T) {
	testAccPreCheck(t)
	orgID := testOrgID(t)
	testRequireClassicOrg(t)

	name := testUniqueGroupName(t, "tfacc-group-rejected")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccGroupNetConfig(orgID, name, "should be rejected"),
				ExpectError: regexp.MustCompile(`(?s)Permission denied`),
			},
		},
	})
}
