# Find the Slack integration installed through the CircleCI web UI's OAuth
# flow, then manage whether it is active or disabled.
data "circleci_notification_integrations" "org" {
  org_id = "00000000-0000-0000-0000-000000000000"
}

resource "circleci_notification_integration_status" "slack" {
  id     = data.circleci_notification_integrations.org.integrations[0].id
  status = "active"
}
