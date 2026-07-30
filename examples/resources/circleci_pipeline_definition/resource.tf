# A definition is keyed by project UUID, which is not something CircleCI shows you
# — resolve it from the slug rather than pasting a literal.
data "circleci_project" "example" {
  slug = "github/acme/api"
}

# GitHub's own numeric repository id, which is what the github_app config and
# checkout sources are keyed by, not the repository name. The
# circleci_github_app_repository data source reports it; so does
# `gh api repos/acme/api --jq .id`.
data "circleci_github_app_repository" "example" {
  org_id    = data.circleci_project.example.org_id
  full_name = "acme/api"
}

resource "circleci_pipeline_definition" "example" {
  project_id  = data.circleci_project.example.id
  name        = "my-pipeline"
  description = "Main CI/CD pipeline"

  config_source_provider         = "github_app"
  config_source_file_path        = ".circleci/config.yml"
  config_source_repo_external_id = data.circleci_github_app_repository.example.external_id

  # A definition can compile its configuration from one repository while checking
  # out another, so the checkout source is set separately even when it is the same.
  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = data.circleci_github_app_repository.example.external_id
}

# Migrating from the old `circleci_pipeline` type name? Add a moved block. The
# definition is re-addressed in state rather than destroyed and recreated, so
# its ID is preserved and any triggers attached to it stay attached. Delete the
# block once you have applied it.
moved {
  from = circleci_pipeline.example
  to   = circleci_pipeline_definition.example
}
