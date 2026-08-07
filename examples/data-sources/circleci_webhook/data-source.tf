# The API never discloses a webhook's signing secret, so `signing_secret` here is
# always null -- use circleci_webhooks' `has_signing_secret` to check only whether
# one is configured.
data "circleci_webhook" "example" {
  id = "00000000-0000-0000-0000-000000000000"
}

output "circleci_webhook_events" {
  value = data.circleci_webhook.example.events
}
