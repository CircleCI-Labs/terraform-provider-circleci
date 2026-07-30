data "circleci_organization" "acme" {
  slug = "gh/acme"
}

# Turn on config policy evaluation for the organization. Every pipeline's
# configuration is then checked against the bundle before it runs.
resource "circleci_config_policy_settings" "config" {
  owner_id = data.circleci_organization.acme.id
  enabled  = true

  # Never enable enforcement before the policies exist: with an empty bundle
  # nothing is blocked, but the ordering matters when both are created at once.
  depends_on = [circleci_config_policy_bundle.config]
}

# Rego is normally read from disk with `file()`, which keeps it reviewable and
# testable — see the circleci_config_policy_bundle page. Inline is fine for one
# short policy.
resource "circleci_config_policy_bundle" "config" {
  owner_id = data.circleci_organization.acme.id

  policies = {
    "allow-docker.rego" = <<-EOT
      package org

      policy_name["allow_docker"]

      enable_rule["deny_unapproved_images"]

      hard_fail["deny_unapproved_images"]

      deny_unapproved_images[reason] {
        some job_name
        image := input.jobs[job_name].docker[_].image
        not startswith(image, "cimg/")
        reason := sprintf("job %q uses unapproved docker image %q", [job_name, image])
      }
    EOT
  }
}

# `policy_context` is optional and defaults to "config". Setting it explicitly is
# fine, but "config" is the only accepted value: CircleCI documents a "custom"
# policy context and every the API route rejects it with a 400. A policy
# context is also not a CircleCI context — it has nothing to do with
# circleci_context or the environment variables that live there.
#
# So there is one settings record per organization, and enforcement is switched on
# or off per organization. This one is off, which is the state to leave a bundle in
# while its policies are still being reviewed.
data "circleci_organization" "sandbox" {
  slug = "gh/acme-sandbox"
}

resource "circleci_config_policy_settings" "sandbox" {
  owner_id       = data.circleci_organization.sandbox.id
  policy_context = "config"
  enabled        = false
}
