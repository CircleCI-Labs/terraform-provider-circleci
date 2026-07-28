# Classic GitHub OAuth or Bitbucket: addressed by name.
output "classic_slug" {
  value = provider::circleci::project_slug("gh", "my-org", "my-repo")
}

# GitLab, GitHub App, GHES, and standalone organizations: addressed by UUID.
# There is no "gitlab" or "github_app" vcs_type — both collapse to "circleci".
output "uuid_slug" {
  value = provider::circleci::project_slug(
    "circleci",
    "11111111-1111-1111-1111-111111111111",
    "22222222-2222-2222-2222-222222222222",
  )
}

data "circleci_project" "example" {
  project_slug = provider::circleci::project_slug("gh", "my-org", "my-repo")
}
