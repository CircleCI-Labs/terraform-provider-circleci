# Lists every group in the organization, following pagination internally.
# Available on both CircleCI Cloud and CircleCI Server.
data "circleci_groups" "all" {
  org_id = "00000000-0000-0000-0000-000000000000"
}

output "circleci_group_names" {
  value = [for group in data.circleci_groups.all.groups : group.name]
}

# The API addresses groups by id, so look an id up by name when needed.
output "circleci_developers_group_id" {
  value = one([
    for group in data.circleci_groups.all.groups : group.id
    if group.name == "developers"
  ])
}
