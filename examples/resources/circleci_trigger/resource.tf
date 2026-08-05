# A trigger is created under a pipeline definition but read under the project, so
# both ids form its address — which is also why import needs all three segments.
# Referencing them by expression is what orders the graph correctly: the definition
# is created first, and the trigger is destroyed before it.
data "circleci_project" "api" {
  slug = "github/acme/api"
}

locals {
  # GitHub's own numeric repository id, which is what CircleCI's GitHub App event
  # sources are keyed by — not the repository name. The
  # circleci_github_app_repository data source reports it; so does
  # `gh api repos/acme/api --jq .id`.
  repo_external_id = "123456789"
}

resource "circleci_pipeline_definition" "build" {
  project_id  = data.circleci_project.api.id
  name        = "build"
  description = "Build and test on every push"

  config_source_provider         = "github_app"
  config_source_file_path        = ".circleci/config.yml"
  config_source_repo_external_id = local.repo_external_id

  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = local.repo_external_id
}

# GitHub App event source: run the pipeline on every push to the connected
# repository.
#
# `event_preset` selects which GitHub events fire the trigger, and is optional here
# (required for github_oauth, and rejected for webhook and schedule).
# `checkout_ref` and `config_ref` are deliberately absent: for a GitHub event source
# they are only expected when the event source repository differs from the pipeline
# definition's checkout or config repository, and must otherwise be omitted.
resource "circleci_trigger" "github_app" {
  project_id                    = data.circleci_project.api.id
  pipeline_definition_id        = circleci_pipeline_definition.build.id
  event_source_provider         = "github_app"
  event_source_repo_external_id = local.repo_external_id
  event_preset                  = "all-pushes"
  disabled                      = false
}

# `pipeline_id` is the deprecated spelling of `pipeline_definition_id` — it always
# took a pipeline *definition* id. Set exactly one of the two. Renaming the attribute
# is not a change and replaces nothing; changing the definition *id* it holds
# replaces the trigger, because a trigger is created under a definition and CircleCI
# has no route that moves it to another one.
