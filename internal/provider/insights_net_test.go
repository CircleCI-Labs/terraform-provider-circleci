// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// This file is the real-API counterpart to insights_data_sources_test.go,
// which exercises circleci_insights_workflows, circleci_insights_flaky_tests
// and circleci_insights_summary entirely against newMockDiscoveryAPI. Before
// these tests existed, "what happens for a project with no runs at all" —
// one of this family's specific open questions — was answered only by reading
// the handler code, never by asking the API.
//
// gitlab-test's writable project (the one CIRCLECI_TEST_GL_CLOUD_PROJECT_ID
// names) is the one fixture confirmed [NET, on 2026-08-21] to have zero
// pipeline runs of any kind — every other fixture project has at least one.
// That is what every test below needs, which is why they are gated to
// "gitlab" specifically rather than to the active integration generically:
// this is a property of that one project today, not of the GitLab
// integration, and would need re-pointing at a different empty project if
// gitlab-test ever accumulates a run.

// testEmptyGitLabProjectSlug resolves the project slug of the one fixture
// confirmed to have no runs. Standalone organizations (which every "gitlab"
// fixture is) have no VCS-side org/repo name to build a slug from, so the
// slug is assembled from the two UUIDs directly rather than read from a
// PROJECT_SLUG variable — see insightsProjectSlugDescription.
func testEmptyGitLabProjectSlug(t *testing.T) string {
	t.Helper()
	testRequireVCSType(t, "gitlab")

	return fmt.Sprintf("circleci/%s/%s", testOrgID(t), testProjectID(t))
}

func TestAccInsightsWorkflowsDataSourceNet_NoRuns(t *testing.T) {
	slug := testEmptyGitLabProjectSlug(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "circleci_insights_workflows" "net_test" {
  project_slug = %[1]q
}
`, slug),
			ConfigStateChecks: []statecheck.StateCheck{
				// The whole point: 200 with an empty list, never an error, for a
				// project that has never run a pipeline.
				statecheck.ExpectKnownValue(
					"data.circleci_insights_workflows.net_test",
					tfjsonpath.New("workflows"),
					knownvalue.ListSizeExact(0),
				),
			},
		}},
	})
}

func TestAccInsightsFlakyTestsDataSourceNet_NoRuns(t *testing.T) {
	slug := testEmptyGitLabProjectSlug(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "circleci_insights_flaky_tests" "net_test" {
  project_slug = %[1]q
}
`, slug),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"data.circleci_insights_flaky_tests.net_test",
					tfjsonpath.New("flaky_tests"),
					knownvalue.ListSizeExact(0),
				),
				statecheck.ExpectKnownValue(
					"data.circleci_insights_flaky_tests.net_test",
					tfjsonpath.New("total_flaky_tests"),
					knownvalue.Int64Exact(0),
				),
			},
		}},
	})
}

// TestAccInsightsSummaryDataSourceNet_NamedProjectWithNoRuns pins a real
// asymmetry the schema doc hedges with the word "recognised" but does not spell
// out: [NET, reproduced against the live API on 2026-08-21] asking the summary
// route for per-project metrics on a project that exists, is spelled correctly,
// and is genuinely visible to the token (it appears in all_projects) but has
// no runs — omits that project from org_project_data entirely, rather than
// including it with zero-valued metrics. A caller cannot distinguish "you
// misspelled the name" from "that project has no data yet" by looking at
// org_project_data; all_projects is the only source of truth for spelling.
func TestAccInsightsSummaryDataSourceNet_NamedProjectWithNoRuns(t *testing.T) {
	testRequireVCSType(t, "gitlab")

	orgSlug := testOrgSlug(t)
	projectID := testProjectID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "circleci_insights_summary" "net_test" {
  organization_slug = %[1]q
  project_names     = [%[2]q]
}
`, orgSlug, projectID),
			ConfigStateChecks: []statecheck.StateCheck{
				// Not an error, and not a zero-valued entry either: absent.
				statecheck.ExpectKnownValue(
					"data.circleci_insights_summary.net_test",
					tfjsonpath.New("projects"),
					knownvalue.ListSizeExact(0),
				),
			},
		}},
	})
}

// TestAccInsightsSummaryDataSourceNet_AllProjects confirms the organization-wide
// half works and all_projects is non-empty (gitlab-test has at least the one
// project this whole file is built around, plus whichever others happened to
// run in the window) — the discovery mechanism the schema doc points a caller
// at instead of guessing names.
func TestAccInsightsSummaryDataSourceNet_AllProjects(t *testing.T) {
	testRequireVCSType(t, "gitlab")

	orgSlug := testOrgSlug(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "circleci_insights_summary" "net_test" {
  organization_slug = %[1]q
}
`, orgSlug),
			Check: resource.TestCheckResourceAttrSet(
				"data.circleci_insights_summary.net_test", "all_projects.0",
			),
		}},
	})
}
