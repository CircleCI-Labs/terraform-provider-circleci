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
