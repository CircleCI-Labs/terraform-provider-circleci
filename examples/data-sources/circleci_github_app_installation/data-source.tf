# Whether the CircleCI GitHub App is installed for an organization, and how much
# of the GitHub account it can reach. Reading this fails with an explicit error
# when the app is not installed at all, which is what makes it a usable
# precondition check for the two repository data sources below.
data "circleci_github_app_installation" "acme" {
  # The organization's UUID, as shown on Organization Settings in the CircleCI
  # web application. The GitHub App integration only exists for `circleci` type
  # (standalone) organizations.
  org_id = "00000000-0000-0000-0000-000000000000"
}

# The repositories data source answers with an empty list both when nothing is
# installed and when an installation was granted no repositories, so on its own
# an empty result is ambiguous. Pairing it with the installation above resolves
# that: if the installation read succeeded, the app *is* installed.
data "circleci_github_app_repositories" "acme" {
  org_id = data.circleci_github_app_installation.acme.org_id
}

# `repository_selection` is "all" when the installation can reach every
# repository the GitHub account owns, or "selected" when it was scoped to a
# subset. A repository missing from a "selected" installation is invisible to
# Terraform, and the fix is on GitHub — not in this configuration — so surface it
# as a check rather than letting a later lookup fail with "not found".
check "github_app_grants_repositories" {
  assert {
    condition     = length(data.circleci_github_app_repositories.acme.repositories) > 0
    error_message = "The CircleCI GitHub App is installed on ${data.circleci_github_app_installation.acme.login} (repository_selection = ${data.circleci_github_app_installation.acme.repository_selection}) but has been granted no repositories. Widen the installation on GitHub."
  }
}

# Resolving a repository name to the numeric GitHub ID that
# circleci_pipeline_definition and circleci_trigger require is the reason the
# GitHub App data sources exist at all; the installation is what makes that
# lookup possible.
data "circleci_github_app_repository" "api" {
  org_id    = data.circleci_github_app_installation.acme.org_id
  full_name = "acme/api"
}

output "api_repository_external_id" {
  value = data.circleci_github_app_repository.api.external_id
}

# `target_type` distinguishes an app installed on a GitHub organization from one
# installed on a single user account: a user installation can only ever reach
# that user's own repositories. `id` is the installation's own numeric id, not
# any repository id.
output "github_app_installation" {
  value = {
    installation_id      = data.circleci_github_app_installation.acme.id
    github_account       = data.circleci_github_app_installation.acme.login
    account_kind         = data.circleci_github_app_installation.acme.target_type
    repository_selection = data.circleci_github_app_installation.acme.repository_selection
  }
}
