# The resource class the token grants access to. Referencing it by expression
# rather than repeating its name as a literal is what makes Terraform create the
# class first — a token issued for a class that does not exist is rejected.
#
# The class name is always "<namespace>/<class>", and the namespace half is
# created by circleci_orb_namespace (an organization owns exactly one).
resource "circleci_runner_resource_class" "builders" {
  org_id         = "00000000-0000-0000-0000-000000000000" # the organization's UUID; the runner API accepts a UUID only, never a slug
  resource_class = "acme/builders"
  description    = "Self-hosted Linux build runners"
}

resource "circleci_runner_token" "agent" {
  org_id         = circleci_runner_resource_class.builders.org_id
  resource_class = circleci_runner_resource_class.builders.resource_class
  nickname       = "linux-agent-01"
}

# The API returns the token value exactly once, at creation, and never again — so
# this resource keeps it in state, and a fresh `terraform apply` cannot recover
# it. Capture it here (or write it straight to a secrets manager) on the apply
# that creates it.
output "runner_token" {
  value     = circleci_runner_token.agent.token
  sensitive = true
}

output "runner_token_created_at" {
  value = circleci_runner_token.agent.created_at
}

# Rotating a token means two tokens co-existing, not one token changing. Every
# configurable attribute here forces replacement, so editing `nickname` in place
# revokes the live credential and issues a new one in a single apply — agents
# still holding the old one stop authenticating. Add the replacement, move the
# agents onto it, then remove the old resource.
resource "circleci_runner_token" "agent_rotated" {
  org_id         = circleci_runner_resource_class.builders.org_id
  resource_class = circleci_runner_resource_class.builders.resource_class
  nickname       = "linux-agent-01-rotated"
}

# Use the circleci_ephemeral_runner_token ephemeral resource instead of this
# resource when the token is consumed inside the same `terraform apply` that
# creates it: it never writes the credential to a state file and deletes the
# token when the run ends. This managed resource is the right choice when the
# token has to outlive the run — for example when an agent provisioned entirely
# outside Terraform will be handed it later.
