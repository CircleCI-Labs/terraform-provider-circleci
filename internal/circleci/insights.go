// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Insights routes.
//
// Insights is v2 on every deployment. Unlike most of v2 these routes are keyed by
// slug rather than by UUID, and the slug sits in the middle of the path with its
// separators intact, which is why the routes below are assembled rather than
// formatted (see insightsSlugPath).
const (
	insightsWorkflowsRoute  = "/insights/%s/workflows"
	insightsFlakyTestsRoute = "/insights/%s/flaky-tests"
	insightsSummaryRoute    = "/insights/%s/summary"
)

// Reporting windows accepted by the reporting-window query parameter. The API
// defaults to InsightsWindow90Days when the parameter is omitted, and rejects any
// other value with a 400.
const (
	// InsightsWindow24Hours aggregates over the last 24 hours.
	InsightsWindow24Hours = "last-24-hours"
	// InsightsWindow7Days aggregates over the last 7 days.
	InsightsWindow7Days = "last-7-days"
	// InsightsWindow30Days aggregates over the last 30 days.
	InsightsWindow30Days = "last-30-days"
	// InsightsWindow60Days aggregates over the last 60 days.
	InsightsWindow60Days = "last-60-days"
	// InsightsWindow90Days aggregates over the last 90 days, and is the API
	// default. It is also the longest window Insights retains.
	InsightsWindow90Days = "last-90-days"
)

// InsightsReportingWindows lists every accepted reporting window, for schema
// validation.
var InsightsReportingWindows = []string{
	InsightsWindow24Hours,
	InsightsWindow7Days,
	InsightsWindow30Days,
	InsightsWindow60Days,
	InsightsWindow90Days,
}

// insightsSlugPath percent-escapes each segment of a slug for use inside a
// request path, leaving the separators literal, and checks that it has the
// expected number of segments.
//
// It exists because RouteParams escapes each value as a single path segment,
// which turns a slug's separators into %2F. The Insights routes document the
// separators as optionally escaped, but an escaped separator does not survive
// every intermediate proxy, and it does not match the route on CircleCI Server —
// the same problem checkout_key.go solves the same way.
//
// The segment count is checked rather than assumed because the project slug is
// not always vcs/org/repo. A GitLab, GitHub App or GitHub Server project has no
// VCS-side org and repo name to build a slug from, so its slug is
// circleci/<org-uuid>/<project-uuid> instead. Both forms are three segments, so
// counting works for both, but nothing here may assume the second and third
// segments are human-readable names.
func insightsSlugPath(slug string, wantSegments int, shape string) (string, error) {
	if slug == "" {
		return "", fmt.Errorf("circleci: slug is empty, want the form %s", shape)
	}

	segments := strings.Split(slug, "/")
	if len(segments) != wantSegments {
		return "", fmt.Errorf(
			"circleci: slug %q has %d segments, want %d of the form %s",
			slug, len(segments), wantSegments, shape,
		)
	}

	for i, segment := range segments {
		if segment == "" {
			return "", fmt.Errorf("circleci: slug %q has an empty segment, want the form %s", slug, shape)
		}
		segments[i] = url.PathEscape(segment)
	}

	return strings.Join(segments, "/"), nil
}

// insightsRoute renders an Insights route by substituting an already-escaped slug
// for the template's single %s.
//
// The substitution is a string split rather than fmt.Sprintf because the escaped
// slug may contain percent escapes, and a %2F inside a format argument is fine but
// the result must never be formatted again. Callers therefore pass no RouteParams.
func insightsRoute(template, escapedSlug string) (string, error) {
	before, after, found := strings.Cut(template, "%s")
	if !found {
		return "", errors.New("circleci: insights route template has no slug placeholder")
	}

	return before + escapedSlug + after, nil
}

// insightsProjectRoute renders an Insights project route for a project slug.
func insightsProjectRoute(template, projectSlug string) (string, error) {
	slug, err := insightsSlugPath(
		projectSlug, 3,
		"vcs-slug/org-name/repo-name (or circleci/<org-uuid>/<project-uuid>)",
	)
	if err != nil {
		return "", err
	}

	return insightsRoute(template, slug)
}

// InsightsDurationMetrics holds the duration statistics for a group of runs. Every
// value is in seconds, and every value is nullable: a window in which nothing ran
// has no duration to report.
type InsightsDurationMetrics struct {
	Min               *int64   `json:"min"`
	Mean              *int64   `json:"mean"`
	Median            *int64   `json:"median"`
	P95               *int64   `json:"p95"`
	Max               *int64   `json:"max"`
	StandardDeviation *float64 `json:"standard_deviation"`
}

