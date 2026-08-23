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

// This used to read the pre-existing fixture context and variable
// (CIRCLECI_TEST_<key>_CONTEXT_ID / _CONTEXT_ENV_VAR_NAME), neither of which
// any CI job's environment block sets, so it never ran in CI. It now creates
// its own scratch context and variable, the same way
// TestAccContextEnvironmentVariableResource does, so the only fixture it
// needs is testOrgID.
func TestAccContextEnvironmentVariableDataSource(t *testing.T) {
	organizationID := testOrgID(t)
	contextName := "tf-acc-ctx-envvar-ds-" + rand.Text()
	envVarName := fmt.Sprintf("N%s", rand.Text())
	envVarValue := rand.Text()
	dateRegex := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z$`)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testContextEnvironmentVariableDataSourceConfig(organizationID, contextName, envVarName, envVarValue),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_context_environment_variable.test",
						tfjsonpath.New("name"),
						knownvalue.StringExact(envVarName),
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
					statecheck.CompareValuePairs(
						"circleci_context.test_context",
						tfjsonpath.New("id"),
						"data.circleci_context_environment_variable.test",
						tfjsonpath.New("context_id"),
						compare.ValuesSame(),
					),
				},
			},
		},
	})
}

func testContextEnvironmentVariableDataSourceConfig(organizationID, contextName, name, value string) string {
	return fmt.Sprintf(`
resource "circleci_context" "test_context" {
  organization_id = %[1]q
  name             = %[2]q
}

resource "circleci_context_environment_variable" "test_env" {
  context_id = circleci_context.test_context.id
  name       = %[3]q
  value      = %[4]q
}

data "circleci_context_environment_variable" "test" {
  name       = circleci_context_environment_variable.test_env.name
  context_id = circleci_context.test_context.id
}
`, organizationID, contextName, name, value)
}
