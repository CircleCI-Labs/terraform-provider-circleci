# Lists every deploy/release component in the organization.
# Available on CircleCI Cloud only.
data "circleci_deploy_components" "all" {
  organization_id = "00000000-0000-0000-0000-000000000000"
}

# Scope the listing to one CircleCI project.
data "circleci_deploy_components" "for_project" {
  organization_id = "00000000-0000-0000-0000-000000000000"
  project_id      = "11111111-1111-1111-1111-111111111111"
}

output "circleci_component_names" {
  value = [for c in data.circleci_deploy_components.all.components : c.name]
}