// InsightsWorkflowMetrics holds the aggregated metrics for one workflow.
//
// MTTR, TotalCreditsUsed and TotalRecoveries are pointers because the API
// declares them nullable, and zero is a meaningful value for each: a workflow
// that never failed has no mean time to recovery, which is not the same as a mean
// time to recovery of zero seconds.
type InsightsWorkflowMetrics struct {
	TotalRuns        int64                   `json:"total_runs"`
	SuccessfulRuns   int64                   `json:"successful_runs"`
	FailedRuns       int64                   `json:"failed_runs"`
	SuccessRate      float64                 `json:"success_rate"`
	Throughput       float64                 `json:"throughput"`
	MTTR             *int64                  `json:"mttr"`
	TotalCreditsUsed *int64                  `json:"total_credits_used"`
	TotalRecoveries  *int64                  `json:"total_recoveries"`
	DurationMetrics  InsightsDurationMetrics `json:"duration_metrics"`
}

// InsightsWorkflow is one workflow's summary over the reporting window.
//
// Name is sent as name rather than workflow_name: the API renames its internal
// config-name key to name before responding.
//
// WindowStart and WindowEnd are kept as the strings the API sent (RFC 3339
// timestamps) so they round-trip into Terraform state exactly as received.
type InsightsWorkflow struct {
	Name        string                  `json:"name"`
	ProjectID   string                  `json:"project_id"`
	WindowStart string                  `json:"window_start"`
	WindowEnd   string                  `json:"window_end"`
	Metrics     InsightsWorkflowMetrics `json:"metrics"`
}

// InsightsWorkflowsOptions narrows a workflow metrics listing.
type InsightsWorkflowsOptions struct {
	// ReportingWindow is one of the InsightsWindow constants. Empty takes the API
	// default of last-90-days.
	ReportingWindow string
	// Branch scopes the metrics to one branch. Empty takes the project's default
	// branch, which is the API's behaviour, not an absence of filtering.
	Branch string
	// AllBranches combines every branch instead of scoping to one. It is mutually
	// exclusive with Branch; the API reads all-branches first and ignores branch
	// when it is set.
	AllBranches bool
}

// InsightsFlakyTest is one recorded flake.
//
// A flake is a test that both passed and failed at the same commit. Flakes are
// branch agnostic, so there is no branch here and no way to scope the listing to
// one. A test stops being reported once two weeks pass without another flake.
//
// Source is a plain string although the API declares it nullable: a null decodes
// to "", which is the right rendering for a field that only ever gets displayed.
// TimeWasted is a pointer because it is genuinely optional — it is absent from the
// response rather than null when the API has not computed it.
type InsightsFlakyTest struct {
	// TestName is the name of the flaking test.
	TestName string `json:"test_name"`
	// Classname is the test's class or suite, as reported by the test runner.
	Classname string `json:"classname"`
	// File is the source file the test lives in, when the test runner reported it.
	File string `json:"file"`
	// Source identifies which test result collection produced the record.
	Source string `json:"source"`
	// TimesFlaked is how many times this test has flaked.
	TimesFlaked int64 `json:"times_flaked"`
	// JobName is the job the test ran in.
	JobName string `json:"job_name"`
	// JobNumber is the number of the job the flake was last seen in.
	JobNumber int64 `json:"job_number"`
	// PipelineNumber is the number of the pipeline the flake was last seen in.
	PipelineNumber int64 `json:"pipeline_number"`
	// WorkflowID is the id of the workflow the flake was last seen in.
	WorkflowID string `json:"workflow_id"`
	// WorkflowName is the name of that workflow.
	WorkflowName string `json:"workflow_name"`
	// WorkflowCreatedAt is when that workflow started, as the RFC 3339 string the
	// API sent.
	WorkflowCreatedAt string `json:"workflow_created_at"`
	// TimeWasted is the seconds spent on runs that flaked, when the API reports it.
	TimeWasted *int64 `json:"time_wasted"`
}

// InsightsFlakyTests is the flaky tests response.
//
// TotalFlakyTests counts unique tests, whereas FlakyTests holds one entry per
// flake instance. The two differ whenever a test has flaked more than once, so
// len(FlakyTests) is not a substitute for TotalFlakyTests.
type InsightsFlakyTests struct {
	FlakyTests      []InsightsFlakyTest `json:"flaky_tests"`
	TotalFlakyTests int64               `json:"total_flaky_tests"`
}

// InsightsSummaryMetrics holds the aggregated metrics for an organization or one
// of its projects, across all branches.
//
// Throughput is a pointer because it is reported for the organization but not for
// individual projects, so a plain float64 would silently report zero runs per day
// for every project.
type InsightsSummaryMetrics struct {
	TotalRuns         int64    `json:"total_runs"`
	TotalDurationSecs int64    `json:"total_duration_secs"`
	TotalCreditsUsed  int64    `json:"total_credits_used"`
	SuccessRate       float64  `json:"success_rate"`
	Throughput        *float64 `json:"throughput"`
}

// InsightsSummaryTrends holds the change in each metric against the preceding
// window of the same length. A value is a ratio, so 0.1 is a ten percent increase
// and -0.1 a ten percent decrease.
type InsightsSummaryTrends struct {
	TotalRuns         float64  `json:"total_runs"`
	TotalDurationSecs float64  `json:"total_duration_secs"`
	TotalCreditsUsed  float64  `json:"total_credits_used"`
	SuccessRate       float64  `json:"success_rate"`
	Throughput        *float64 `json:"throughput"`
}

