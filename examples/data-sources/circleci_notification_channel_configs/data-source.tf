# List every channel config for a project.
data "circleci_notification_channel_configs" "project" {
  scope      = "project"
  project_id = "11111111-1111-1111-1111-111111111111"
  org_id     = "00000000-0000-0000-0000-000000000000"
}

# List the calling user's own channel configs.
data "circleci_notification_channel_configs" "mine" {
  scope = "user"
}

output "project_channel_config_ids" {
  value = [for c in data.circleci_notification_channel_configs.project.channel_configs : c.id]
}
