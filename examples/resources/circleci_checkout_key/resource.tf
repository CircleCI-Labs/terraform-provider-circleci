# A deploy key: scoped to this repository only.
resource "circleci_checkout_key" "deploy" {
  project_slug = "github/my-org/my-repo"
  type         = "deploy-key"
}

# A user key: carries the permissions of the user whose API token created it, so
# it can also check out other repositories such as private submodules. Creating
# one requires a user API token rather than a project token.
resource "circleci_checkout_key" "user" {
  project_slug = "github/my-org/my-repo"
  type         = "user-key"
}

# The fingerprint is what a job's add_ssh_keys step refers to.
output "deploy_key_fingerprint" {
  value = circleci_checkout_key.deploy.fingerprint
}
