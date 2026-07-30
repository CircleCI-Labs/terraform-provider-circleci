# Lists every deploy/release environment in the organization.
# Available on CircleCI Cloud only.
data "circleci_deploy_environments" "all" {
  org_id = "00000000-0000-0000-0000-000000000000"
}

output "circleci_deploy_environment_names" {
  value = [for env in data.circleci_deploy_environments.all.environments : env.name]
}

# Environments are addressed by id elsewhere (e.g. circleci_deploy_component's
# version list), so look one up by name when needed.
output "circleci_prod_environment_id" {
  value = one([
    for env in data.circleci_deploy_environments.all.environments : env.id
    if env.name == "prod"
  ])
}
