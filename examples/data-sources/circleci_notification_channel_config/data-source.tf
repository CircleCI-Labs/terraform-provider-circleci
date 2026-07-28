# Look up a channel config by id, for one this configuration does not manage.
data "circleci_notification_channel_config" "example" {
  id = "22222222-2222-2222-2222-222222222222"
}

output "channel_config_target" {
  value = data.circleci_notification_channel_config.example.target
}
