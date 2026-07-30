# Read the groups granted a role on a project.
data "circleci_project_groups" "api" {
  org_id     = "00000000-0000-0000-0000-000000000000"
  project_id = "44444444-4444-4444-4444-444444444444"
}

# Which groups can administer the project?
output "api_project_admins" {
  value = [
    for group in data.circleci_project_groups.api.groups :
    group.name if group.role == "project-admin"
  ]
}

output "api_project_group_roles" {
  value = { for group in data.circleci_project_groups.api.groups : group.name => group.role }
}
