# The resource class the token authenticates against. Referencing it by
# expression rather than repeating its name orders the two correctly: a token for
# a class that does not exist yet is rejected.
#
# The class name is always "<namespace>/<class>", and the namespace half is only
# creatable through circleci_orb_namespace.
resource "circleci_runner_resource_class" "builders" {
  org_id         = "00000000-0000-0000-0000-000000000000" # the organization's UUID; the runner API accepts a UUID only, never a slug
  resource_class = "acme/builders"
  description    = "Self-hosted Linux build runners"
}

# Unlike the circleci_runner_token resource, the token value here never reaches a
# state file: Open creates it and Close deletes it again when the run ends. That
# makes this the right shape when something inside this same apply consumes the
# token.
ephemeral "circleci_ephemeral_runner_token" "bootstrap" {
  org_id         = circleci_runner_resource_class.builders.org_id
  resource_class = circleci_runner_resource_class.builders.resource_class
  nickname       = "bootstrap"
}

# Hand the token straight to whatever registers the runner agent. An ephemeral
# value may be used inside a provisioner, which Terraform does not persist, but
# not in a resource argument that would be written to state.
resource "terraform_data" "runner_agent" {
  provisioner "local-exec" {
    command = "install-runner-agent --token=${ephemeral.circleci_ephemeral_runner_token.bootstrap.token}"
  }
}
