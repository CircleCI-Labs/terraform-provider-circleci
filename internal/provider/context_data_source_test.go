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

func TestAccContextDataSource(t *testing.T) {
	contextID := testContextID(t)
	contextName := testContextName(t)
	organizationID := testOrgID(t)
	dateRegex := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z$`)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Read testing
			{
				Config: testContextDataSourceConfig(contextID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_context.test_context",
						tfjsonpath.New("id"),
						knownvalue.StringExact(contextID),
					),
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
				},
			},
		},
	})
}

func testContextDataSourceConfig(contextID string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = "https://circleci.com/api/v2"
}

data "circleci_context" "test_context" {
  id = %[1]q
}
`, contextID)
}
