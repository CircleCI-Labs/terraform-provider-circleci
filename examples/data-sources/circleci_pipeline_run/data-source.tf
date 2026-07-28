# Fetches a pipeline run by id. Available on both CircleCI Cloud and
# CircleCI Server.
#
# NOTE: this reads mutable runtime state (state/errors/warnings change while
# the run is processed). Use it for inspection or in `check` blocks, not to
# derive a resource attribute — the state or config data source and the
# check are all read-only, but a resource attribute recomputed from it would
# perpetually diff.
data "circleci_pipeline_run" "by_id" {
  id = "5034460f-c7c4-4c43-9457-de07e2029e7b"
}

# Fetches the same run by project slug and pipeline number instead.
data "circleci_pipeline_run" "by_number" {
  project_slug = "gh/CircleCI-Public/api-preview-docs"
  number       = 25
}

check "pipeline_did_not_error" {
  data "circleci_pipeline_run" "check" {
    id = "5034460f-c7c4-4c43-9457-de07e2029e7b"
  }

  assert {
    condition     = length(data.circleci_pipeline_run.check.errors) == 0
    error_message = "Pipeline run has errors: ${jsonencode(data.circleci_pipeline_run.check.errors)}"
  }
}
