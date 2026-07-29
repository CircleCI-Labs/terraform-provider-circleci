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

// These are fake-server-backed unit tests for
// context_environment_variable_data_source.go: they need no TF_ACC and no
// CircleCI credentials, unlike TestAccContextEnvironmentVariableDataSource.

const contextEnvVarDataSourceUnitContextID = "ctx-fixed-4"

func contextEnvVarDataSourceUnitConfig(host, contextID, name string) string {
	return contextFakeProviderConfig(host) + fmt.Sprintf(`
data "circleci_context_environment_variable" "test" {
  context_id = %[1]q
  name       = %[2]q
}
`, contextID, name)
}

func TestContextEnvVarDataSourceUnit_Found(t *testing.T) {
	api, host := newContextFakeAPI(t)
	api.seedContext(contextEnvVarDataSourceUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")
	api.seedEnvVar(contextEnvVarDataSourceUnitContextID, "API_KEY", "s3cr3t", "2024-01-02T03:04:05.000Z", "2024-02-03T04:05:06.000Z")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: contextEnvVarDataSourceUnitConfig(host, contextEnvVarDataSourceUnitContextID, "API_KEY"),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("data.circleci_context_environment_variable.test", tfjsonpath.New("name"), knownvalue.StringExact("API_KEY")),
				statecheck.ExpectKnownValue("data.circleci_context_environment_variable.test", tfjsonpath.New("context_id"), knownvalue.StringExact(contextEnvVarDataSourceUnitContextID)),
				statecheck.ExpectKnownValue("data.circleci_context_environment_variable.test", tfjsonpath.New("created_at"), knownvalue.StringExact("2024-01-02T03:04:05.000Z")),
				statecheck.ExpectKnownValue("data.circleci_context_environment_variable.test", tfjsonpath.New("updated_at"), knownvalue.StringExact("2024-02-03T04:05:06.000Z")),
			},
		}},
	})
}

// TestContextEnvVarDataSourceUnit_NotFound documents the current behavior when
// no environment variable matches the requested name: Read (context_environment_variable_data_source.go)
// does not error, it just leaves created_at/updated_at unset.
func TestContextEnvVarDataSourceUnit_NotFound(t *testing.T) {
	api, host := newContextFakeAPI(t)
	api.seedContext(contextEnvVarDataSourceUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: contextEnvVarDataSourceUnitConfig(host, contextEnvVarDataSourceUnitContextID, "MISSING"),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("data.circleci_context_environment_variable.test", tfjsonpath.New("name"), knownvalue.StringExact("MISSING")),
				statecheck.ExpectKnownValue("data.circleci_context_environment_variable.test", tfjsonpath.New("created_at"), knownvalue.Null()),
				statecheck.ExpectKnownValue("data.circleci_context_environment_variable.test", tfjsonpath.New("updated_at"), knownvalue.Null()),
			},
		}},
	})
}

// TestContextEnvVarDataSourceUnit_APIError proves a 4xx from the API surfaces
// as a Terraform diagnostic rather than a panic.
// TestContextEnvVarDataSourceUnit_NoValueAttributeExposed asserts the design
// rule that a value the API never returns is not exposed as a string a
// configuration could mistake for the real thing (see DESIGN.md, "Values the
// API never returns are not exposed as strings"). The schema
// (context_environment_variable_data_source.go) has no "value" attribute at
// all, so referencing one is a configuration-time error rather than reading
// back "" or a mask.
func TestContextEnvVarDataSourceUnit_NoValueAttributeExposed(t *testing.T) {
	api, host := newContextFakeAPI(t)
	api.seedContext(contextEnvVarDataSourceUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")
	api.seedEnvVar(contextEnvVarDataSourceUnitContextID, "API_KEY", "s3cr3t", "2024-01-02T03:04:05.000Z", "2024-02-03T04:05:06.000Z")

	cfg := contextFakeProviderConfig(host) + fmt.Sprintf(`
data "circleci_context_environment_variable" "test" {
  context_id = %[1]q
  name       = "API_KEY"
}

output "leaked" {
  value = data.circleci_context_environment_variable.test.value
}
`, contextEnvVarDataSourceUnitContextID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`(?s)([Uu]nsupported attribute|does not have an attribute)`),
		}},
	})
}

func TestContextEnvVarDataSourceUnit_APIError(t *testing.T) {
	api, host := newContextFakeAPI(t)
	api.seedContext(contextEnvVarDataSourceUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")
	api.fail(400, "context not found")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      contextEnvVarDataSourceUnitConfig(host, contextEnvVarDataSourceUnitContextID, "API_KEY"),
			ExpectError: regexp.MustCompile(`(?s)Unable to read CircleCI context environment variable`),
		}},
	})
}
