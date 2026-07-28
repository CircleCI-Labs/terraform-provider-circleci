// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func testAccGroupMembershipDataSourceConfig(host, deployment string) string {
	return testAccMembershipProviderConfig(host, deployment) + fmt.Sprintf(`
data "circleci_group_membership" "test" {
  organization_id = %[1]q
  group_id        = %[2]q
}
`, testMembershipOrgID, testMembershipGroupID)
}

func TestAccGroupMembershipDataSource(t *testing.T) {
	api, host := newMockMembershipAPI(t)
	api.seedMembers(testUserA, testUserB)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGroupMembershipDataSourceConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					// user_ids is the shape the resource consumes.
					statecheck.ExpectKnownValue(
						"data.circleci_group_membership.test",
						tfjsonpath.New("user_ids"),
						knownvalue.SetExact([]knownvalue.Check{
							knownvalue.StringExact(testUserA),
							knownvalue.StringExact(testUserB),
						}),
					),
					// members carries the display fields alongside each id.
					statecheck.ExpectKnownValue(
						"data.circleci_group_membership.test",
						tfjsonpath.New("members"),
						knownvalue.ListSizeExact(2),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_group_membership.test",
						tfjsonpath.New("members").AtSliceIndex(0).AtMapKey("user_id"),
						knownvalue.StringExact(testUserA),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_group_membership.test",
						tfjsonpath.New("members").AtSliceIndex(0).AtMapKey("username"),
						knownvalue.StringExact("user-"+testUserA[:4]),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_group_membership.test",
						tfjsonpath.New("members").AtSliceIndex(0).AtMapKey("email"),
						knownvalue.StringExact(testUserA[:4]+"@example.com"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_group_membership.test",
						tfjsonpath.New("members").AtSliceIndex(0).AtMapKey("avatar_url"),
						knownvalue.StringExact("https://avatars.example/"+testUserA[:4]+".png"),
					),
				},
			},
		},
	})
}

func TestAccGroupMembershipDataSource_emptyGroup(t *testing.T) {
	_, host := newMockMembershipAPI(t)

	// An empty, non-null list keeps for_each and length() working against a group
	// with no members.
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGroupMembershipDataSourceConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_group_membership.test",
						tfjsonpath.New("members"),
						knownvalue.ListSizeExact(0),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_group_membership.test",
						tfjsonpath.New("user_ids"),
						knownvalue.SetSizeExact(0),
					),
				},
			},
		},
	})
}

func TestAccGroupMembershipDataSource_serverDeployment(t *testing.T) {
	_, host := newMockMembershipAPI(t)

	// Groups need a `circleci` type (standalone) organization. A CircleCI Server
	// installation is always a `github` type organization, so deployment =
	// "server" must be rejected with an explanatory error rather than attempting
	// a request the API would refuse.
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccGroupMembershipDataSourceConfig(host, "server"),
			ExpectError: regexp.MustCompile(`circleci_group_membership requires a standalone CircleCI organization`),
		}},
	})
}
