// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"strings"
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

// contextEnvVarsDataSourceUnitConfig builds a
// circleci_context_environment_variables config. Every call site in this package
// points context_id at contextEnvVarsDataSourceUnitContextID; that is fixed here
// rather than threaded through as a parameter that would never vary.
func contextEnvVarsDataSourceUnitConfig(host string) string {
	return contextFakeProviderConfig(host) + fmt.Sprintf(`
data "circleci_context_environment_variables" "test" {
  context_id = %[1]q
}
`, contextEnvVarsDataSourceUnitContextID)
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
			Config: contextEnvVarsDataSourceUnitConfig(host),
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
			Config: contextEnvVarsDataSourceUnitConfig(host),
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
			Config:      contextEnvVarsDataSourceUnitConfig(host),
			ExpectError: regexp.MustCompile(`(?s)Unable to list CircleCI environment variables.*Forbidden`),
		}},
	})
}

// wrappedDiagnostic builds a pattern matching a diagnostic phrase across the
// hard-wrapping Terraform applies to a diagnostic's detail, which puts a newline
// and indentation wherever a line happens to run long. Writing the phrase out
// and letting whitespace float is what keeps these assertions about the wording
// rather than about the column the wording landed in. "..." stands for elided
// text.
func wrappedDiagnostic(phrase string) *regexp.Regexp {
	parts := strings.Split(phrase, "...")
	for i, part := range parts {
		words := strings.Fields(part)
		for j, word := range words {
			words[j] = regexp.QuoteMeta(word)
		}
		parts[i] = strings.Join(words, `\s+`)
	}

	return regexp.MustCompile(`(?s)` + strings.Join(parts, `.*`))
}

// seedEnvVarsPastThePage seeds enough variables, named with the given prefix, to
// push the context one variable past what the list route will disclose.
//
// The prefix decides which side of the page boundary a variable under test lands
// on, because the API sorts by name: a prefix sorting after the variable under
// test leaves it on the disclosed page, and one sorting before it pushes it off.
func seedEnvVarsPastThePage(api *contextFakeAPI, contextID, prefix string) {
	for i := range fakeContextEnvVarPageSize {
		name := fmt.Sprintf("%s%03d", prefix, i)
		api.seedEnvVar(contextID, name, "padding-value", "2024-03-01T00:00:00.000Z", "2024-03-01T00:00:00.000Z")
	}
}

// TestContextEnvironmentVariablesDataSourceUnit_TruncatedListErrors is the
// regression test for the plural data source under-reporting in silence.
//
// A context holding more variables than the list route discloses cannot be
// reported at all: this data source's whole contract is "every environment
// variable set on the context", and there is no way to fetch the rest — the page
// token is advertised and never read back, and no route reads one variable by
// name. See circleci.ContextEnvVarsTruncatedError for the measurements.
//
// So it fails loudly. Returning the disclosed page as if it were the collection
// is the worst option available: a for_each over it would quietly stop managing
// whatever fell off the end, with nothing in the plan to show it.
//
// The count assertion is the other half. Truncation is visible in the first
// response, so one GET is enough; a second cannot reach anything and only
// doubles the cost of the failure.
func TestContextEnvironmentVariablesDataSourceUnit_TruncatedListErrors(t *testing.T) {
	api, host := newContextFakeAPI(t)
	api.seedContext(contextEnvVarsDataSourceUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")
	seedEnvVarsPastThePage(api, contextEnvVarsDataSourceUnitContextID, "PAD")
	api.seedEnvVar(contextEnvVarsDataSourceUnitContextID, "ZONE", "FOOBARBAZ", "2024-02-01T00:00:00.000Z", "2024-02-02T00:00:00.000Z")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: contextEnvVarsDataSourceUnitConfig(host),
			// Terraform hard-wraps a diagnostic's detail, so the patterns skip
			// over whitespace rather than pinning the exact line breaks.
			ExpectError: wrappedDiagnostic(
				"disclosed 100 environment variables ... will not return a partial list"),
		}},
	})

	gets := 0
	for _, req := range api.recorded() {
		if req == "GET /api/v2/context/"+contextEnvVarsDataSourceUnitContextID+"/environment-variable" {
			gets++
		}
	}
	if gets != 1 {
		t.Errorf("made %d list requests, want 1: truncation is visible in the first response, and a "+
			"second request reaches nothing (%q)", gets, api.recorded())
	}
}

// TestTruncateContextEnvVarValueMatchesMeasuredAPI pins the fake's model of
// truncated_value against the real route, length by length.
//
// The formula is min(4, floor(len/2)) characters from the end, with no mask
// prefix. It appears in no published spec, so the table below is a measurement:
// values of these lengths were written to a real context and the list route read
// back. It is worth pinning because truncated_value is the only way to tell two
// similarly named variables apart in a UI, and because the fake got this wrong
// once already — it answered "xxxx" plus the last four characters, borrowing the
// *project* environment variable convention, and the client's doc comment carried
// the same mistake, so mock and client agreed with each other and with no
// service.
func TestTruncateContextEnvVarValueMatchesMeasuredAPI(t *testing.T) {
	t.Parallel()

	// Measured: value of length N (the first N letters of the alphabet) -> tail.
	for _, tc := range []struct{ value, want string }{
		{"", ""},
		{"A", ""},
		{"AB", "B"},
		{"ABC", "C"},
		{"ABCD", "CD"},
		{"ABCDE", "DE"},
		{"ABCDEF", "DEF"},
		{"ABCDEFG", "EFG"},
		{"ABCDEFGH", "EFGH"},
		{"ABCDEFGHI", "FGHI"},
		{"ABCDEFGHIJ", "GHIJ"},
		{"ABCDEFGHIJK", "HIJK"},
		{"ABCDEFGHIJKL", "IJKL"},
	} {
		if got := truncateContextEnvVarValue(tc.value); got != tc.want {
			t.Errorf("truncateContextEnvVarValue(%q) = %q, want %q (min(4, floor(%d/2)) characters, "+
				"no mask prefix)", tc.value, got, tc.want, len(tc.value))
		}
	}
}
