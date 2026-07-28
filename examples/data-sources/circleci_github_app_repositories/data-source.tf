# List every repository the CircleCI GitHub App can reach for an organization.
data "circleci_github_app_repositories" "all" {
  organization_id = var.organization_id
}

# Keyed by full name, this becomes a lookup table for external IDs.
locals {
  repo_external_ids = {
    for repo in data.circleci_github_app_repositories.all.repositories :
    repo.full_name => repo.external_id
  }
}

output "api_external_id" {
  value = local.repo_external_ids["acme/api"]
}

# Or drive resources directly off the listing. Private repositories only, here.
locals {
  private_repos = {
    for repo in data.circleci_github_app_repositories.all.repositories :
    repo.full_name => repo if repo.private
  }
}

output "private_repository_names" {
  value = keys(local.private_repos)
}

# Useful when circleci_github_app_repository cannot find a name: this shows what
# the installation is actually scoped to. A repository missing from here has to be
# granted to the app on GitHub.
output "visible_repository_names" {
  value = [for repo in data.circleci_github_app_repositories.all.repositories : repo.full_name]
}
