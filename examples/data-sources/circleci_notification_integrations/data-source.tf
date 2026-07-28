# List every notification integration (Slack workspace installation, today)
# for one organization.
data "circleci_notification_integrations" "org" {
  org_id = "00000000-0000-0000-0000-000000000000"
}

# List every integration for every organization the calling user belongs to.
data "circleci_notification_integrations" "mine" {}

output "slack_integration_ids" {
  value = [for i in data.circleci_notification_integrations.org.integrations : i.id]
}
