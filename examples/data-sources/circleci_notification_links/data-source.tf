# Lists the Slack accounts the calling token's own user has connected, so
# CircleCI can send them personal notifications.
#
# You cannot read another user's links: user_id accepts only "me" (the default)
# or the calling user's own UUID. Anything else is refused with HTTP 403.
data "circleci_notification_links" "me" {
  connection_type = "slack"
}

output "circleci_linked_slack_workspaces" {
  value = distinct([
    for link in data.circleci_notification_links.me.links :
    link.external_scope_id
  ])
}
