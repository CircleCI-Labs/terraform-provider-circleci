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

func TestAccContextEnvironmentVariableDataSource(t *testing.T) {
	contextID := testContextID(t)
	envVarName := testContextEnvVarName(t)
	dateRegex := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z$`)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Read testing
			{
				Config: testContextEnvironmentVariableDataSourceConfig(envVarName, contextID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_context_environment_variable.test",
						tfjsonpath.New("name"),
						knownvalue.StringExact(envVarName),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_context_environment_variable.test",
						tfjsonpath.New("context_id"),
						knownvalue.StringExact(contextID),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_context_environment_variable.test",
						tfjsonpath.New("created_at"),
						knownvalue.StringRegexp(dateRegex),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_context_environment_variable.test",
						tfjsonpath.New("updated_at"),
						knownvalue.StringRegexp(dateRegex),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_context_environment_variable.test",
						tfjsonpath.New("name"),
						knownvalue.StringExact(envVarName),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_context_environment_variable.test",
						tfjsonpath.New("context_id"),
						knownvalue.StringExact(contextID),
					),
				},
			},
		},
	})
}

func testContextEnvironmentVariableDataSourceConfig(name, contextID string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = "https://circleci.com/api/v2"
}

data "circleci_context_environment_variable" "test" {
  name = %[1]q
  context_id = %[2]q
}
`, name, contextID)
}
