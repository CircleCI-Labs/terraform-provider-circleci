# Reads the built-in pipeline.* values for a pipeline run — the same values a
# config interpolates as << pipeline.number >>. Available on both CircleCI Cloud
# and CircleCI Server.
data "circleci_pipeline_run" "this" {
  project_slug = "gh/CircleCI-Public/api-preview-docs"
  number       = 25
}

data "circleci_pipeline_run_values" "this" {
  run_id = data.circleci_pipeline_run.this.id
}

# Every value is rendered as a string, because the API mixes strings and numbers
# in one response and a map attribute has a single element type.
output "circleci_pipeline_run_branch" {
  value = data.circleci_pipeline_run_values.this.values["pipeline.git.branch"]
}

output "circleci_pipeline_run_number" {
  value = data.circleci_pipeline_run_values.this.values["pipeline.number"]
}
