---
page_title: "circleci_insights_summary Data Source - circleci"
subcategory: ""
description: |-
  Fetches organization-wide Insights metrics, with the change against the preceding window, and optionally the same for named projects.
---

# circleci_insights_summary (Data Source)

Fetches organization-wide Insights metrics, with the change against the preceding window, and optionally the same for named projects.

This is the only organization-level view the CircleCI API offers: total runs, total compute duration, credits consumed and success rate, each with a trend. Use it for a rollup output, or to assert an organization's success rate has not regressed.

## Availability

| | |
| --- | --- |
| **CircleCI Cloud** | Yes |
| **CircleCI Server** | Depends on the installation. The route is v2, but Insights is a separate service that a given CircleCI Server installation may not run. The provider does not block `deployment = "server"`; if the installation has no Insights, the request fails with a not-found error. |
| **API** | `GET /api/v2/insights/{org-slug}/summary` |
| **Organization type** | Any. |
| **Token** | An API token with permission to view the organization's builds. |

## `projects` is empty unless you ask for names

~> **The API does not return every project by default.** It only computes per-project metrics for the names passed in `project_names`. With no names, `projects` is an empty list — the organization totals are still populated.

`all_projects` exists to close that loop: it lists every project name in the organization the token can see, which is how you discover the names to ask for. In practice that means **two applies** to get per-project data for a project set you do not already know: one to read `all_projects`, and one to feed those names back in.

Note that these are project *names*, not slugs.

## Organization slugs are two segments, not three

The `organization_slug` argument takes `vcs-slug/org-name` — `gh/acme` — or `circleci/<org-uuid>` for a standalone organization. That is **two** segments, unlike the three of a project slug. Passing a project slug here is rejected before a request is made, with an error saying so.

`circleci_user_collaborations` reports the slug of every organization the configured token can see, which is the reliable way to get one.

## Accuracy

~> **Insights metrics are approximate and lag reality.** They are recomputed **daily**, so the last 24 hours of activity may be missing. No window longer than 90 days is retained.

~> **Do not use credit figures for cost reporting.** `total_credits_used` does not share a source of truth with billing. CircleCI documents Insights as unsuitable for credit reporting, and the Plan Overview page in the web application as the only accurate source. An organization-wide credit total is exactly the figure someone will be tempted to put in a finance report; it is not suitable for that.

## Reading the numbers

- `success_rate` is a ratio between 0 and 1, not a percentage.
- Every value under `trends` is a ratio of change against the preceding window of the same length: `0.1` is a ten percent increase, `-0.1` a ten percent decrease. A negative `total_duration_secs` trend is an improvement.
- `throughput` is reported for the organization but is `null` for individual projects, which the API does not compute it for.

## Example Usage

```terraform
# Organization-wide metrics with the change against the preceding window. Note the
# organization slug is two segments, unlike a project slug's three.
data "circleci_insights_summary" "acme" {
  organization_slug = "gh/acme"
  reporting_window  = "last-30-days"
}

# all_projects lists every project name the token can see. That is how you discover
# the names to pass in project_names — the API does not report per-project metrics
# unless it is asked for specific names.
output "project_names" {
  value = data.circleci_insights_summary.acme.all_projects
}

# A second read, now asking for per-project data. In a real configuration these are
# usually two applies: the names have to be known before they can be requested.
data "circleci_insights_summary" "acme_projects" {
  organization_slug = "gh/acme"
  reporting_window  = "last-30-days"
  project_names     = ["api", "web-ui"]
}

output "credits_by_project" {
  value = {
    for p in data.circleci_insights_summary.acme_projects.projects :
    p.project_name => p.metrics.total_credits_used
  }
}

# Trends are ratios: 0.1 is a ten percent increase, -0.1 a ten percent decrease.
output "credit_spend_trend" {
  value = data.circleci_insights_summary.acme.trends.total_credits_used
}

# success_rate is a ratio between 0 and 1, not a percentage.
check "organization_success_rate" {
  assert {
    condition     = data.circleci_insights_summary.acme.metrics.success_rate >= 0.85
    error_message = "Organization-wide success rate has fallen to ${data.circleci_insights_summary.acme.metrics.success_rate * 100}%."
  }
}

# throughput is reported for the organization but null for individual projects, which
# the API does not compute it for.
output "runs_per_day" {
  value = data.circleci_insights_summary.acme.metrics.throughput
}
```

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `organization_slug` (String) Slug of the organization to report on, in `vcs-slug/org-name` form — for example `gh/acme` — or `circleci/<org-uuid>` for a standalone organization.

