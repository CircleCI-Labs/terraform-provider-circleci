# Manage the advanced settings of a project that already exists in CircleCI.
# Only the settings named here are written; everything else is left as it is.
resource "circleci_project_settings" "example" {
  slug = "github/my-org/my-repo"

  auto_cancel_builds            = true
  build_fork_prs                = false
  forks_receive_secret_env_vars = false
  set_github_status             = true
}

# Build only branches with an open pull request, with exceptions.
resource "circleci_project_settings" "prs_only" {
  slug = "github/my-org/my-other-repo"

  build_prs_only           = true
  pr_only_branch_overrides = ["main", "develop"]
}

# Adopt a project's settings without managing any of them yet. Useful as a first
# step: import the resource, then add settings one at a time.
resource "circleci_project_settings" "adopted" {
  slug = "circleci/9a1b2c3d-4e5f-6789-abcd-ef0123456789/my-repo"
}
