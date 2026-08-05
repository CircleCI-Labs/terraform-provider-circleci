// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

const (
	// testInsightsProjectSlug is the VCS-backed project slug form.
	testInsightsProjectSlug = "gh/acme/api"
	// testInsightsStandaloneSlug is the form a GitLab, GitHub App or GitHub Server
	// project takes: there are no VCS-side org and repo names to build a slug from,
	// so both segments are UUIDs.
	testInsightsStandaloneSlug = "circleci/11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222"
	testInsightsOrgSlug        = "gh/acme"
)

// newInsightsServer serves the Insights routes from handler and records the raw
// request line of every call, so tests can assert on both the literal path
// separators inside a slug and the query parameters.
func newInsightsServer(t *testing.T, handler http.HandlerFunc) (*circleci.Client, *[]string) {
	t.Helper()

	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.RequestURI)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	return circleci.New(circleci.Config{Host: srv.URL, Token: "tok"}), &seen
}

// testInsightsWorkflowsBody is the shape production sends. The API renames its
// internal keys before responding, which is why the workflow name arrives as
// name (not workflow_name), the duration block as duration_metrics (not
// duration_stats) and its 95th percentile as p95 (not ninety_fifth).
const testInsightsWorkflowsBody = `{
  "items": [
    {
      "name": "build-and-test",
      "project_id": "22222222-2222-2222-2222-222222222222",
      "window_start": "2026-04-28T00:00:00Z",
      "window_end": "2026-07-27T00:00:00Z",
      "metrics": {
        "total_runs": 420,
        "successful_runs": 399,
        "failed_runs": 21,
        "success_rate": 0.95,
        "throughput": 4.6153846153846154,
        "mttr": 1834,
        "total_credits_used": 91234,
        "total_recoveries": 19,
        "duration_metrics": {
          "min": 121,
          "mean": 384,
          "median": 355,
          "p95": 701,
          "max": 1288,
          "standard_deviation": 142.5
        }
      }
    },
    {
      "name": "never-failed",
      "project_id": "22222222-2222-2222-2222-222222222222",
      "window_start": "2026-04-28T00:00:00Z",
      "window_end": "2026-07-27T00:00:00Z",
      "metrics": {
        "total_runs": 3,
        "successful_runs": 3,
        "failed_runs": 0,
        "success_rate": 1.0,
        "throughput": 0.03,
        "mttr": null,
        "total_credits_used": null,
        "total_recoveries": null,
        "duration_metrics": {
          "min": null,
          "mean": null,
          "median": null,
          "p95": null,
          "max": null,
          "standard_deviation": null
        }
      }
    }
  ],
  "next_page_token": null
}`

func TestInsightsListWorkflows(t *testing.T) {
	t.Parallel()

	client, seen := newInsightsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testInsightsWorkflowsBody))
	})

	workflows, err := client.Insights().ListWorkflows(
		context.Background(), testInsightsProjectSlug, circleci.InsightsWorkflowsOptions{},
	)
	if err != nil {
		t.Fatalf("ListWorkflows returned error: %v", err)
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}
	// The slug's separators must stay literal: an escaped %2F does not survive
	// every intermediate proxy and does not match the route on CircleCI Server.
	// With no options set, nothing is sent as a query parameter either, so the API
	// applies its own defaults of last-90-days and the project's default branch.
	if got := (*seen)[0]; got != "GET /api/v2/insights/gh/acme/api/workflows" {
		t.Errorf("request = %q, want %q", got, "GET /api/v2/insights/gh/acme/api/workflows")
	}

	if len(workflows) != 2 {
		t.Fatalf("workflow count = %d, want 2", len(workflows))
	}

	got := workflows[0]
	if got.Name != "build-and-test" {
		t.Errorf("name = %q, want %q (decoded from name, not workflow_name)", got.Name, "build-and-test")
	}
	if got.ProjectID != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("project id = %q, want the project UUID", got.ProjectID)
	}
	if got.WindowStart != "2026-04-28T00:00:00Z" {
		t.Errorf("window start = %q, want it kept as the string sent", got.WindowStart)
	}
	if got.Metrics.TotalRuns != 420 || got.Metrics.FailedRuns != 21 {
		t.Errorf("run counts = %d total / %d failed, want 420 / 21", got.Metrics.TotalRuns, got.Metrics.FailedRuns)
	}
	if got.Metrics.SuccessRate != 0.95 {
		t.Errorf("success rate = %v, want 0.95 (a ratio, not a percentage)", got.Metrics.SuccessRate)
	}
	if got.Metrics.MTTR == nil || *got.Metrics.MTTR != 1834 {
		t.Errorf("mttr = %v, want 1834", got.Metrics.MTTR)
	}
	if got.Metrics.TotalCreditsUsed == nil || *got.Metrics.TotalCreditsUsed != 91234 {
		t.Errorf("total credits used = %v, want 91234", got.Metrics.TotalCreditsUsed)
	}
	if got.Metrics.DurationMetrics.P95 == nil || *got.Metrics.DurationMetrics.P95 != 701 {
		t.Errorf("p95 = %v, want 701 (decoded from p95)", got.Metrics.DurationMetrics.P95)
	}
	if sd := got.Metrics.DurationMetrics.StandardDeviation; sd == nil || *sd != 142.5 {
		t.Errorf("standard deviation = %v, want 142.5", sd)
	}

	// A workflow that never failed has no mean time to recovery. That must stay
	// distinguishable from a recovery time of zero seconds.
	quiet := workflows[1]
	if quiet.Metrics.MTTR != nil {
		t.Errorf("mttr for a never-failed workflow = %v, want nil", quiet.Metrics.MTTR)
	}
	if quiet.Metrics.TotalRecoveries != nil {
		t.Errorf("total recoveries = %v, want nil", quiet.Metrics.TotalRecoveries)
	}
	if quiet.Metrics.DurationMetrics.Median != nil {
		t.Errorf("median duration = %v, want nil", quiet.Metrics.DurationMetrics.Median)
	}
}

