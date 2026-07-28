# Fetches one deploy/release environment by id.
# Available on CircleCI Cloud only.
data "circleci_deploy_environment" "prod" {
  id = "00000000-0000-0000-0000-000000000000"
}

output "circleci_prod_environment_labels" {
  value = data.circleci_deploy_environment.prod.labels
}
