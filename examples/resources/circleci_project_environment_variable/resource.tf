# `value` is recorded in Terraform state in cleartext. The API never returns a
# project environment variable's value on any route, so this attribute only ever
# reflects configuration or state -- there is no refresh that could catch drift
# against it.
resource "circleci_project_environment_variable" "example" {
  project_slug = "github/my-org/my-repo"
  name         = "MY_SECRET"
  value        = "my-secret-value"
}

# `value_wo` is the same argument as a write-only argument (Terraform 1.11 or
# later): it is sent to CircleCI and then discarded, so it lands in neither state
# nor the plan file. Set exactly one of `value` and `value_wo`.
variable "deploy_token" {
  description = "Deploy token, supplied out of band -- TF_VAR_deploy_token, or a -var-file."
  type        = string
  sensitive   = true
  ephemeral   = true
}

# Because nothing derived from the value is stored, Terraform cannot tell that
# `value_wo` changed. `value_wo_version` is the rotation counter: increment it
# every time the value changes, or the new value is never sent. There is no
# update route for this resource at all, so *any* change here -- including the
# version bump on its own -- destroys and recreates the variable.
resource "circleci_project_environment_variable" "deploy_token" {
  project_slug     = "github/my-org/my-repo"
  name             = "DEPLOY_TOKEN"
  value_wo         = var.deploy_token
  value_wo_version = 1 # bump to 2 to send a new value
}
