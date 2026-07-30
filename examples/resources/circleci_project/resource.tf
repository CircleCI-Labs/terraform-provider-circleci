data "circleci_organization" "acme" {
  slug = "gh/acme"
}

# Use this resource for a project Terraform creates. For a project that already
# exists in CircleCI, use circleci_project_settings instead: this resource owns the
# project's whole settings record and cannot adopt a project it did not create.
#
# A setting this configuration does not mention is not sent at all, so CircleCI
# applies its own default — and those defaults are not uniformly false. The table
# further down this page lists them.
resource "circleci_project" "api" {
  org_id = data.circleci_organization.acme.id
  name   = "api" # the repository name

  auto_cancel_builds            = true
  disable_ssh                   = true
  set_github_status             = true
  setup_workflows               = false
  write_settings_requires_admin = false
}

# Build only branches with an open pull request, with named exceptions.
# `pr_only_branch_overrides` is a set: CircleCI does not preserve the order branches
# are sent in, so order here is not significant.
resource "circleci_project" "web" {
  org_id = data.circleci_organization.acme.id
  name   = "web"

  build_prs_only           = true
  pr_only_branch_overrides = ["main", "develop"]

  # `forks_receive_secret_env_vars` must be set explicitly whenever
  # `build_fork_prs` is true, and the provider errors at validate time otherwise.
  # There is no safe default to fall back on: CircleCI leaves it at true on a
  # private project, which would hand this project's environment variables, secrets
  # and build cache to a pull request opened from any fork.
  build_fork_prs                = true
  forks_receive_secret_env_vars = false
}

# `oss` is read-only. CircleCI reports it but the settings API rejects the field
# outright, so it is Computed here and has to be set in the web application.
output "circleci_api_project" {
  value = {
    id             = circleci_project.api.id
    slug           = circleci_project.api.slug
    default_branch = circleci_project.api.vcs_info_default_branch
    oss            = circleci_project.api.oss
  }
}
