# A resource class is always named "<namespace>/<class>", and the namespace half
# has to exist first. Namespaces are only creatable through
# circleci_orb_namespace — they are the orb registry's namespaces, reused to name
# runner pools — and an organization may own exactly one, claimed across all of
# CircleCI rather than just within the organization.
#
# circleci_orb_namespace is CircleCI Cloud only (the v3 API that serves it is not
# routed on Server). On CircleCI Server, create the namespace with the CircleCI
# CLI and drop this resource, keeping the rest of this example as-is.
resource "circleci_orb_namespace" "acme" {
  name   = "acme"
  org_id = "00000000-0000-0000-0000-000000000000" # the organization's UUID; the runner API accepts a UUID only, never a slug
}

# The pool itself. `resource_class` is the name a job's `resource_class:` key
# refers to in .circleci/config.yml, so building it from the namespace resource
# keeps the two halves from drifting apart.
resource "circleci_runner_resource_class" "builders" {
  org_id         = circleci_orb_namespace.acme.org_id
  resource_class = "${circleci_orb_namespace.acme.name}/builders"
  description    = "Self-hosted Linux build runners"

  # A resource class that still has tokens attached cannot be deleted unless this
  # is set. Leaving it false makes `terraform destroy` fail loudly rather than
  # silently revoking tokens that agents outside Terraform may still be using.
  force_delete = false
}

# Runner agents authenticate to the class with a token. Referencing the class by
# expression — rather than repeating the "acme/builders" string — is what orders
# the two: a token for a class that does not exist yet is rejected.
resource "circleci_runner_token" "builders" {
  org_id         = circleci_runner_resource_class.builders.org_id
  resource_class = circleci_runner_resource_class.builders.resource_class
  nickname       = "builders-agent-01"
}

# The token value is returned exactly once, at creation, so it lives in state
# from here on. Use the circleci_ephemeral_runner_token ephemeral resource
# instead when the token is consumed within the same `terraform apply`: it never
# writes the credential to state and deletes it when the run ends.
output "builders_token" {
  value     = circleci_runner_token.builders.token
  sensitive = true
}

# Resource classes work on both CircleCI Cloud and CircleCI Server, but the
# runner administration API lives on its own origin — https://runner.circleci.com
# on Cloud, your own installation on Server — so a Server configuration must set
# the provider's `runner_host`.
output "builders_resource_class_id" {
  value = circleci_runner_resource_class.builders.id
}
