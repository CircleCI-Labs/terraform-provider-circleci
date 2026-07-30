# With no id, this reports the user the configured token authenticates as. The
# usual reason to want it: confirming which account a CI token belongs to before
# letting a pipeline make changes with it.
data "circleci_user" "me" {}

output "authenticated_as" {
  value = data.circleci_user.me.login
}

variable "expected_login" {
  type        = string
  description = <<-EOT
    The VCS login the configured CIRCLE_TOKEN is expected to belong to, for example
    the CI service account. The check below fails the run if it belongs to someone
    else. Not a secret: it is the account's public username.
  EOT
}

# Guard against running with a token nobody expected.
check "expected_service_account" {
  assert {
    condition     = data.circleci_user.me.login == var.expected_login
    error_message = "The configured CIRCLE_TOKEN belongs to ${data.circleci_user.me.login}, not ${var.expected_login}."
  }
}

variable "actor_id" {
  type        = string
  description = <<-EOT
    UUID of the CircleCI user to look up. A pipeline reports the actor that started
    it under `trigger.actor`, and the circleci_pipeline_run data source surfaces it.
  EOT
}

# With an id, it looks that user up instead — for example the actor recorded on a
# pipeline.
data "circleci_user" "pipeline_actor" {
  id = var.actor_id
}

output "pipeline_actor_name" {
  value = data.circleci_user.pipeline_actor.name
}
