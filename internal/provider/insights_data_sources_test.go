// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// --- workflows ---

func TestInsightsWorkflowsDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewInsightsWorkflowsDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	projectSlug, ok := resp.Schema.Attributes["project_slug"]
	if !ok {
		t.Fatal("schema is missing the project_slug attribute")
	}
	if !projectSlug.IsRequired() {
		t.Error("project_slug is not required, but it is the scope of the report")
	}
	// The non-vcs/org/repo slug form is the most likely way to reach a confusing
	// 404, so it has to be documented on the argument itself.
	if !strings.Contains(projectSlug.GetMarkdownDescription(), "circleci/<org-uuid>/<project-uuid>") {
		t.Error("project_slug description does not mention the circleci/<org-uuid>/<project-uuid> slug form")
	}

	// The accuracy caveat is not optional: a configuration that gates on Insights
	// credit figures is gating on approximations.
	if !strings.Contains(resp.Schema.MarkdownDescription, "unsuitable for credit reporting") {
		t.Error("schema description does not carry the Insights accuracy caveat")
	}
}

func testAccInsightsWorkflowsConfig(host, projectSlug, extra string) string {
	return discoveryProviderConfig(host, "cloud") + fmt.Sprintf(`
data "circleci_insights_workflows" "test" {
  project_slug = %[1]q
%[2]s
}
`, projectSlug, extra)
}

func TestAccInsightsWorkflowsDataSource(t *testing.T) {
	_, host := newMockDiscoveryAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccInsightsWorkflowsConfig(host, testDiscoveryProjectSlug, ""),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_insights_workflows.test",
						tfjsonpath.New("workflows"),
						knownvalue.ListSizeExact(2),
					),
					// Decoded from name, not workflow_name: the API renames its
					// internal config-name key before responding.
					statecheck.ExpectKnownValue(
						"data.circleci_insights_workflows.test",
						tfjsonpath.New("workflows").AtSliceIndex(0).AtMapKey("name"),
						knownvalue.StringExact("build-and-test"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_insights_workflows.test",
						tfjsonpath.New("workflows").AtSliceIndex(0).AtMapKey("total_runs"),
						knownvalue.Int64Exact(420),
					),
					// A ratio, not a percentage.
					statecheck.ExpectKnownValue(
						"data.circleci_insights_workflows.test",
						tfjsonpath.New("workflows").AtSliceIndex(0).AtMapKey("success_rate"),
						knownvalue.Float64Exact(0.95),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_insights_workflows.test",
						tfjsonpath.New("workflows").AtSliceIndex(0).AtMapKey("mttr"),
						knownvalue.Int64Exact(1834),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_insights_workflows.test",
						tfjsonpath.New("workflows").AtSliceIndex(0).AtMapKey("total_credits_used"),
						knownvalue.Int64Exact(91234),
					),
					// The duration block is flattened with a prefix rather than left two
					// objects deep, so it is referenced as duration_p95.
					statecheck.ExpectKnownValue(
						"data.circleci_insights_workflows.test",
						tfjsonpath.New("workflows").AtSliceIndex(0).AtMapKey("duration_p95"),
						knownvalue.Int64Exact(701),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_insights_workflows.test",
						tfjsonpath.New("workflows").AtSliceIndex(0).AtMapKey("duration_standard_deviation"),
						knownvalue.Float64Exact(142.5),
					),
					// A workflow that never failed has no recovery time. Null, not zero:
					// "never failed" is not "recovered instantly".
					statecheck.ExpectKnownValue(
						"data.circleci_insights_workflows.test",
						tfjsonpath.New("workflows").AtSliceIndex(1).AtMapKey("mttr"),
						knownvalue.Null(),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_insights_workflows.test",
						tfjsonpath.New("workflows").AtSliceIndex(1).AtMapKey("total_recoveries"),
						knownvalue.Null(),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_insights_workflows.test",
						tfjsonpath.New("workflows").AtSliceIndex(1).AtMapKey("duration_median"),
						knownvalue.Null(),
					),
				},
			},
		},
	})
}

func TestAccInsightsWorkflowsDataSource_standaloneProjectSlug(t *testing.T) {
	api, host := newMockDiscoveryAPI(t)

	// A GitHub App, GitHub Server or GitLab project's slug is
	// circleci/<org-uuid>/<project-uuid>. Its separators must stay literal in the
	// request path.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccInsightsWorkflowsConfig(host, testDiscoveryStandaloneProjectSlug, ""),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_insights_workflows.test",
						tfjsonpath.New("workflows"),
						knownvalue.ListSizeExact(2),
					),
				},
			},
		},
	})

	want := "GET /api/v2/insights/" + testDiscoveryStandaloneProjectSlug + "/workflows"
	if !slices.Contains(api.seenRequests(), want) {
		t.Errorf("requests = %v, want a GET of %s", api.seenRequests(), want)
	}
}

