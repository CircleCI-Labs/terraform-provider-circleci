# Lists every context in the organization, following pagination internally.
# Available on both CircleCI Cloud and CircleCI Server.
data "circleci_contexts" "all" {
  organization_id = "00000000-0000-0000-0000-000000000000"
}

output "circleci_context_names" {
  value = [for context in data.circleci_contexts.all.contexts : context.name]
}

# The API addresses contexts by id, so look an id up by name when needed.
output "circleci_deploy_context_id" {
  value = one([
    for context in data.circleci_contexts.all.contexts : context.id
    if context.name == "deploy"
  ])
}
