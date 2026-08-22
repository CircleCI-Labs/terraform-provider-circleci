// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// TestAccContextDataSource used to read a pre-existing fixture context
// (CIRCLECI_TEST_<key>_CONTEXT_ID / _CONTEXT_NAME), which is never set in any
// of the four CI jobs' environment blocks (see .circleci/config.yml), so this
// test skipped on every run and had no live coverage at all. It now creates
// its own scratch context — the same pattern TestAccContextResource and
// TestAccContextRestrictionResource already use — so it needs only
// testOrgID, which every CI job does set.
//
// It also drops the config's hardcoded `provider "circleci" { host =
// "https://circleci.com/api/v2" }` block: that pinned every run to CircleCI
// Cloud regardless of CIRCLE_HOST, which would have been silently wrong on a
// CircleCI Server run. No test ever caught it because the fixture requirement
// above kept this test from running at all. Omitting the block lets the
// provider resolve the host the same way every other acceptance test in this
// package does.
//
// The restrictions assertion is unchanged: a freshly created context is born
// with exactly one restriction, an "All members" group grant whose value is
// the organization's own UUID (see circleci_context_restriction's package
// doc and TestAccContextRestrictionResource_GroupType). A test that creates
// its own context has to account for that restriction existing already,
// rather than assuming an empty list, or it would be surprised by it the
// first time it ran for real.
func TestAccContextDataSource(t *testing.T) {
	organizationID := testOrgID(t)
	contextName := "tf-acc-context-ds-" + rand.Text()
	dateRegex := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z$`)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testContextDataSourceConfig(organizationID, contextName),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_context.test_context",
						tfjsonpath.New("name"),
						knownvalue.StringExact(contextName),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_context.test_context",
						tfjsonpath.New("created_at"),
						knownvalue.StringRegexp(dateRegex),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_context.test_context",
						tfjsonpath.New("restrictions"),

						knownvalue.ListExact(
							[]knownvalue.Check{
								knownvalue.MapPartial(
									map[string]knownvalue.Check{
										"id":         knownvalue.StringExact(organizationID),
										"project_id": knownvalue.StringExact(""),
										"name":       knownvalue.StringExact("All members"),
										"type":       knownvalue.StringExact("group"),
										"value":      knownvalue.StringExact(organizationID),
									},
								),
							},
						),
					),
					// The data source must resolve to the same context the config
					// created, not merely one with the same name.
					statecheck.CompareValuePairs(
						"circleci_context.test_context",
						tfjsonpath.New("id"),
						"data.circleci_context.test_context",
						tfjsonpath.New("id"),
						compare.ValuesSame(),
					),
				},
			},
		},
	})
}

func testContextDataSourceConfig(organizationID, name string) string {
	return fmt.Sprintf(`
resource "circleci_context" "test_context" {
  name            = %[2]q
  organization_id = %[1]q
}

data "circleci_context" "test_context" {
  id = circleci_context.test_context.id
}
`, organizationID, name)
}