func TestAccInsightsWorkflowsDataSource_reportingWindowAndBranch(t *testing.T) {
	api, host := newMockDiscoveryAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccInsightsWorkflowsConfig(host, testDiscoveryProjectSlug, `
  reporting_window = "last-30-days"
  branch           = "release/2.0"`),
			},
		},
	})

	// The parameters are kebab-case, and a branch containing a slash is escaped in
	// the query even though the slug's separators are not escaped in the path.
	want := "GET /api/v2/insights/gh/acme/api/workflows?branch=release%2F2.0&reporting-window=last-30-days"
	if !slices.Contains(api.seenRequests(), want) {
		t.Errorf("requests = %v, want a GET of %s", api.seenRequests(), want)
	}
}

func TestAccInsightsWorkflowsDataSource_rejectsInvalidReportingWindow(t *testing.T) {
	_, host := newMockDiscoveryAPI(t)

	// The API rejects an unknown window with a bare 400. Catching it in the schema
	// says which values are allowed, at plan time.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{{
			Config: testAccInsightsWorkflowsConfig(host, testDiscoveryProjectSlug, `
  reporting_window = "last-6-months"`),
			ExpectError: regexp.MustCompile(`Attribute reporting_window value must be one of`),
		}},
	})
}

func TestAccInsightsWorkflowsDataSource_rejectsBranchWithAllBranches(t *testing.T) {
	_, host := newMockDiscoveryAPI(t)

	// The API reads all-branches first and silently ignores branch, which would
	// quietly report something other than what was asked for.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{{
			Config: testAccInsightsWorkflowsConfig(host, testDiscoveryProjectSlug, `
  branch       = "main"
  all_branches = true`),
			ExpectError: regexp.MustCompile(`(?s)Invalid Attribute Combination|cannot be specified when`),
		}},
	})
}

func TestAccInsightsWorkflowsDataSource_rejectsMalformedSlug(t *testing.T) {
	_, host := newMockDiscoveryAPI(t)

	// A two-segment slug is rejected by the client before a request is made, so the
	// practitioner is told what shape the slug should be rather than seeing a 404
	// that looks like a missing project.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccInsightsWorkflowsConfig(host, "gh/acme", ""),
			ExpectError: regexp.MustCompile(`(?s)has 2 segments, want 3`),
		}},
	})
}

func TestAccInsightsWorkflowsDataSource_rateLimited(t *testing.T) {
	api, host := newMockDiscoveryAPI(t)

	// A 429 is the quota, not a transient fault. The error must point at reducing
	// the number of Insights reads rather than at retrying.
	api.insightsRateLimited = true

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccInsightsWorkflowsConfig(host, testDiscoveryProjectSlug, ""),
			ExpectError: regexp.MustCompile(`(?s)Rate Limit Exceeded.*rate-limits Insights requests`),
		}},
	})
}

// --- flaky tests ---

func TestInsightsFlakyTestsDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewInsightsFlakyTestsDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	// The route takes nothing but the project: flakes are branch agnostic and the
	// window is fixed, so an extra argument here would be inventing an API.
	for name, attr := range resp.Schema.Attributes {
		if attr.IsOptional() {
			t.Errorf("%s is optional, but the flaky tests route takes no parameters", name)
		}
	}

	// The most likely misreading of this data source, so it must be called out.
	if !strings.Contains(resp.Schema.MarkdownDescription, "not `length(flaky_tests)`") {
		t.Error("schema description does not explain that total_flaky_tests differs from the list length")
	}
}

func testAccInsightsFlakyTestsConfig(host, projectSlug string) string {
	return discoveryProviderConfig(host, "cloud") + fmt.Sprintf(`
data "circleci_insights_flaky_tests" "test" {
  project_slug = %[1]q
}
`, projectSlug)
}

