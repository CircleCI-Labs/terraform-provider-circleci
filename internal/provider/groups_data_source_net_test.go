// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// This file is the real-API ([NET]) counterpart to
// groups_data_source_test.go, which is entirely fake-backed.
//
// It does not attempt to re-measure whether circleci_groups' internal
// pagination (DrainV2) ever crosses a page boundary: that was established by
// hand, once, by creating groups on a live standalone organization one at a
// time up to 100 -- see the [NET, 2026-08-22] comment on GroupService.List in
// internal/circleci/group.go. At every count up to the organization's own
// hard cap (409 "Exceeded max number of groups in org: 100." beyond that),
// the route answered a single page with next_page_token always null, so
// there is no boundary for a repeatable test to cross, and creating and
// deleting 100 groups on every CI run to re-confirm a fact about the API's
// architecture (not about this provider's code) would cost far more than it
// proves. What this test does check, on every run, is the shape that
// matters for regression: with more than one group actually present, the
// data source still lists every one of them rather than silently truncating.
func TestAccGroupsDataSourceNet_ListsCreatedGroups(t *testing.T) {
	testAccPreCheck(t)
	testRequireStandaloneOrg(t, testOrgSlug(t))

	orgID := testOrgID(t)
	nameA := testUniqueGroupName(t, "tfacc-groups-a")
	nameB := testUniqueGroupName(t, "tfacc-groups-b")
	nameC := testUniqueGroupName(t, "tfacc-groups-c")

	config := testAccGroupNetProviderConfig + fmt.Sprintf(`
resource "circleci_group" "a" {
  org_id = %[1]q
  name   = %[2]q
}

resource "circleci_group" "b" {
  org_id = %[1]q
  name   = %[3]q
}

resource "circleci_group" "c" {
  org_id = %[1]q
  name   = %[4]q
}

data "circleci_groups" "net_test" {
  org_id = %[1]q

  depends_on = [circleci_group.a, circleci_group.b, circleci_group.c]
}
`, orgID, nameA, nameB, nameC)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: func(s *terraform.State) error {
					rs, ok := s.RootModule().Resources["data.circleci_groups.net_test"]
					if !ok {
						return fmt.Errorf("data.circleci_groups.net_test not found in state")
					}

					countRaw, ok := rs.Primary.Attributes["groups.#"]
					if !ok {
						return fmt.Errorf("data.circleci_groups.net_test has no groups.# attribute in state")
					}

					count, err := strconv.Atoi(countRaw)
					if err != nil {
						return fmt.Errorf("data.circleci_groups.net_test groups.# = %q is not a number: %w", countRaw, err)
					}

					// The organization may legitimately hold other groups too (this
					// suite's own concurrent runs against a different fixture, or
					// pre-existing ones), so this asserts presence, never an exact
					// count.
					if count < 3 {
						return fmt.Errorf("data.circleci_groups.net_test reported %d groups, want at least the 3 created here", count)
					}

					wantNames := map[string]bool{nameA: false, nameB: false, nameC: false}
					for i := 0; i < count; i++ {
						name := rs.Primary.Attributes[fmt.Sprintf("groups.%d.name", i)]
						if _, ok := wantNames[name]; ok {
							wantNames[name] = true
						}
					}

					for name, found := range wantNames {
						if !found {
							return fmt.Errorf("data.circleci_groups.net_test did not list group %q, which was just created", name)
						}
					}

					return nil
				},
			},
		},
	})
}
