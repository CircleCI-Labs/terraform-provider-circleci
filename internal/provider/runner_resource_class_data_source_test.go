// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccRunnerResourceClassDataSource(t *testing.T) {
	organizationId := testOrgID(t)
	resourceClass := testUniqueRunnerResourceClass(t, "acc-test-runner-ds")
	description := "Acceptance test runner resource class data source"
	uuidRegex := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create a resource class then read it back via the data source.
			// Verifies all three attributes and that the data source id matches
			// the resource id.
			{
				Config: testAccRunnerResourceClassDataSourceConfig(organizationId, resourceClass, description),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_runner_resource_class.test",
						tfjsonpath.New("resource_class"),
						knownvalue.StringExact(resourceClass),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_runner_resource_class.test",
						tfjsonpath.New("description"),
						knownvalue.StringExact(description),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_runner_resource_class.test",
						tfjsonpath.New("id"),
						knownvalue.StringRegexp(uuidRegex),
					),
					// Data source id must equal the resource id.
					statecheck.CompareValuePairs(
						"circleci_runner_resource_class.test",
						tfjsonpath.New("id"),
						"data.circleci_runner_resource_class.test",
						tfjsonpath.New("id"),
						compare.ValuesSame(),
					),
				},
			},
		},
	})
}

// The absent resource class is named uniquely per run for the same reason the
// created ones are, inverted: this test's assertion is that the name is *not*
// there, and a fixed name is one hand-created resource class away from making
// the test fail for a reason that has nothing to do with the provider. A
// random tail makes absence a property of the name rather than of the account's
// history.
func TestAccRunnerResourceClassDataSourceNotFound(t *testing.T) {
	organizationId := testOrgID(t)
	resourceClass := testUniqueRunnerResourceClass(t, "does-not-exist-acc")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccRunnerResourceClassDataSourceOnlyConfig(organizationId, resourceClass),
				ExpectError: regexp.MustCompile(`Runner resource class not found`),
			},
		},
	})
}

// TestAccRunnerResourceClassDataSourceInvalidFormat used to expect "Invalid
// resource_class format" — the error Read produces from its own slash check —
// but that check is unreachable through this data source today. Its
// resource_class attribute carries runnerResourceClassPattern (see the Schema
// method's comment: "the stricter, earlier check"), which requires exactly
// one "/" and so already rejects "noslash" during plan, before Read ever
// runs. That earlier belief was wrong even against the fake — nothing here
// needed a live API to find it — see
// TestRunnerResourceClassFormatIsRejectedEverywhere/no_namespace, which
// already covers the same input against every resource-class attribute
// including this one without touching the network. This asserts the real,
// current behavior: a plan-time validation failure.
func TestAccRunnerResourceClassDataSourceInvalidFormat(t *testing.T) {
	organizationId := testOrgID(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccRunnerResourceClassDataSourceOnlyConfig(organizationId, "noslash"),
				ExpectError: regexp.MustCompile(`must be in the format 'namespace/name'`),
			},
		},
	})
}

func testAccRunnerResourceClassDataSourceConfig(organizationId, resourceClass, description string) string {
	return fmt.Sprintf(`
resource "circleci_runner_resource_class" "test" {
  organization_id = %[1]q
  resource_class = %[2]q
  description    = %[3]q
}

data "circleci_runner_resource_class" "test" {
  organization_id = circleci_runner_resource_class.test.organization_id
  resource_class = circleci_runner_resource_class.test.resource_class
}
`, organizationId, resourceClass, description)
}

func testAccRunnerResourceClassDataSourceOnlyConfig(organizationId, resourceClass string) string {
	return fmt.Sprintf(`
data "circleci_runner_resource_class" "test" {
  organization_id = %[1]q
  resource_class = %[2]q
}
`, organizationId, resourceClass)
}