func TestAccInsightsFlakyTestsDataSource(t *testing.T) {
	api, host := newMockDiscoveryAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccInsightsFlakyTestsConfig(host, testDiscoveryProjectSlug),
				ConfigStateChecks: []statecheck.StateCheck{
					// One entry per flake instance, but one unique test — the two figures
					// are deliberately different in the fixture.
					statecheck.ExpectKnownValue(
						"data.circleci_insights_flaky_tests.test",
						tfjsonpath.New("flaky_tests"),
						knownvalue.ListSizeExact(2),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_insights_flaky_tests.test",
						tfjsonpath.New("total_flaky_tests"),
						knownvalue.Int64Exact(1),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_insights_flaky_tests.test",
						tfjsonpath.New("flaky_tests").AtSliceIndex(0).AtMapKey("test_name"),
						knownvalue.StringExact("test_checkout_retries"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_insights_flaky_tests.test",
						tfjsonpath.New("flaky_tests").AtSliceIndex(0).AtMapKey("classname"),
						knownvalue.StringExact("CheckoutTest"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_insights_flaky_tests.test",
						tfjsonpath.New("flaky_tests").AtSliceIndex(0).AtMapKey("times_flaked"),
						knownvalue.Int64Exact(7),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_insights_flaky_tests.test",
						tfjsonpath.New("flaky_tests").AtSliceIndex(0).AtMapKey("pipeline_number"),
						knownvalue.Int64Exact(10027),
					),
					// Millisecond precision must survive into state.
					statecheck.ExpectKnownValue(
						"data.circleci_insights_flaky_tests.test",
						tfjsonpath.New("flaky_tests").AtSliceIndex(0).AtMapKey("workflow_created_at"),
						knownvalue.StringExact("2026-07-26T23:59:55.667Z"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_insights_flaky_tests.test",
						tfjsonpath.New("flaky_tests").AtSliceIndex(0).AtMapKey("time_wasted"),
						knownvalue.Int64Exact(480),
					),
					// An uncosted flake is not a free flake: absent time_wasted is null.
					statecheck.ExpectKnownValue(
						"data.circleci_insights_flaky_tests.test",
						tfjsonpath.New("flaky_tests").AtSliceIndex(1).AtMapKey("time_wasted"),
						knownvalue.Null(),
					),
					// A null file or source is a display field with nothing to display, so
					// it renders as an empty string rather than null.
					statecheck.ExpectKnownValue(
						"data.circleci_insights_flaky_tests.test",
						tfjsonpath.New("flaky_tests").AtSliceIndex(1).AtMapKey("file"),
						knownvalue.StringExact(""),
					),
				},
			},
		},
	})

	// The route is called with no query parameters at all.
	want := "GET /api/v2/insights/gh/acme/api/flaky-tests"
	if !slices.Contains(api.seenRequests(), want) {
		t.Errorf("requests = %v, want a GET of %s with no query", api.seenRequests(), want)
	}
}

func TestAccInsightsFlakyTestsDataSource_empty(t *testing.T) {
	api, host := newMockDiscoveryAPI(t)

	// The state every project should be in. An empty, non-null list keeps for_each
	// and length() working.
	api.flakyTestsEmpty = true

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccInsightsFlakyTestsConfig(host, testDiscoveryProjectSlug),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_insights_flaky_tests.test",
						tfjsonpath.New("flaky_tests"),
						knownvalue.ListSizeExact(0),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_insights_flaky_tests.test",
						tfjsonpath.New("total_flaky_tests"),
						knownvalue.Int64Exact(0),
					),
				},
			},
		},
	})
}

