# Reads a project's advanced settings. Use this to audit settings across projects,
# or to assert a security-relevant toggle rather than assuming it.
#
# To *manage* these settings, use circleci_project_settings (the resource) or the
# settings attributes on circleci_project.
data "circleci_project_settings" "api" {
  slug = "github/acme/api"
}

# The two toggles that together decide whether a fork's pull request can read your
# secrets. CircleCI only exposes secrets to a fork PR when BOTH are true, so a
# check on the pair is more meaningful than a check on either alone.
check "forks_cannot_read_secrets" {
  assert {
    condition = !(
      data.circleci_project_settings.api.build_fork_prs &&
      data.circleci_project_settings.api.forks_receive_secret_env_vars
    )
    error_message = "Fork pull requests on acme/api can read secret environment variables."
  }
}

output "circleci_project_settings_api" {
  value = {
    autocancel_builds             = data.circleci_project_settings.api.auto_cancel_builds
    build_fork_prs                = data.circleci_project_settings.api.build_fork_prs
    build_prs_only                = data.circleci_project_settings.api.build_prs_only
    forks_receive_secret_env_vars = data.circleci_project_settings.api.forks_receive_secret_env_vars
    set_github_status             = data.circleci_project_settings.api.set_github_status
  }
}

# Branches exempted from build_prs_only, so pushes to them build even when the
# project otherwise only builds pull requests. This is a set: order is not
# meaningful and the API does not preserve the order you sent.
output "circleci_project_settings_pr_only_overrides" {
  value = data.circleci_project_settings.api.pr_only_branch_overrides
}
