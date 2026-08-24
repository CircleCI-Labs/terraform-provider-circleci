# The second accepted config_source_provider / checkout_source_provider value:
# GitHub Enterprise Server, alongside the github_app example above. It behaves
# identically — same required attributes — but its repository ids belong to your
# own GitHub Enterprise Server installation rather than to github.com, so they
# are small integers allocated per installation, not github_app's ids, and the
# two cannot be mixed.
#
# There used to be a third config_source_provider here: "circleci", a
# CircleCI-hosted configuration with no repository at all. It is not an accepted
# value — every create attempt against a customer-plausible file path answers
# HTTP 400 "Invalid config file path." — so there is no working example to show.
data "circleci_project" "github_server_example" {
  slug = "github/acme-internal/payments"
}

locals {
  # The repository id on the GitHub Server installation. These are small
  # integers allocated per installation, not github.com ids.
  github_server_example_repo_external_id = "2259"
}

resource "circleci_pipeline_definition" "github_server_example" {
  project_id  = data.circleci_project.github_server_example.id
  name        = "build"
  description = "Build and test on every push"

  config_source_provider         = "github_server"
  config_source_file_path        = ".circleci/config.yml"
  config_source_repo_external_id = local.github_server_example_repo_external_id

  checkout_source_provider         = "github_server"
  checkout_source_repo_external_id = local.github_server_example_repo_external_id
}
