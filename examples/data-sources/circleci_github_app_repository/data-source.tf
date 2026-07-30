# Resolve a repository name to the numeric GitHub ID that
# circleci_pipeline_definition and circleci_trigger require. Without this, the ID
# has to be copied out of the GitHub UI by hand and pasted into the configuration
# as a magic number.
variable "org_id" {
  type        = string
  description = <<-EOT
    UUID of the CircleCI organization whose GitHub App installation is searched.
    This is CircleCI's own organization id, not a GitHub id — the
    circleci_organization data source reports it for a slug.
  EOT
}

variable "project_id" {
  type        = string
  description = <<-EOT
    UUID of the CircleCI project the pipeline definition and trigger belong to. The
    circleci_project data source reports it for a project slug.
  EOT
}

data "circleci_github_app_repository" "api" {
  org_id    = var.org_id
  full_name = "acme/api"
}

resource "circleci_pipeline_definition" "api" {
  project_id  = var.project_id
  name        = "build"
  description = "Build and test the API"

  config_source_provider         = "github_app"
  config_source_file_path        = ".circleci/config.yml"
  config_source_repo_external_id = data.circleci_github_app_repository.api.external_id

  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = data.circleci_github_app_repository.api.external_id
}

resource "circleci_trigger" "api_push" {
  project_id             = var.project_id
  pipeline_definition_id = circleci_pipeline_definition.api.id

  event_source_provider         = "github_app"
  event_source_repo_external_id = data.circleci_github_app_repository.api.external_id
  event_preset                  = "all-pushes"
}

# The default branch is reported too, so a configuration does not have to repeat
# what GitHub already knows.
output "api_default_branch" {
  value = data.circleci_github_app_repository.api.default_branch
}
