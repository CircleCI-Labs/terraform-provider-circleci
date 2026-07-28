---
page_title: "circleci_insights_workflows Data Source - circleci"
subcategory: ""
description: |-
  Fetches per-workflow summary metrics for a project: run counts, success rate, duration percentiles and credits consumed.
---

# circleci_insights_workflows (Data Source)

Fetches per-workflow summary metrics for a project: run counts, success rate, duration percentiles and credits consumed.

The practical use is a health check a plan can act on — asserting a workflow's success rate has not dropped below a threshold, or surfacing the slowest workflows as outputs for a dashboard.

Pagination is followed internally, so the result covers every workflow rather than one page.

## Availability

| | |
| --- | --- |
| **CircleCI Cloud** | Yes |
| **CircleCI Server** | Depends on the installation. The route is v2, but Insights is a separate service that a given CircleCI Server installation may not run. The provider does not block `deployment = "server"`; if the installation has no Insights, the request fails with a not-found error. |
| **API** | `GET /api/v2/insights/{project-slug}/workflows` |
| **Organization type** | Any. |
| **Token** | An API token with permission to view the project's builds. |

## Accuracy

~> **Insights metrics are approximate and lag reality.** They are recomputed **daily**, so the last 24 hours of activity may be missing — a plan run immediately after a pipeline will not see it. No window longer than 90 days is retained.

~> **Do not use credit figures for cost reporting.** `total_credits_used` does not share a source of truth with billing. CircleCI documents Insights as unsuitable for credit reporting, and the Plan Overview page in the web application as the only accurate source. Treat these values as indicators, not as inputs to anything that must be exact.

## Project slugs are not always `vcs/org/repo`

The `project_slug` argument takes the familiar `vcs-slug/org-name/repo-name` form — `gh/acme/api` — for GitHub OAuth and Bitbucket projects.

**GitLab, GitHub App and GitHub Server projects do not have that form.** They have no VCS-side organization and repository name to build a slug from, so their slug is `circleci/<org-uuid>/<project-uuid>` instead. Both forms are accepted here. The `slug` attribute of the `circleci_project` data source reports whichever one applies, which is the reliable way to get it right.

A slug with the wrong number of segments is rejected before a request is made, so a malformed slug produces an explanation rather than an HTTP 404 that looks like a missing project.

## `branch` and `all_branches`

Omitting `branch` does **not** report all branches — the API falls back to the project's default branch. Set `all_branches = true` to combine every branch instead. The two arguments conflict: the API reads `all-branches` first and silently ignores `branch`, so the provider rejects the combination rather than quietly reporting something other than what was asked for.

## Nulls are meaningful

`mttr`, `total_credits_used`, `total_recoveries` and every `duration_*` attribute are `null` rather than `0` when the API has nothing to report. A workflow that never failed has no mean time to recovery, which is not the same as a recovery time of zero seconds, so compare against `null` before doing arithmetic.

`success_rate` is a ratio between 0 and 1, not a percentage. Compare against `0.95`, not `95`.

## Example Usage

```terraform
# Per-workflow metrics for a project over the last 30 days, across every branch
# rather than just the default one.
data "circleci_insights_workflows" "api" {
  project_slug     = "gh/acme/api"
  reporting_window = "last-30-days"
  all_branches     = true
}

# A GitLab, GitHub App or GitHub Server project has no VCS-side organization and
# repository name, so its slug is circleci/<org-uuid>/<project-uuid> instead. Both
# forms are accepted.
data "circleci_insights_workflows" "standalone" {
  project_slug = "circleci/${var.organization_id}/${var.project_id}"
  branch       = "main"
}

locals {
  by_name = {
    for w in data.circleci_insights_workflows.api.workflows : w.name => w
  }
}

# success_rate is a ratio between 0 and 1, not a percentage.
check "build_workflow_is_healthy" {
  assert {
    condition     = local.by_name["build-and-test"].success_rate >= 0.9
    error_message = "build-and-test succeeded on only ${local.by_name["build-and-test"].success_rate * 100}% of runs in the last 30 days."
  }
}

# The 95th percentile is usually the more useful latency figure than the mean.
output "slow_workflows" {
  value = {
    for w in data.circleci_insights_workflows.api.workflows :
    w.name => w.duration_p95
    if w.duration_p95 != null && w.duration_p95 > 600
  }
}

# mttr is null for a workflow that never failed, which is not the same as a recovery
# time of zero, so the null has to be handled rather than compared.
output "recovery_times" {
  value = {
    for w in data.circleci_insights_workflows.api.workflows :
    w.name => w.mttr == null ? "never failed" : "${w.mttr}s"
  }
}
```

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `project_slug` (String) Slug of the project to report on, in `vcs-slug/org-name/repo-name` form — for example `gh/acme/api`.

GitLab, GitHub App and GitHub Server projects do not have that form. They have no VCS-side organization and repository name to build a slug from, so their slug is `circleci/<org-uuid>/<project-uuid>` instead. Both forms are accepted; the `slug` attribute of the `circleci_project` data source reports whichever one applies.

### Optional

- `all_branches` (Boolean) Combine every branch into one set of metrics instead of scoping to a single branch. Conflicts with `branch`.
- `branch` (String) Scope the metrics to a single branch. Omitting this does not report all branches — the API falls back to the project's default branch. Set `all_branches` to combine every branch instead.
- `reporting_window` (String) The aggregation window. One of `last-24-hours`, `last-7-days`, `last-30-days`, `last-60-days` or `last-90-days`. Defaults to `last-90-days`, which is also the longest window Insights retains.

### Read-Only

- `workflows` (Attributes List) One entry per workflow, in the order the API returns them. (see [below for nested schema](#nestedatt--workflows))

<a id="nestedatt--workflows"></a>
### Nested Schema for `workflows`

Read-Only:

- `duration_max` (Number) Longest run duration in seconds, or `null` when nothing ran.
- `duration_mean` (Number) Mean run duration in seconds, or `null` when nothing ran.
- `duration_median` (Number) Median run duration in seconds, or `null` when nothing ran.
- `duration_min` (Number) Shortest run duration in seconds, or `null` when nothing ran.
- `duration_p95` (Number) 95th percentile run duration in seconds, or `null` when nothing ran. This is usually the more useful latency figure than the mean.
- `duration_standard_deviation` (Number) Standard deviation of run duration in seconds, or `null` when nothing ran.
- `failed_runs` (Number) Runs that failed.
- `mttr` (Number) Mean time to recovery in seconds: the mean gap between a failure and the next success. `null` for a workflow that never failed in the window, which is not the same as a recovery time of zero.
- `name` (String) Name of the workflow, as written in `.circleci/config.yml`.
- `project_id` (String) Unique identifier (UUID) of the project the workflow belongs to.
- `success_rate` (Number) Proportion of runs that succeeded, as a ratio between 0 and 1 — not a percentage. Compare against `0.95`, not `95`.
- `successful_runs` (Number) Runs that succeeded.
- `throughput` (Number) Average number of runs per day over the window.
- `total_credits_used` (Number) Credits consumed by the workflow in the window, or `null` when not reported. See the accuracy note above before relying on this.
- `total_recoveries` (Number) Number of runs that recovered a previously failing workflow. `null` when the workflow never failed.
- `total_runs` (Number) Total runs in the window, including runs still on hold or running. This is therefore not always `successful_runs + failed_runs`.
- `window_end` (String) Timestamp of the last run inside the reporting window (RFC 3339).
- `window_start` (String) Timestamp of the first run inside the reporting window (RFC 3339).
