// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

func testAccProjectGroupsDataSourceConfig(host, deployment string) string {
	return testAccMembershipProviderConfig(host, deployment) + fmt.Sprintf(`
data "circleci_project_groups" "test" {
  organization_id = %[1]q
  project_id      = %[2]q
}
`, testPGOrgID, testPGProjectID)
}

func TestAccProjectGroupsDataSource(t *testing.T) {
	api, host := newMockProjectGroupAPI(t)
	api.setRole(testPGProjectID, testPGGroupA, circleci.ProjectRoleAdmin)
	api.setRole(testPGProjectID, testPGGroupB, circleci.ProjectRoleViewer)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectGroupsDataSourceConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_project_groups.test",
						tfjsonpath.New("groups"),
						knownvalue.ListSizeExact(2),
					),
					// The mock lists grants sorted by group id, so group A is first.
					statecheck.ExpectKnownValue(
						"data.circleci_project_groups.test",
						tfjsonpath.New("groups").AtSliceIndex(0).AtMapKey("id"),
						knownvalue.StringExact(testPGGroupA),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project_groups.test",
						tfjsonpath.New("groups").AtSliceIndex(0).AtMapKey("name"),
						knownvalue.StringExact("platform"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project_groups.test",
						tfjsonpath.New("groups").AtSliceIndex(0).AtMapKey("role"),
						knownvalue.StringExact(circleci.ProjectRoleAdmin),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project_groups.test",
						tfjsonpath.New("groups").AtSliceIndex(1).AtMapKey("id"),
						knownvalue.StringExact(testPGGroupB),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_project_groups.test",
						tfjsonpath.New("groups").AtSliceIndex(1).AtMapKey("role"),
						knownvalue.StringExact(circleci.ProjectRoleViewer),
					),
				},
			},
		},
	})
}

func TestAccProjectGroupsDataSource_noGroups(t *testing.T) {
	_, host := newMockProjectGroupAPI(t)

	// An empty, non-null list keeps for_each and length() working against a
	// project with no groups assigned.
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectGroupsDataSourceConfig(host, "cloud"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_project_groups.test",
						tfjsonpath.New("groups"),
						knownvalue.ListSizeExact(0),
					),
				},
			},
		},
	})
}
