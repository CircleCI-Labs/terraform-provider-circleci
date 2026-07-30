data "circleci_organization" "acme" {
  slug = "gh/acme"
}

# Referencing the context by expression rather than pasting its UUID is what makes
# Terraform create it first, and delete its variables before it on destroy.
resource "circleci_context" "deploy" {
  org_id = data.circleci_organization.acme.id
  name   = "production-deploy"
}

# `value` is recorded in Terraform state in cleartext. The API never returns a
# context environment variable's value on any route, so this attribute only ever
# reflects configuration or state — a refresh never overwrites it.
resource "circleci_context_environment_variable" "registry_host" {
  context_id = circleci_context.deploy.id
  name       = "REGISTRY_HOST"
  value      = "registry.internal.acme.example"
}

# `value_wo` is the same argument as a write-only argument (Terraform 1.11 or
# later): it is sent to CircleCI and then discarded, so it lands in neither state
# nor the plan file. Set exactly one of `value` and `value_wo`.
variable "registry_token" {
  description = "Registry password, supplied out of band — TF_VAR_registry_token, or a -var-file."
  type        = string
  sensitive   = true
  ephemeral   = true
}

# Because nothing derived from the value is stored, Terraform cannot tell that
# `value_wo` changed. `value_wo_version` is the rotation counter: increment it every
# time the value changes, or the new value is never sent. The provider rejects a
# `value_wo` with no version, since such a variable could be created but never
# rotated.
resource "circleci_context_environment_variable" "registry_token" {
  context_id       = circleci_context.deploy.id
  name             = "REGISTRY_TOKEN"
  value_wo         = var.registry_token
  value_wo_version = 1 # bump to 2 to send a new value
}

# A change made outside Terraform is detected by comparing timestamps, since the
# values cannot be compared: `updated_at` is what CircleCI reported the last time
# Terraform wrote the variable, `remote_updated_at` what it reports now. When the
# second is later than the first, something else wrote it, and the next apply
# re-asserts the configured value.
output "circleci_registry_token_drifted" {
  value = (
    circleci_context_environment_variable.registry_token.remote_updated_at >
    circleci_context_environment_variable.registry_token.updated_at
  )
}
