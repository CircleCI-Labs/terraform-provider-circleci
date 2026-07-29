# Reads one pipeline definition: where CircleCI checks out from, and where it
# finds the configuration. A definition is not an execution — for one of those,
# see circleci_pipeline_run.
#
# CircleCI Cloud only: CircleCI Server does not route pipeline-definitions.
data "circleci_pipeline_definition" "nightly" {
  id         = "44444444-4444-4444-4444-444444444444"
  project_id = "00000000-0000-0000-0000-000000000000"
}

output "circleci_pipeline_definition_name" {
  value = data.circleci_pipeline_definition.nightly.name
}

# The config source says which file in which repository drives this definition.
output "circleci_pipeline_definition_config_source" {
  value = {
    provider  = data.circleci_pipeline_definition.nightly.config_source_provider
    repo      = data.circleci_pipeline_definition.nightly.config_source_repo_full_name
    file_path = data.circleci_pipeline_definition.nightly.config_source_file_path
  }
}

# The checkout source is tracked separately, because a definition can compile
# its configuration from one repository while checking out another.
output "circleci_pipeline_definition_checkout_source" {
  value = {
    provider = data.circleci_pipeline_definition.nightly.checkout_source_provider
    repo     = data.circleci_pipeline_definition.nightly.checkout_source_repo_full_name
  }
}
