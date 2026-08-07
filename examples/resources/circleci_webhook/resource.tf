# scope_id is the project's UUID, not its slug -- the same id
# data.circleci_project's `id` returns. scope_type is "project", the only value
# the API currently accepts.
resource "circleci_webhook" "example" {
  name           = "my-webhook"
  url            = "https://example.com/webhook"
  signing_secret = "my-signing-secret"
  scope_id       = "00000000-0000-0000-0000-000000000000"
  scope_type     = "project"
  events         = ["workflow-completed", "job-completed"]
  verify_tls     = true
}

# `signing_secret_wo` is a write-only alternative to `signing_secret`, available
# with Terraform 1.11 or later. Set exactly one of the two.
variable "webhook_signing_secret" {
  type      = string
  sensitive = true
}

resource "circleci_webhook" "write_only" {
  name = "my-webhook"
  url  = "https://example.com/webhook"

  signing_secret_wo         = var.webhook_signing_secret
  signing_secret_wo_version = 1 # bump to 2 to rotate the secret

  scope_id   = "00000000-0000-0000-0000-000000000000"
  scope_type = "project"
  events     = ["workflow-completed", "job-completed"]
}
