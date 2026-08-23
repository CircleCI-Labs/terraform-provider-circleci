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

// This file is the real-API ([NET]) counterpart to
// project_group_resource_test.go and project_groups_data_source_test.go, both
// entirely fake-backed. project_group.go's routes are plain /api/v2, so these
// run through a normal `provider "circleci" {}` block and a real
// `resource.Test`.

func testAccProjectGroupNetConfig(orgID, projectID, groupName, role string) string {
	return testAccGroupNetProviderConfig + fmt.Sprintf(`
resource "circleci_group" "net_test" {
  org_id = %[1]q
  name   = %[3]q
}

resource "circleci_project_group" "net_test" {
  org_id     = %[1]q
  project_id = %[2]q
  group_id   = circleci_group.net_test.id
  role       = %[4]q
}

data "circleci_project_groups" "net_test" {
  org_id     = %[1]q
  project_id = %[2]q

  depends_on = [circleci_project_group.net_test]
}
`, orgID, projectID, groupName, role)
}

// testAccProjectGroupNetConfigGroupOnly is the same fixture with the grant
// resource removed, leaving only the group -- used to observe a real destroy
// of circleci_project_group in isolation (see TestAccProjectGroupResourceNet_
// Lifecycle's last apply step) before the final teardown destroys the group
// itself.
func testAccProjectGroupNetConfigGroupOnly(orgID, groupName string) string {
	return testAccGroupNetProviderConfig + fmt.Sprintf(`
resource "circleci_group" "net_test" {
  org_id = %[1]q
  name   = %[2]q
}
`, orgID, groupName)
}

// TestAccProjectGroupResourceNet_Lifecycle exercises create, the in-place role
// update, import, and the documented "destroy warns but does not revoke"
// behaviour against a real standalone organization and its writable test
// project.
func TestAccProjectGroupResourceNet_Lifecycle(t *testing.T) {
	testAccPreCheck(t)
	testRequireStandaloneOrg(t, testOrgSlug(t))

	orgID := testOrgID(t)
	projectID := testProjectID(t)
	groupName := testUniqueGroupName(t, "tfacc-projectgroup")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create with project-viewer.
			{
				Config: testAccProjectGroupNetConfig(orgID, projectID, groupName, circleci.ProjectRoleViewer),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_project_group.net_test", tfjsonpath.New("role"),
						knownvalue.StringExact(circleci.ProjectRoleViewer),
					),
					statecheck.ExpectKnownValue(
						"circleci_project_group.net_test", tfjsonpath.New("name"),
						knownvalue.StringExact(groupName),
					),
				},
				Check: testAccCheckProjectGroupNetRole(orgID, projectID, circleci.ProjectRoleViewer),
			},
			// The role can change in place -- everything else on this resource
			// forces replacement, but role has a real Update method.
			{
				Config: testAccProjectGroupNetConfig(orgID, projectID, groupName, circleci.ProjectRoleAdmin),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("circleci_project_group.net_test", plancheck.ResourceActionUpdate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_project_group.net_test", tfjsonpath.New("role"),
						knownvalue.StringExact(circleci.ProjectRoleAdmin),
					),
				},
				// The project may legitimately carry other groups' grants too (a
				// concurrent run against a different group on the same project, or
				// leftovers this test's own cascading-delete cleanup has not reached
				// yet), so this checks presence of the specific grant this test
				// manages, never an exact count.
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckProjectGroupNetRole(orgID, projectID, circleci.ProjectRoleAdmin),
					testAccCheckProjectGroupsDataSourceContains(groupName, circleci.ProjectRoleAdmin),
				),
			},
			// Import, using the "org_id/project_id/group_id" form.
			{
				ResourceName:      "circleci_project_group.net_test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs, ok := s.RootModule().Resources["circleci_project_group.net_test"]
					if !ok {
						return "", fmt.Errorf("circleci_project_group.net_test not found in state")
					}

					return rs.Primary.Attributes["org_id"] + "/" +
						rs.Primary.Attributes["project_id"] + "/" +
						rs.Primary.Attributes["group_id"], nil
				},
			},
			// Dropping the grant resource from configuration destroys it in
			// Terraform's eyes, with a warning (there is no revoke route), but the
			// grant must survive live: this is the documented behaviour, measured
			// directly against the API rather than only through the fake.
			{
				Config: testAccProjectGroupNetConfigGroupOnly(orgID, groupName),
				Check:  testAccCheckProjectGroupNetRole(orgID, projectID, circleci.ProjectRoleAdmin),
			},
		},
		// The final teardown destroys circleci_group.net_test (still present in
		// the last step's config), which -- measured [NET] -- cascades and
		// removes the grant from the project's group list even though no revoke
		// route exists. This independently confirms both are actually gone.
		CheckDestroy: func(*terraform.State) error {
			client := circleci.New(circleci.Config{Token: os.Getenv("CIRCLE_TOKEN")})

			groups, err := client.Groups().List(context.Background(), orgID)
			if err != nil {
				return fmt.Errorf("listing groups in %s to confirm cleanup: %w", orgID, err)
			}
			for _, g := range groups {
				if g.Name == groupName {
					return fmt.Errorf("group %q still exists in organization %s after destroy", groupName, orgID)
				}
			}

			grants, err := client.ProjectGroups().List(context.Background(), orgID, projectID)
			if err != nil {
				return fmt.Errorf("listing project groups for %s to confirm cleanup: %w", projectID, err)
			}
			for _, g := range grants {
				if g.Name == groupName {
					return fmt.Errorf("project %s still has a grant for group %q after destroy", projectID, groupName)
				}
			}

			return nil
		},
	})
}