func TestInsightsListWorkflowsQueryParameters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		opts      circleci.InsightsWorkflowsOptions
		wantQuery string
	}{
		{
			name:      "reporting window only",
			opts:      circleci.InsightsWorkflowsOptions{ReportingWindow: circleci.InsightsWindow30Days},
			wantQuery: "reporting-window=last-30-days",
		},
		{
			// The parameters are kebab-case, unlike the snake_case of the response
			// bodies.
			name: "branch",
			opts: circleci.InsightsWorkflowsOptions{
				ReportingWindow: circleci.InsightsWindow7Days,
				Branch:          "release/2.0",
			},
			wantQuery: "branch=release%2F2.0&reporting-window=last-7-days",
		},
		{
			// all-branches replaces branch rather than joining it: the API reads
			// all-branches first and ignores branch when it is set, so sending both
			// would quietly discard the branch the practitioner asked for.
			name: "all branches wins over branch",
			opts: circleci.InsightsWorkflowsOptions{
				Branch:      "main",
				AllBranches: true,
			},
			wantQuery: "all-branches=true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, seen := newInsightsServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"items":[],"next_page_token":null}`))
			})

			if _, err := client.Insights().ListWorkflows(context.Background(), testInsightsProjectSlug, tt.opts); err != nil {
				t.Fatalf("ListWorkflows returned error: %v", err)
			}

			want := "GET /api/v2/insights/gh/acme/api/workflows?" + tt.wantQuery
			if got := (*seen)[0]; got != want {
				t.Errorf("request = %q, want %q", got, want)
			}
		})
	}
}

func TestInsightsListWorkflowsStandaloneSlug(t *testing.T) {
	t.Parallel()

	// A GitLab, GitHub App or GitHub Server project's slug is
	// circleci/<org-uuid>/<project-uuid>. Nothing may assume vcs/org/repo.
	client, seen := newInsightsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testInsightsWorkflowsBody))
	})

	if _, err := client.Insights().ListWorkflows(
		context.Background(), testInsightsStandaloneSlug, circleci.InsightsWorkflowsOptions{},
	); err != nil {
		t.Fatalf("ListWorkflows returned error: %v", err)
	}

	want := "GET /api/v2/insights/" + testInsightsStandaloneSlug + "/workflows"
	if got := (*seen)[0]; got != want {
		t.Errorf("request = %q, want %q", got, want)
	}
}