func TestAccInsightsFlakyTestsDataSource_gatesOnCount(t *testing.T) {
	api, host := newMockDiscoveryAPI(t)

	api.flakyTestsEmpty = true

	// The use this data source exists for: a plan-time assertion on flake count.
	config := testAccInsightsFlakyTestsConfig(host, testDiscoveryProjectSlug) + `
check "no_flaky_tests" {
  assert {
    condition     = data.circleci_insights_flaky_tests.test.total_flaky_tests == 0
    error_message = "The project has flaky tests."
  }
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps:                    []resource.TestStep{{Config: config}},
	})
}

// --- summary ---

func TestInsightsSummaryDataSourceSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	resp := &fwdatasource.SchemaResponse{}
	NewInsightsSummaryDataSource().Schema(ctx, fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema validation returned diagnostics: %v", diags)
	}

	orgSlug, ok := resp.Schema.Attributes["organization_slug"]
	if !ok {
		t.Fatal("schema is missing the organization_slug attribute")
	}
	if !orgSlug.IsRequired() {
		t.Error("organization_slug is not required, but it is the scope of the report")
	}
	// An organization slug is two segments where a project slug is three, which is
	// an easy thing to get wrong when both arguments exist in the same provider.
	if !strings.Contains(orgSlug.GetMarkdownDescription(), "two segments") {
		t.Error("organization_slug description does not point out that it is two segments, not three")
	}

	// The API does not report per-project data unless asked, which is surprising
	// enough to need saying.
	if !strings.Contains(resp.Schema.MarkdownDescription, "empty unless `project_names` is set") {
		t.Error("schema description does not explain that projects requires project_names")
	}
}

func testAccInsightsSummaryConfig(host, orgSlug, extra string) string {
	return discoveryProviderConfig(host, "cloud") + fmt.Sprintf(`
data "circleci_insights_summary" "test" {
  organization_slug = %[1]q
%[2]s
}
`, orgSlug, extra)
}

func TestAccInsightsSummaryDataSource(t *testing.T) {
	api, host := newMockDiscoveryAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccInsightsSummaryConfig(host, testDiscoveryOrgSlug, ""),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_insights_summary.test",
						tfjsonpath.New("metrics").AtMapKey("total_runs"),
						knownvalue.Int64Exact(5000),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_insights_summary.test",
						tfjsonpath.New("metrics").AtMapKey("total_credits_used"),
						knownvalue.Int64Exact(2500000),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_insights_summary.test",
						tfjsonpath.New("metrics").AtMapKey("throughput"),
						knownvalue.Float64Exact(55.5),
					),
					// Trends may be negative: a shorter total duration is an improvement.
					statecheck.ExpectKnownValue(
						"data.circleci_insights_summary.test",
						tfjsonpath.New("trends").AtMapKey("total_duration_secs"),
						knownvalue.Float64Exact(-0.05),
					),
					// Empty because no project_names was set, and empty rather than null so
					// for_each keeps working.
					statecheck.ExpectKnownValue(
						"data.circleci_insights_summary.test",
						tfjsonpath.New("projects"),
						knownvalue.ListSizeExact(0),
					),
					// all_projects is how a caller discovers the names to ask for.
					statecheck.ExpectKnownValue(
						"data.circleci_insights_summary.test",
						tfjsonpath.New("all_projects"),
						knownvalue.ListExact([]knownvalue.Check{
							knownvalue.StringExact("api"),
							knownvalue.StringExact("web-ui"),
							knownvalue.StringExact("docs"),
						}),
					),
				},
			},
		},
	})

	want := "GET /api/v2/insights/gh/acme/summary"
	if !slices.Contains(api.seenRequests(), want) {
		t.Errorf("requests = %v, want a GET of %s with no query", api.seenRequests(), want)
	}
}

func TestAccInsightsSummaryDataSource_withProjectNames(t *testing.T) {
	api, host := newMockDiscoveryAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccInsightsSummaryConfig(host, testDiscoveryOrgSlug, `
  reporting_window = "last-7-days"
  project_names    = ["api", "web-ui"]`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_insights_summary.test",
						tfjsonpath.New("projects"),
						knownvalue.ListSizeExact(2),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_insights_summary.test",
						tfjsonpath.New("projects").AtSliceIndex(0).AtMapKey("project_name"),
						knownvalue.StringExact("api"),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_insights_summary.test",
						tfjsonpath.New("projects").AtSliceIndex(0).AtMapKey("metrics").AtMapKey("success_rate"),
						knownvalue.Float64Exact(0.94),
					),
					// The API does not compute throughput per project, so it must be null
					// rather than a claim of zero runs per day.
					statecheck.ExpectKnownValue(
						"data.circleci_insights_summary.test",
						tfjsonpath.New("projects").AtSliceIndex(0).AtMapKey("metrics").AtMapKey("throughput"),
						knownvalue.Null(),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_insights_summary.test",
						tfjsonpath.New("projects").AtSliceIndex(1).AtMapKey("trends").AtMapKey("success_rate"),
						knownvalue.Float64Exact(-0.02),
					),
				},
			},
		},
	})

	// project-names repeats rather than taking a comma-separated list.
	want := "GET /api/v2/insights/gh/acme/summary?project-names=api&project-names=web-ui&reporting-window=last-7-days"
	if !slices.Contains(api.seenRequests(), want) {
		t.Errorf("requests = %v, want a GET of %s", api.seenRequests(), want)
	}
}

func TestAccInsightsSummaryDataSource_rejectsProjectSlug(t *testing.T) {
	_, host := newMockDiscoveryAPI(t)

	// A three-segment slug is a project slug, not an organization slug. Rejecting it
	// before the request is what turns a 404 into an explanation.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: discoveryProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccInsightsSummaryConfig(host, testDiscoveryProjectSlug, ""),
			ExpectError: regexp.MustCompile(`(?s)has 3 segments, want 2`),
		}},
	})
}
