data "circleci_organization" "acme" {
  slug = "gh/acme"
}

# One resource owns the whole bundle: every policy for the organization's config
# policy context lives in this map. Reading the Rego from files keeps it
# reviewable and testable with `circleci policy test ./policies`.
resource "circleci_config_policy_bundle" "config" {
  owner_id = data.circleci_organization.acme.id

  policies = {
    "allow-docker.rego"     = file("${path.module}/policies/allow-docker.rego")
    "require-approval.rego" = file("${path.module}/policies/require-approval.rego")
  }
}

# Inline Rego works too, for a single short policy.
#
# Note this is a *different* organization. There is exactly one bundle per
# organization, because `policy_context` accepts only "config" — a second
# circleci_config_policy_bundle for the same owner_id would delete this one's
# policies on every apply, and the other would put them back on the next.
data "circleci_organization" "sandbox" {
  slug = "gh/acme-sandbox"
}

resource "circleci_config_policy_bundle" "sandbox" {
  owner_id = data.circleci_organization.sandbox.id

  policies = {
    "deny-all.rego" = <<-EOT
      package org

      policy_name["deny_all"]

      enable_rule["deny_all"]

      hard_fail["deny_all"]

      deny_all["the sandbox organization does not run pipelines"]
    EOT
  }
}

# Uploading policies does not enforce them; this is the switch that does.
resource "circleci_config_policy_settings" "config" {
  owner_id = data.circleci_organization.acme.id
  enabled  = true

  # Enable enforcement only once the bundle is in place.
  depends_on = [circleci_config_policy_bundle.config]
}
