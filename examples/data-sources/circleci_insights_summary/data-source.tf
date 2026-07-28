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