func TestInsightsListWorkflowsDrainsPages(t *testing.T) {
	t.Parallel()

	pages := []string{
		`{"items":[{"name":"one","metrics":{}}],"next_page_token":"tok-2"}`,
		`{"items":[{"name":"two","metrics":{}}],"next_page_token":null}`,
	}

	var calls int
	client, seen := newInsightsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := pages[min(calls, len(pages)-1)]
		calls++
		_, _ = w.Write([]byte(body))
	})

	workflows, err := client.Insights().ListWorkflows(
		context.Background(), testInsightsProjectSlug, circleci.InsightsWorkflowsOptions{},
	)
	if err != nil {
		t.Fatalf("ListWorkflows returned error: %v", err)
	}

	if len(workflows) != 2 {
		t.Fatalf("workflow count = %d, want 2 (both pages drained)", len(workflows))
	}
	if len(*seen) != 2 {
		t.Fatalf("request count = %d, want 2", len(*seen))
	}
	// The first request must not send page-token at all; the second must send the
	// token the first page returned.
	if got := (*seen)[0]; got != "GET /api/v2/insights/gh/acme/api/workflows" {
		t.Errorf("first request = %q, want no page-token", got)
	}
	if got := (*seen)[1]; got != "GET /api/v2/insights/gh/acme/api/workflows?page-token=tok-2" {
		t.Errorf("second request = %q, want page-token=tok-2", got)
	}
}

func TestInsightsRejectsMalformedSlugs(t *testing.T) {
	t.Parallel()

	// A malformed slug must be rejected before a request is made, so the caller
	// gets an explanation rather than an HTTP 404 that looks like a missing project.
	client, seen := newInsightsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "project slug with two segments",
			call: func() error {
				_, err := client.Insights().ListWorkflows(
					context.Background(), "gh/acme", circleci.InsightsWorkflowsOptions{},
				)

				return err
			},
		},
		{
			name: "empty project slug",
			call: func() error {
				_, err := client.Insights().GetFlakyTests(context.Background(), "")

				return err
			},
		},
		{
			name: "project slug with an empty segment",
			call: func() error {
				_, err := client.Insights().GetFlakyTests(context.Background(), "gh//api")

				return err
			},
		},
		{
			name: "org slug with three segments",
			call: func() error {
				_, err := client.Insights().GetSummary(context.Background(), "gh/acme/api", "", nil)

				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			if err == nil {
				t.Fatal("call returned no error for a malformed slug, want one")
			}
			if !strings.Contains(err.Error(), "want") {
				t.Errorf("error = %q, want it to say what shape the slug should be", err)
			}
		})
	}

	if len(*seen) != 0 {
		t.Errorf("requests made = %v, want none for malformed slugs", *seen)
	}
}

// testInsightsFlakyTestsBody is the shape production sends. The API drops
// org-id and project-id from the response and then converts every remaining
// kebab-case key to snake_case, which is what makes these flaky_tests /
// total_flaky_tests / workflow_created_at rather than the hyphenated originals.
const testInsightsFlakyTestsBody = `{
  "flaky_tests": [
    {
      "test_name": "test_checkout_retries",
      "classname": "CheckoutTest",
      "file": "spec/checkout_spec.rb",
      "source": "junit",
      "times_flaked": 7,
      "job_name": "rspec",
      "job_number": 1234,
      "pipeline_number": 10027,
      "workflow_id": "966b80cf-909b-4e79-b228-9d59dfd0c3ff",
      "workflow_name": "build-and-test",
      "workflow_created_at": "2026-07-26T23:59:55.667Z",
      "time_wasted": 480
    },
    {
      "test_name": "test_upload_timeout",
      "classname": "UploadTest",
      "file": null,
      "source": null,
      "times_flaked": 1,
      "job_name": "integration",
      "job_number": 1240,
      "pipeline_number": 10031,
      "workflow_id": "0f2c9e51-7b0f-45c3-9d4a-6dd6c0e1a2b3",
      "workflow_name": "build-and-test",
      "workflow_created_at": "2026-07-27T04:12:00.000Z"
    }
  ],
  "total_flaky_tests": 2
}`

