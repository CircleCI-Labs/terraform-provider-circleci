// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

// This file is the real-API ([NET]) counterpart to
// group_membership_resource_test.go, which is entirely fake-backed and, per
// that file's own header comment, CANNOT reach the real private API through a
// full Terraform run -- Client.PrivateHost() has no provider-schema attribute
// to redirect, so the fake tests drive Create/Read/Update/Delete directly.
//
// That limitation is exactly what makes a real provider "circleci" {} block
// here meaningful rather than redundant: with no override available, a
// TF_ACC=1 run of this file is the ONLY way this resource is ever driven
// through an actual `terraform apply`, against the actual
// https://app.circleci.com private origin.
//
// The one user every one of these tests can safely add to and remove from a
// group without depending on any other account existing is the token's own
// owner (testCurrentUserID) -- TESTING.md requires that token to belong to an
// organization admin, so it is guaranteed to already be a member of the
// organization under test.

func testAccGroupMembershipNetConfig(orgID, groupName, userIDsHCL string) string {
	return testAccGroupNetProviderConfig + fmt.Sprintf(`
resource "circleci_group" "net_test" {
  org_id = %[1]q
  name   = %[2]q
}

resource "circleci_group_membership" "net_test" {
  org_id   = %[1]q
  group_id = circleci_group.net_test.id
  user_ids = %[3]s
}

data "circleci_group_membership" "net_test" {
  org_id   = %[1]q
  group_id = circleci_group.net_test.id

  depends_on = [circleci_group_membership.net_test]
}
`, orgID, groupName, userIDsHCL)
}

// testAccCheckGroupNetMembership independently confirms, with a direct API
// call bypassing Terraform entirely, that a group's live membership matches
// want exactly. This is the "actually taking effect server-side" assertion
// the task calls for: Terraform's own state after apply is Read's return
// value, so trusting only ConfigStateChecks here would just be checking that
// the provider agrees with itself.
func testAccCheckGroupNetMembership(orgID string, want []string) resource.TestCheckFunc {
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

		members, err := client.GroupMembership().List(context.Background(), orgID, groupID)
		if err != nil {
			return fmt.Errorf("listing live membership of group %s: %w", groupID, err)
		}

		got := memberIDs(members)
		gotSet := map[string]bool{}
		for _, id := range got {
			gotSet[id] = true
		}
		wantSet := map[string]bool{}
		for _, id := range want {
			wantSet[id] = true
		}

		if len(gotSet) != len(wantSet) {
			return fmt.Errorf("live membership of group %s = %v, want %v", groupID, got, want)
		}
		for id := range wantSet {
			if !gotSet[id] {
				return fmt.Errorf("live membership of group %s = %v, want it to contain %s", groupID, got, id)
			}
		}

		return nil
	}
}

// TestAccGroupMembershipResourceNet_Lifecycle proves add and remove actually
// take effect server-side, in both directions (empty -> populated and
// populated -> empty), plus import, against a real standalone organization.
func TestAccGroupMembershipResourceNet_Lifecycle(t *testing.T) {
	testAccPreCheck(t)
	testRequireStandaloneOrg(t, testOrgSlug(t))

	orgID := testOrgID(t)
	groupName := testUniqueGroupName(t, "tfacc-membership")
	userID := testCurrentUserID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create with one member.
			{
				Config: testAccGroupMembershipNetConfig(orgID, groupName, fmt.Sprintf("[%q]", userID)),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_group_membership.net_test", tfjsonpath.New("user_ids"),
						knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact(userID)}),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_group_membership.net_test", tfjsonpath.New("user_ids"),
						knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact(userID)}),
					),
				},
				Check: testAccCheckGroupNetMembership(orgID, []string{userID}),
			},
			// Import, using the "org_id/group_id" form.
			{
				ResourceName:      "circleci_group_membership.net_test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs, ok := s.RootModule().Resources["circleci_group_membership.net_test"]
					if !ok {
						return "", fmt.Errorf("circleci_group_membership.net_test not found in state")
					}

					return rs.Primary.Attributes["org_id"] + "/" + rs.Primary.Attributes["group_id"], nil
				},
			},
			// Remove the only member: an in-place update, not a replace, and it
			// must actually empty the group server-side.
			{
				Config: testAccGroupMembershipNetConfig(orgID, groupName, "[]"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_group_membership.net_test", tfjsonpath.New("user_ids"),
						knownvalue.SetExact(nil),
					),
				},
				Check: testAccCheckGroupNetMembership(orgID, nil),
			},
			// Add the member back, proving the add path works after a remove, not
			// only on initial create.
			{
				Config: testAccGroupMembershipNetConfig(orgID, groupName, fmt.Sprintf("[%q]", userID)),
				Check:  testAccCheckGroupNetMembership(orgID, []string{userID}),
			},
		},
		// The membership resource's own Delete only removes the users it manages
		// (see group_membership_resource.go); the group itself is destroyed
		// afterward because circleci_group.net_test is also torn down at the end
		// of this test. Confirming the group is gone is a reasonable proxy for
		// "the membership was cleaned up too", since a gone group has none.
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

			return nil
		},
	})
}
