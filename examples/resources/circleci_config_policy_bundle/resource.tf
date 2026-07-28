# One resource owns the whole bundle: every policy for the organization's config
# policy context lives in this map. Reading the Rego from files keeps it
# reviewable and testable with `circleci policy test`.
resource "circleci_config_policy_bundle" "config" {
  owner_id = "00000000-0000-0000-0000-000000000000"

  policies = {
    "allow-docker.rego"     = file("${path.module}/policies/allow-docker.rego")
    "require-approval.rego" = file("${path.module}/policies/require-approval.rego")
  }
}

# Inline Rego works too, for a single short policy.
resource "circleci_config_policy_bundle" "custom" {
  owner_id       = "00000000-0000-0000-0000-000000000000"
  policy_context = "custom"

  policies = {
    "deny-all.rego" = <<-EOT
      package custom

      policy_name["deny_all"]

      enable_rule["deny_all"]

      deny_all = "nothing is permitted in this context"
    EOT
  }
}

# Uploading policies does not enforce them; this is the switch that does.
resource "circleci_config_policy_settings" "config" {
  owner_id = "00000000-0000-0000-0000-000000000000"
  enabled  = true

  # Enable enforcement only once the bundle is in place.
  depends_on = [circleci_config_policy_bundle.config]
}
