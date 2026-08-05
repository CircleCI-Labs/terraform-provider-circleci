// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// These are fake-server-backed unit tests for
// context_environment_variables_data_source.go: they need no TF_ACC and no
// CircleCI credentials, reusing contextFakeAPI (context_fake_test.go) rather
// than duplicating its wire shapes — the same fake already backs
// circleci_context_environment_variable and circleci_context_environment_variable
// (resource), and its getEnvVars handler is the one place in this package that
// reproduces truncated_value's real shape (see truncateContextEnvVarValue).

const contextEnvVarsDataSourceUnitContextID = "ctx-plural-1"

func contextEnvVarsDataSourceUnitConfig(host, contextID string) string {
	return contextFakeProviderConfig(host) + fmt.Sprintf(`
data "circleci_context_environment_variables" "test" {
  context_id = %[1]q
}
`, contextID)
}

func TestContextEnvironmentVariablesDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewContextEnvironmentVariablesDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	contextID, ok := resp.Schema.Attributes["context_id"]
	if !ok {
		t.Fatal("schema is missing the context_id attribute")
	}
	if !contextID.IsRequired() {
		t.Error("context_id is not required, but it is the scope of the listing")
	}

	variables, ok := resp.Schema.Attributes["environment_variables"]
	if !ok {
		t.Fatal("schema is missing the environment_variables attribute")
	}
	if !variables.IsComputed() {
		t.Error("environment_variables is not computed, but it is entirely API-derived")
	}
}

// TestContextEnvironmentVariablesDataSourceUnit_Read covers the shape that
// matters most: every variable on the context is reported, including ones
// seeded independently of one another, and truncated_value comes back as a
// bare tail with no "xxxx" mask prefix — the shape production actually sends,
// per internal/circleci/environment_variable.go's ContextEnvironmentVariable
// doc comment.
func TestContextEnvironmentVariablesDataSourceUnit_Read(t *testing.T) {
	api, host := newContextFakeAPI(t)
	api.seedContext(contextEnvVarsDataSourceUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")

	// Seeded independently, in reverse alphabetical order, so a passing sort
	// check proves the data source relies on the API's ordering rather than
	// happening to match insertion order.
	api.seedEnvVar(contextEnvVarsDataSourceUnitContextID, "ZONE", "FOOBARBAZ", "2024-02-01T00:00:00.000Z", "2024-02-02T00:00:00.000Z")
	api.seedEnvVar(contextEnvVarsDataSourceUnitContextID, "API_KEY", "s3cr3t", "2024-01-05T00:00:00.000Z", "2024-01-06T00:00:00.000Z")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: contextEnvVarsDataSourceUnitConfig(host, contextEnvVarsDataSourceUnitContextID),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_context_environment_variables.test",
					tfjsonpath.New("environment_variables"),
					knownvalue.ListSizeExact(2),
				),
				// Sorted by name: API_KEY before ZONE.
				statecheck.ExpectKnownValue(
					"data.circleci_context_environment_variables.test",
					tfjsonpath.New("environment_variables").AtSliceIndex(0).AtMapKey("name"),
					knownvalue.StringExact("API_KEY"),
				),
				// "s3cr3t" is 6 characters, so min(4, 6/2) = 3 are revealed: "r3t".
				statecheck.ExpectKnownValue(
					"data.circleci_context_environment_variables.test",
					tfjsonpath.New("environment_variables").AtSliceIndex(0).AtMapKey("truncated_value"),
					knownvalue.StringExact("r3t"),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_context_environment_variables.test",
					tfjsonpath.New("environment_variables").AtSliceIndex(0).AtMapKey("created_at"),
					knownvalue.StringExact("2024-01-05T00:00:00.000Z"),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_context_environment_variables.test",
					tfjsonpath.New("environment_variables").AtSliceIndex(0).AtMapKey("updated_at"),
					knownvalue.StringExact("2024-01-06T00:00:00.000Z"),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_context_environment_variables.test",
					tfjsonpath.New("environment_variables").AtSliceIndex(1).AtMapKey("name"),
					knownvalue.StringExact("ZONE"),
				),
				// "FOOBARBAZ" is 9 characters, so the full 4-character tail is
				// revealed, with no "xxxx" prefix: "RBAZ", not "xxxxRBAZ".
				statecheck.ExpectKnownValue(
					"data.circleci_context_environment_variables.test",
					tfjsonpath.New("environment_variables").AtSliceIndex(1).AtMapKey("truncated_value"),
					knownvalue.StringExact("RBAZ"),
				),
			},
		}},
	})
}

// TestContextEnvironmentVariablesDataSourceUnit_Empty checks that a context
// with no variables reads back an empty (not null) list.
func TestContextEnvironmentVariablesDataSourceUnit_Empty(t *testing.T) {
	api, host := newContextFakeAPI(t)
	api.seedContext(contextEnvVarsDataSourceUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: contextEnvVarsDataSourceUnitConfig(host, contextEnvVarsDataSourceUnitContextID),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_context_environment_variables.test",
					tfjsonpath.New("environment_variables"),
					knownvalue.ListSizeExact(0),
				),
			},
		}},
	})
}

// TestContextEnvironmentVariablesDataSourceUnit_APIError checks that a
// non-2xx list response surfaces as a diagnostic rather than propagating a
// raw error or panicking. This also covers the 403-for-a-missing-context
// shape the API actually sends for a context that cannot be resolved (see
// contextFakeAPI's doc comment), since that same 403 goes through the
// failed() path this test exercises directly.
func TestContextEnvironmentVariablesDataSourceUnit_APIError(t *testing.T) {
	api, host := newContextFakeAPI(t)
	api.seedContext(contextEnvVarsDataSourceUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")
	api.fail(403, "Forbidden")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      contextEnvVarsDataSourceUnitConfig(host, contextEnvVarsDataSourceUnitContextID),
			ExpectError: regexp.MustCompile(`(?s)Unable to list CircleCI environment variables.*Forbidden`),
		}},
	})
}
