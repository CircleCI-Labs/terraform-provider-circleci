# A user-scoped email channel config. scope = "user" always belongs to
# whoever the provider's API token authenticates as.
resource "circleci_notification_channel_config" "personal_email" {
  scope        = "user"
  channel_type = "email"
  target       = "me@example.com"
  is_enabled   = true
  org_id       = "00000000-0000-0000-0000-000000000000"
}

# A project-scoped Slack channel config. This requires an active Slack
# integration for the organization (see circleci_notification_integrations),
# and `target` must be a channel ID the installed app can already post to.
resource "circleci_notification_channel_config" "build_alerts" {
  scope        = "project"
  channel_type = "slack"
  target       = "C0123456789"
  is_enabled   = true
  project_id   = "11111111-1111-1111-1111-111111111111"
  org_id       = "00000000-0000-0000-0000-000000000000"
}

# CircleCI resolves the human-readable channel name for a project-scoped Slack
# config.
output "build_alerts_channel_name" {
  value = circleci_notification_channel_config.build_alerts.channel_name
}
