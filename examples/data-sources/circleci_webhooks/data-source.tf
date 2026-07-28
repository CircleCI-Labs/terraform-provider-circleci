# Lists every outbound webhook on a project, following pagination internally.
# Available on both CircleCI Cloud and CircleCI Server.
data "circleci_webhooks" "project" {
  project_id = "00000000-0000-0000-0000-000000000000"
}

output "circleci_webhook_urls" {
  value = [for webhook in data.circleci_webhooks.project.webhooks : webhook.url]
}

# Webhooks that would deliver payloads without verifying the receiver's
# certificate, or without signing the payload at all. The signing secret itself
# is masked by the API, so only its presence is reported.
output "circleci_insecure_webhooks" {
  value = [
    for webhook in data.circleci_webhooks.project.webhooks : webhook.name
    if !webhook.verify_tls || !webhook.has_signing_secret
  ]
}