Note this is two segments, unlike a project slug's three. The `circleci_user_collaborations` data source reports the slug of every organization the configured token can see.

### Optional

- `project_names` (List of String) Names of the projects to report per-project metrics for. These are project names, not slugs. Omit this to get organization totals only; the names available are reported in `all_projects`.
- `reporting_window` (String) The aggregation window. One of `last-24-hours`, `last-7-days`, `last-30-days`, `last-60-days` or `last-90-days`. Defaults to `last-90-days`, which is also the longest window Insights retains.

### Read-Only

- `all_projects` (List of String) Names of every project in the organization the configured token can see. This is what to read to discover the values to pass in `project_names`.
- `metrics` (Attributes) Aggregated metrics for the whole organization over the window. (see [below for nested schema](#nestedatt--metrics))
- `projects` (Attributes List) Per-project metrics, one entry per name in `project_names` that the API recognised. Empty when `project_names` is not set. (see [below for nested schema](#nestedatt--projects))
- `trends` (Attributes) Change in each organization-wide metric against the preceding window of the same length. (see [below for nested schema](#nestedatt--trends))

<a id="nestedatt--metrics"></a>
### Nested Schema for `metrics`

Read-Only:

- `success_rate` (Number) Proportion of runs that succeeded, as a ratio between 0 and 1 — not a percentage.
- `throughput` (Number) Average number of runs per day. Reported for the organization but `null` for individual projects, which the API does not compute it for.
- `total_credits_used` (Number) Credits consumed in the window. See the accuracy note above before relying on this.
- `total_duration_secs` (Number) Total compute duration in seconds across every run in the window.
- `total_runs` (Number) Total pipeline runs in the window.


<a id="nestedatt--projects"></a>
### Nested Schema for `projects`

Read-Only:

- `metrics` (Attributes) Aggregated metrics for this project, across all branches. (see [below for nested schema](#nestedatt--projects--metrics))
- `project_name` (String) Name of the project. The API identifies projects by name here rather than by ID.
- `trends` (Attributes) Change in each of this project's metrics against the preceding window. (see [below for nested schema](#nestedatt--projects--trends))

<a id="nestedatt--projects--metrics"></a>
### Nested Schema for `projects.metrics`

Read-Only:

- `success_rate` (Number) Proportion of runs that succeeded, as a ratio between 0 and 1 — not a percentage.
- `throughput` (Number) Average number of runs per day. Reported for the organization but `null` for individual projects, which the API does not compute it for.
- `total_credits_used` (Number) Credits consumed in the window. See the accuracy note above before relying on this.
- `total_duration_secs` (Number) Total compute duration in seconds across every run in the window.
- `total_runs` (Number) Total pipeline runs in the window.


<a id="nestedatt--projects--trends"></a>
### Nested Schema for `projects.trends`

Read-Only:

- `success_rate` (Number) Change in success rate against the preceding window of the same length, as a ratio: `0.1` is a ten percent increase and `-0.1` a ten percent decrease.
- `throughput` (Number) Change in throughput against the preceding window of the same length, as a ratio: `0.1` is a ten percent increase and `-0.1` a ten percent decrease.
- `total_credits_used` (Number) Change in credits consumed against the preceding window of the same length, as a ratio: `0.1` is a ten percent increase and `-0.1` a ten percent decrease.
- `total_duration_secs` (Number) Change in total duration against the preceding window of the same length, as a ratio: `0.1` is a ten percent increase and `-0.1` a ten percent decrease.
- `total_runs` (Number) Change in total runs against the preceding window of the same length, as a ratio: `0.1` is a ten percent increase and `-0.1` a ten percent decrease.



<a id="nestedatt--trends"></a>
### Nested Schema for `trends`

Read-Only:

- `success_rate` (Number) Change in success rate against the preceding window of the same length, as a ratio: `0.1` is a ten percent increase and `-0.1` a ten percent decrease.
- `throughput` (Number) Change in throughput against the preceding window of the same length, as a ratio: `0.1` is a ten percent increase and `-0.1` a ten percent decrease.
- `total_credits_used` (Number) Change in credits consumed against the preceding window of the same length, as a ratio: `0.1` is a ten percent increase and `-0.1` a ten percent decrease.
- `total_duration_secs` (Number) Change in total duration against the preceding window of the same length, as a ratio: `0.1` is a ten percent increase and `-0.1` a ten percent decrease.
- `total_runs` (Number) Change in total runs against the preceding window of the same length, as a ratio: `0.1` is a ten percent increase and `-0.1` a ten percent decrease.
