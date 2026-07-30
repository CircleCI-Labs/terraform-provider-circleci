# GitHub Server (GitHub Enterprise Server) event source. It behaves exactly like
# `github_app` — same required attributes, same presets — but the repository ids
# belong to your own GitHub installation rather than to github.com, so they cannot
# be shared with a github_app pipeline definition. This example therefore stands on
# its own rather than reusing the one above.
#
# This concerns the *event source*, not where CircleCI runs: triggers are CircleCI
# Cloud only, whichever GitHub the organization is connected to.
data "circleci_project" "internal" {
  slug = "github/acme-internal/payments"
}

locals {
  # The repository id on the GitHub Server installation. These are small integers
  # allocated per installation, not github.com ids.
  internal_repo_external_id = "2259"
}

resource "circleci_pipeline_definition" "internal_build" {
  project_id  = data.circleci_project.internal.id
  name        = "build"
  description = "Build and test on every push"

  config_source_provider         = "github_server"
  config_source_file_path        = ".circleci/config.yml"
  config_source_repo_external_id = local.internal_repo_external_id

  checkout_source_provider         = "github_server"
  checkout_source_repo_external_id = local.internal_repo_external_id
}

resource "circleci_trigger" "github_server" {
  project_id                    = data.circleci_project.internal.id
  pipeline_definition_id        = circleci_pipeline_definition.internal_build.id
  event_source_provider         = "github_server"
  event_source_repo_external_id = local.internal_repo_external_id
  event_preset                  = "only-build-prs"
  disabled                      = false
}
