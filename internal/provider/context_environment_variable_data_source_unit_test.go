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

// contextEnvVarDataSourceUnitConfig builds a
// circleci_context_environment_variable data source config. Every call site in
// this package points context_id at contextEnvVarDataSourceUnitContextID; only
// the name varies, so only the name is a parameter.
func contextEnvVarDataSourceUnitConfig(host, name string) string {
	return contextFakeProviderConfig(host) + fmt.Sprintf(`
data "circleci_context_environment_variable" "test" {
  context_id = %[1]q
  name       = %[2]q
}
`, contextEnvVarDataSourceUnitContextID, name)
}

func TestContextEnvVarDataSourceUnit_Found(t *testing.T) {
	api, host := newContextFakeAPI(t)
	api.seedContext(contextEnvVarDataSourceUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")
	api.seedEnvVar(contextEnvVarDataSourceUnitContextID, "API_KEY", "s3cr3t", "2024-01-02T03:04:05.000Z", "2024-02-03T04:05:06.000Z")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: contextEnvVarDataSourceUnitConfig(host, "API_KEY"),
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
			Config: contextEnvVarDataSourceUnitConfig(host, "MISSING"),
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
			Config:      contextEnvVarDataSourceUnitConfig(host, "API_KEY"),
			ExpectError: regexp.MustCompile(`(?s)Unable to read CircleCI context environment variable`),
		}},
	})
}

// TestContextEnvVarDataSourceUnit_TruncatedListStillFindsAVisibleVariable is the
// pair to the resource's ReadOnTruncatedList tests, for the data source that has
// to list because no route reads one variable by name (measured: 404).
//
// A context of more than 100 variables used to make this data source fail for
// every name, including one the API had just handed back. The requested variable
// being present is the whole question; whether other variables fell off the end
// of the list has no bearing on it.
func TestContextEnvVarDataSourceUnit_TruncatedListStillFindsAVisibleVariable(t *testing.T) {
	api, host := newContextFakeAPI(t)
	api.seedContext(contextEnvVarDataSourceUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")
	api.seedEnvVar(contextEnvVarDataSourceUnitContextID, "API_KEY", "s3cr3t", "2024-01-02T03:04:05.000Z", "2024-02-03T04:05:06.000Z")
	// "Z" sorts after "API_KEY", so API_KEY stays on the disclosed page.
	seedEnvVarsPastThePage(api, contextEnvVarDataSourceUnitContextID, "ZPAD")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: contextEnvVarDataSourceUnitConfig(host, "API_KEY"),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_context_environment_variable.test",
					tfjsonpath.New("created_at"),
					knownvalue.StringExact("2024-01-02T03:04:05.000Z"),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_context_environment_variable.test",
					tfjsonpath.New("updated_at"),
					knownvalue.StringExact("2024-02-03T04:05:06.000Z"),
				),
			},
		}},
	})
}

// TestContextEnvVarDataSourceUnit_TruncatedListErrorsForAHiddenVariable covers
// the other side.
//
// A name absent from a complete list is documented as "not an error, just
// unset attributes" (see TestContextEnvVarDataSourceUnit_NotFound). That
// silence is only defensible when the list was complete. Applying it to a
// truncated list would report "no such variable" for one that exists, so this
// case errors instead of returning the same empty answer for two different
// situations.
func TestContextEnvVarDataSourceUnit_TruncatedListErrorsForAHiddenVariable(t *testing.T) {
	api, host := newContextFakeAPI(t)
	api.seedContext(contextEnvVarDataSourceUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")
	api.seedEnvVar(contextEnvVarDataSourceUnitContextID, "ZZ_LAST", "s3cr3t", "2024-01-02T03:04:05.000Z", "2024-02-03T04:05:06.000Z")
	// "APAD" sorts before "ZZ_LAST", pushing it past the boundary.
	seedEnvVarsPastThePage(api, contextEnvVarDataSourceUnitContextID, "APAD")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: contextEnvVarDataSourceUnitConfig(host, "ZZ_LAST"),
			ExpectError: wrappedDiagnostic(
				"and ZZ_LAST was not among the 100 it disclosed, so Terraform cannot say whether it exists"),
		}},
	})
}