// InsightsOrganizationData is the organization-wide half of the summary.
type InsightsOrganizationData struct {
	Metrics InsightsSummaryMetrics `json:"metrics"`
	Trends  InsightsSummaryTrends  `json:"trends"`
}

// InsightsProjectSummary is one project's half of the summary. The project is
// identified by name only: the API replaces the project id with its name
// before responding.
type InsightsProjectSummary struct {
	ProjectName string                 `json:"project_name"`
	Metrics     InsightsSummaryMetrics `json:"metrics"`
	Trends      InsightsSummaryTrends  `json:"trends"`
}

// InsightsSummary is the organization summary response.
//
// OrganizationProjectData is empty unless project names were requested: the route
// only computes per-project metrics for the names passed in project-names. That is
// why AllProjects exists — it lists every project name in the organization, so a
// caller can discover the names to ask for.
type InsightsSummary struct {
	OrganizationData        InsightsOrganizationData `json:"org_data"`
	OrganizationProjectData []InsightsProjectSummary `json:"org_project_data"`
	AllProjects             []string                 `json:"all_projects"`
}

// InsightsService reads CircleCI Insights.
//
// Read-only in the strictest sense: Insights is a reporting surface with no
// writable state at all, so there is no resource counterpart to any of this.
//
// Two caveats apply to everything here, and both are documented on the data
// sources as well. Metrics are recomputed daily, so they lag the last 24 hours of
// activity. And credit figures do not share a source of truth with billing:
// CircleCI documents Insights as unsuitable for credit reporting, and the Plan
// Overview page in the web UI as the only accurate source.
type InsightsService struct {
	client *Client
}

// Insights returns the Insights service for this client.
func (c *Client) Insights() *InsightsService {
	return &InsightsService{client: c}
}

// ListWorkflows returns summary metrics for every workflow in a project,
// following pagination to the last page.
//
// projectSlug takes either the vcs-slug/org-name/repo-name form or the
// circleci/<org-uuid>/<project-uuid> form used by GitLab, GitHub App and GitHub
// Server projects.
func (s *InsightsService) ListWorkflows(
	ctx context.Context, projectSlug string, opts InsightsWorkflowsOptions,
) ([]InsightsWorkflow, error) {
	route, err := insightsProjectRoute(insightsWorkflowsRoute, projectSlug)
	if err != nil {
		return nil, err
	}

	// The branch filters are kebab-case query parameters, matching the API's own
	// parameter names rather than the snake_case of its response bodies.
	branchOpts := []RequestOption{OptionalQuery("branch", opts.Branch)}
	if opts.AllBranches {
		branchOpts = []RequestOption{Query("all-branches", "true")}
	}

	return DrainV2(ctx, func(ctx context.Context, pageToken string) (PaginatedResponse[InsightsWorkflow], error) {
		var page PaginatedResponse[InsightsWorkflow]

		reqOpts := append([]RequestOption{
			OptionalQuery("reporting-window", opts.ReportingWindow),
			PageToken(pageToken),
		}, branchOpts...)

		return page, s.client.GetV2(ctx, route, &page, reqOpts...)
	})
}

// GetFlakyTests returns the recorded flakes for a project.
//
// The route takes no parameters beyond the project: flakes are branch agnostic and
// the window is fixed, so there is nothing to narrow. It is not paginated either.
func (s *InsightsService) GetFlakyTests(ctx context.Context, projectSlug string) (*InsightsFlakyTests, error) {
	route, err := insightsProjectRoute(insightsFlakyTestsRoute, projectSlug)
	if err != nil {
		return nil, err
	}

	var flaky InsightsFlakyTests
	if err := s.client.GetV2(ctx, route, &flaky); err != nil {
		return nil, err
	}

	return &flaky, nil
}

// GetSummary returns organization-wide summary metrics, plus per-project metrics
// for each name in projectNames.
//
// orgSlug takes the vcs-slug/org-name form, or circleci/<org-uuid> for a
// standalone organization — two segments either way, unlike a project slug.
//
// Passing no project names is valid and yields organization totals plus the list
// of every project name in the organization, which is how a caller discovers the
// names to ask for on a second call.
func (s *InsightsService) GetSummary(
	ctx context.Context, orgSlug, reportingWindow string, projectNames []string,
) (*InsightsSummary, error) {
	slug, err := insightsSlugPath(orgSlug, 2, "vcs-slug/org-name (or circleci/<org-uuid>)")
	if err != nil {
		return nil, err
	}

	route, err := insightsRoute(insightsSummaryRoute, slug)
	if err != nil {
		return nil, err
	}

	// project-names repeats rather than taking a comma-separated list, so each
	// name is its own query parameter.
	opts := []RequestOption{OptionalQuery("reporting-window", reportingWindow)}
	for _, name := range projectNames {
		opts = append(opts, OptionalQuery("project-names", name))
	}

	var summary InsightsSummary
	if err := s.client.GetV2(ctx, route, &summary, opts...); err != nil {
		return nil, err
	}

	return &summary, nil
}
