# Groups are available on both CircleCI Cloud and CircleCI Server.
#
# Group membership is managed in the CircleCI web UI: Terraform creates and
# deletes the group, but never adds or removes its users. The API has no update
# endpoint, so changing name or description recreates the group.
resource "circleci_group" "developers" {
  org_id      = "00000000-0000-0000-0000-000000000000"
  name        = "developers"
  description = "Engineers who deploy to staging"
}

output "circleci_group_id" {
  value = circleci_group.developers.id
}
