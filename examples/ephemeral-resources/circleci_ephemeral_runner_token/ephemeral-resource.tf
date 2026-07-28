ephemeral "circleci_ephemeral_runner_token" "example" {
  organization_id = "00000000-0000-0000-0000-000000000000"
  resource_class  = "my-namespace/my-runner"
  nickname        = "bootstrap"
}

# The token value never touches state: it is created here and deleted again
# when this apply finishes. Hand it to whatever bootstraps the runner in the
# same run — for example a provisioner on a compute resource that installs
# the runner agent.
resource "null_resource" "runner_agent" {
  provisioner "local-exec" {
    command = "install-runner-agent --token=${ephemeral.circleci_ephemeral_runner_token.example.token}"
  }
}
