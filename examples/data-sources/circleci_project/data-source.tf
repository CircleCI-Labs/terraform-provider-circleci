# The slug is "vcs-type/org-name/repo-name" for a VCS-connected project, or
# "circleci/<org-uuid>/<project-name>" for a standalone one.
data "circleci_project" "api" {
  slug = "github/acme/api"
}

# This lookup is mostly how you get from the slug you have to the project UUID that
# the trigger, pipeline-definition and context-restriction APIs require.
resource "circleci_pipeline_definition" "build" {
  project_id  = data.circleci_project.api.id
  name        = "build"
  description = "Build and test on every push"

  config_source_provider         = "github_app"
  config_source_file_path        = ".circleci/config.yml"
  config_source_repo_external_id = "123456789" # GitHub's numeric repository id

  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = "123456789"
}

# The organization is reported under both `org_id` and the deprecated
# `organization_id`; they always hold the same value.
output "circleci_api_project" {
  value = {
    id                = data.circleci_project.api.id
    name              = data.circleci_project.api.name
    org_id            = data.circleci_project.api.org_id
    organization_name = data.circleci_project.api.organization_name
    organization_slug = data.circleci_project.api.organization_slug

    default_branch = data.circleci_project.api.vcs_info.default_branch
    vcs_provider   = data.circleci_project.api.vcs_info.provider
    vcs_url        = data.circleci_project.api.vcs_info.vcs_url
  }
}

# Build settings are deliberately not reported here: this data source describes the
# project's identity and its connected repository. Use the circleci_project_settings
# data source for auto_cancel_builds, build_fork_prs, oss, pr_only_branch_overrides
# and the rest.
