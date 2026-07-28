# Turn on config policy evaluation for the organization. Every pipeline's
# configuration is then checked against the bundle before it runs.
resource "circleci_config_policy_settings" "config" {
  owner_id = "00000000-0000-0000-0000-000000000000"
  enabled  = true

  # Never enable enforcement before the policies exist: with an empty bundle
  # nothing is blocked, but the ordering matters when both are created at once.
  depends_on = [circleci_config_policy_bundle.config]
}

resource "circleci_config_policy_bundle" "config" {
  owner_id = "00000000-0000-0000-0000-000000000000"

  policies = {
    "allow-docker.rego" = file("${path.module}/policies/allow-docker.rego")
  }
}

# The custom policy context has its own switch.
resource "circleci_config_policy_settings" "custom" {
  owner_id       = "00000000-0000-0000-0000-000000000000"
  policy_context = "custom"
  enabled        = false
}