func TestInsightsGetFlakyTests(t *testing.T) {
	t.Parallel()

	client, seen := newInsightsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testInsightsFlakyTestsBody))
	})

	flaky, err := client.Insights().GetFlakyTests(context.Background(), testInsightsProjectSlug)
	if err != nil {
		t.Fatalf("GetFlakyTests returned error: %v", err)
	}

	// The route takes no parameters at all: flakes are branch agnostic and the
	// window is fixed, so there is nothing to narrow and nothing to paginate.
	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}
	if got := (*seen)[0]; got != "GET /api/v2/insights/gh/acme/api/flaky-tests" {
		t.Errorf("request = %q, want %q", got, "GET /api/v2/insights/gh/acme/api/flaky-tests")
	}

	if flaky.TotalFlakyTests != 2 {
		t.Errorf("total flaky tests = %d, want 2", flaky.TotalFlakyTests)
	}
	if len(flaky.FlakyTests) != 2 {
		t.Fatalf("flake count = %d, want 2", len(flaky.FlakyTests))
	}

	got := flaky.FlakyTests[0]
	if got.TestName != "test_checkout_retries" {
		t.Errorf("test name = %q, want %q", got.TestName, "test_checkout_retries")
	}
	if got.Classname != "CheckoutTest" {
		t.Errorf("classname = %q, want %q (classname, one word)", got.Classname, "CheckoutTest")
	}
	if got.TimesFlaked != 7 {
		t.Errorf("times flaked = %d, want 7", got.TimesFlaked)
	}
	if got.JobNumber != 1234 || got.PipelineNumber != 10027 {
		t.Errorf("job/pipeline numbers = %d/%d, want 1234/10027", got.JobNumber, got.PipelineNumber)
	}
	if got.WorkflowID != "966b80cf-909b-4e79-b228-9d59dfd0c3ff" {
		t.Errorf("workflow id = %q, want the workflow UUID", got.WorkflowID)
	}
	// Millisecond precision must survive: the timestamp is kept as the string sent.
	if got.WorkflowCreatedAt != "2026-07-26T23:59:55.667Z" {
		t.Errorf("workflow created at = %q, want the string sent with milliseconds intact", got.WorkflowCreatedAt)
	}
	if got.TimeWasted == nil || *got.TimeWasted != 480 {
		t.Errorf("time wasted = %v, want 480", got.TimeWasted)
	}

	// The second flake omits time_wasted and nulls file and source, which is the
	// documented case for a runner that reported neither.
	second := flaky.FlakyTests[1]
	if second.TimeWasted != nil {
		t.Errorf("time wasted = %v, want nil when the key is absent", second.TimeWasted)
	}
	if second.File != "" || second.Source != "" {
		t.Errorf("file/source = %q/%q, want empty strings for nulls", second.File, second.Source)
	}
}

func TestInsightsGetFlakyTestsEmpty(t *testing.T) {
	t.Parallel()

	client, _ := newInsightsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flaky_tests":[],"total_flaky_tests":0}`))
	})

	flaky, err := client.Insights().GetFlakyTests(context.Background(), testInsightsProjectSlug)
	if err != nil {
		t.Fatalf("GetFlakyTests returned error: %v", err)
	}
	if len(flaky.FlakyTests) != 0 || flaky.TotalFlakyTests != 0 {
		t.Errorf("flakes = %d / total = %d, want 0 / 0", len(flaky.FlakyTests), flaky.TotalFlakyTests)
	}
}

// testInsightsSummaryBody is the shape production sends. Note that
// org_project_data carries project_name rather than project_id: the API looks
// the name up and removes the id before responding. Throughput is present on the
// organization block but absent from the per-project blocks.
const testInsightsSummaryBody = `{
  "org_data": {
    "metrics": {
      "total_runs": 5000,
      "total_duration_secs": 1800000,
      "total_credits_used": 2500000,
      "success_rate": 0.92,
      "throughput": 55.5
    },
    "trends": {
      "total_runs": 0.12,
      "total_duration_secs": -0.05,
      "total_credits_used": 0.08,
      "success_rate": 0.01,
      "throughput": 0.12
    }
  },
  "org_project_data": [
    {
      "project_name": "api",
      "metrics": {
        "total_runs": 3000,
        "total_duration_secs": 1000000,
        "total_credits_used": 1500000,
        "success_rate": 0.94
      },
      "trends": {
        "total_runs": 0.2,
        "total_duration_secs": 0.1,
        "total_credits_used": 0.15,
        "success_rate": -0.02
      }
    }
  ],
  "all_projects": ["api", "web-ui", "docs"]
}`