// testAccCheckProjectGroupNetRole independently confirms, with a direct API
// call bypassing Terraform, that the project's live grant for the managed
// group carries wantRole -- the "actually taking effect server-side"
// assertion for the in-place role update.
func testAccCheckProjectGroupNetRole(orgID, projectID, wantRole string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources["circleci_group.net_test"]
		if !ok {
			return fmt.Errorf("circleci_group.net_test not found in state")
		}
		groupID := rs.Primary.ID
		if groupID == "" {
			return fmt.Errorf("circleci_group.net_test has no id in state")
		}

		client := circleci.New(circleci.Config{Token: os.Getenv("CIRCLE_TOKEN")})

		grant, err := client.ProjectGroups().Get(context.Background(), orgID, projectID, groupID)
		if err != nil {
			return fmt.Errorf("reading live project group grant for %s on project %s: %w", groupID, projectID, err)
		}

		if grant.Role != wantRole {
			return fmt.Errorf("live role for group %s on project %s = %q, want %q", groupID, projectID, grant.Role, wantRole)
		}

		return nil
	}
}

// testAccCheckProjectGroupsDataSourceContains checks that
// data.circleci_project_groups.net_test's "groups" list contains an entry for
// groupName with wantRole, without requiring it to be the only entry: the
// project may carry other groups' grants at the same time (see the caller).
func testAccCheckProjectGroupsDataSourceContains(groupName, wantRole string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources["data.circleci_project_groups.net_test"]
		if !ok {
			return fmt.Errorf("data.circleci_project_groups.net_test not found in state")
		}

		countRaw, ok := rs.Primary.Attributes["groups.#"]
		if !ok {
			return fmt.Errorf("data.circleci_project_groups.net_test has no groups.# attribute in state")
		}

		var count int
		if _, err := fmt.Sscanf(countRaw, "%d", &count); err != nil {
			return fmt.Errorf("groups.# = %q is not a number: %w", countRaw, err)
		}

		for i := 0; i < count; i++ {
			if rs.Primary.Attributes[fmt.Sprintf("groups.%d.name", i)] != groupName {
				continue
			}

			if role := rs.Primary.Attributes[fmt.Sprintf("groups.%d.role", i)]; role != wantRole {
				return fmt.Errorf("data.circleci_project_groups.net_test lists group %q with role %q, want %q",
					groupName, role, wantRole)
			}

			return nil
		}

		return fmt.Errorf("data.circleci_project_groups.net_test does not list group %q", groupName)
	}
}

// TestAccProjectGroupNet_ListOnClassicOrgAnswers400 proves, against a real
// classic organization's project, the third documented status this route can
// answer: 400 "Endpoint is not supported for this organization.", distinct
// from both the 200-empty-list case (any organization with no grants) and the
// 403 a missing organization or project answers.
func TestAccProjectGroupNet_ListOnClassicOrgAnswers400(t *testing.T) {
	testAccPreCheck(t)
	testRequireClassicOrg(t)

	orgID := testOrgID(t)
	projectID := testProjectID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGroupNetProviderConfig + fmt.Sprintf(`
data "circleci_project_groups" "net_test" {
  org_id     = %[1]q
  project_id = %[2]q
}
`, orgID, projectID),
				ExpectError: regexp.MustCompile(`(?s)Endpoint is not supported`),
			},
		},
	})
}
