resource "circleci_pipeline_definition" "example" {
  project_id  = circleci_project.example.id
  name        = "my-pipeline"
  description = "Main CI/CD pipeline"

  config_source_provider         = "github_app"
  config_source_file_path        = ".circleci/config.yml"
  config_source_repo_external_id = "123456789"

  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = "123456789"
}

# Migrating from the old `circleci_pipeline` type name? Add a moved block. The
# definition is re-addressed in state rather than destroyed and recreated, so
# its ID is preserved and any triggers attached to it stay attached. Delete the
# block once you have applied it.
moved {
  from = circleci_pipeline.example
  to   = circleci_pipeline_definition.example
}
