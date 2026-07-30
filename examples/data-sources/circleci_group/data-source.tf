# Group ids are unique only within an organization, so both ids are required.
# Available on both CircleCI Cloud and CircleCI Server.
data "circleci_group" "developers" {
  org_id = "00000000-0000-0000-0000-000000000000"
  id     = "11111111-1111-1111-1111-111111111111"
}

output "circleci_group_name" {
  value = data.circleci_group.developers.name
}

output "circleci_group_description" {
  value = data.circleci_group.developers.description
}