func TestInsightsGetSummary(t *testing.T) {
	t.Parallel()

	client, seen := newInsightsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testInsightsSummaryBody))
	})

	summary, err := client.Insights().GetSummary(
		context.Background(), testInsightsOrgSlug, circleci.InsightsWindow30Days, []string{"api", "web-ui"},
	)
	if err != nil {
		t.Fatalf("GetSummary returned error: %v", err)
	}

	// project-names repeats rather than taking a comma-separated list, and the org
	// slug is two segments with its separator literal. The query is sorted by key,
	// which is how the encoder serializes it; the repeated key keeps its order.
	want := "GET /api/v2/insights/gh/acme/summary?project-names=api&project-names=web-ui&reporting-window=last-30-days"
	if got := (*seen)[0]; got != want {
		t.Errorf("request = %q, want %q", got, want)
	}

	metrics := summary.OrganizationData.Metrics
	if metrics.TotalRuns != 5000 || metrics.TotalCreditsUsed != 2500000 {
		t.Errorf("org metrics = %d runs / %d credits, want 5000 / 2500000", metrics.TotalRuns, metrics.TotalCreditsUsed)
	}
	if metrics.TotalDurationSecs != 1800000 {
		t.Errorf("org total duration = %d, want 1800000", metrics.TotalDurationSecs)
	}
	if metrics.Throughput == nil || *metrics.Throughput != 55.5 {
		t.Errorf("org throughput = %v, want 55.5", metrics.Throughput)
	}
	if trend := summary.OrganizationData.Trends.TotalDurationSecs; trend != -0.05 {
		t.Errorf("duration trend = %v, want -0.05 (trends may be negative)", trend)
	}

	if len(summary.OrganizationProjectData) != 1 {
		t.Fatalf("project count = %d, want 1", len(summary.OrganizationProjectData))
	}
	project := summary.OrganizationProjectData[0]
	if project.ProjectName != "api" {
		t.Errorf("project name = %q, want %q", project.ProjectName, "api")
	}
	if project.Metrics.SuccessRate != 0.94 {
		t.Errorf("project success rate = %v, want 0.94", project.Metrics.SuccessRate)
	}
	// Throughput is not reported per project, so it must stay nil rather than
	// claiming zero runs per day.
	if project.Metrics.Throughput != nil {
		t.Errorf("project throughput = %v, want nil (not reported per project)", project.Metrics.Throughput)
	}

	if len(summary.AllProjects) != 3 || summary.AllProjects[0] != "api" {
		t.Errorf("all projects = %v, want three names starting with api", summary.AllProjects)
	}
}

func TestInsightsGetSummaryWithoutProjectNames(t *testing.T) {
	t.Parallel()

	// Asking for no project names is valid: it yields organization totals plus the
	// list of every project name, which is how a caller discovers the names to ask
	// for on a second call.
	client, seen := newInsightsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"org_data":{"metrics":{},"trends":{}},"org_project_data":[],"all_projects":["api"]}`))
	})

	summary, err := client.Insights().GetSummary(context.Background(), testInsightsOrgSlug, "", nil)
	if err != nil {
		t.Fatalf("GetSummary returned error: %v", err)
	}

	if got := (*seen)[0]; got != "GET /api/v2/insights/gh/acme/summary" {
		t.Errorf("request = %q, want no query parameters", got)
	}
	if len(summary.OrganizationProjectData) != 0 {
		t.Errorf("project data = %v, want empty when no names were requested", summary.OrganizationProjectData)
	}
	if len(summary.AllProjects) != 1 {
		t.Errorf("all projects = %v, want one name", summary.AllProjects)
	}
}

func TestInsightsGetSummaryStandaloneOrgSlug(t *testing.T) {
	t.Parallel()

	client, seen := newInsightsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"org_data":{"metrics":{},"trends":{}},"org_project_data":[],"all_projects":[]}`))
	})

	const slug = "circleci/11111111-1111-1111-1111-111111111111"
	if _, err := client.Insights().GetSummary(context.Background(), slug, "", nil); err != nil {
		t.Fatalf("GetSummary returned error: %v", err)
	}

	want := "GET /api/v2/insights/" + slug + "/summary"
	if got := (*seen)[0]; got != want {
		t.Errorf("request = %q, want %q", got, want)
	}
}

func TestInsightsRateLimited(t *testing.T) {
	t.Parallel()

	// Insights answers 429 when the caller exhausts its quota. That must not be
	// mistaken for a missing project, or a data source would report an empty result
	// instead of explaining the request should be retried.
	client, _ := newInsightsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"Rate Limit Exceeded"}`))
	})

	_, err := client.Insights().GetFlakyTests(context.Background(), testInsightsProjectSlug)
	if err == nil {
		t.Fatal("GetFlakyTests returned no error for a 429, want one")
	}
	if circleci.IsNotFound(err) {
		t.Error("IsNotFound() = true for a 429, want false")
	}
	if !circleci.HasStatus(err, http.StatusTooManyRequests) {
		t.Errorf("HasStatus(429) = false for %v, want true", err)
	}
	if detail := circleci.Detail(err); !strings.Contains(detail, "Rate Limit Exceeded") {
		t.Errorf("Detail() = %q, want it to carry the server message", detail)
	}
}
