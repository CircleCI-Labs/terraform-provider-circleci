# Fetches a project's deploy/release settings: the pipeline definitions used
# for automatic deploys and rollbacks. Available on CircleCI Cloud only.
data "circleci_deploy_settings" "this" {
  project_id = "00000000-0000-0000-0000-000000000000"
}

output "circleci_rollback_pipeline_definition_id" {
  value = data.circleci_deploy_settings.this.rollback_pipeline_definition_id
}
