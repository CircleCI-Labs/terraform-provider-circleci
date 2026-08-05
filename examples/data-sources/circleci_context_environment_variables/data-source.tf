data "circleci_organization" "acme" {
  slug = "gh/acme"
}

data "circleci_context" "deploy" {
  name   = "production-deploy"
  org_id = data.circleci_organization.acme.id
}

# Lists every environment variable on the context, following pagination
# internally. Available on both CircleCI Cloud and CircleCI Server.
#
# The API never discloses a value, so this is for discovery before import: find
# out what a context already holds so each variable can be brought under
# management with circleci_context_environment_variable, rather than guessing at
# names or recreating them from scratch.
data "circleci_context_environment_variables" "deploy" {
  context_id = data.circleci_context.deploy.id
}

output "circleci_deploy_context_variable_names" {
  value = [for variable in data.circleci_context_environment_variables.deploy.environment_variables : variable.name]
}

# Fail the plan when a variable the deploy pipeline depends on has not been set
# on the context yet.
check "required_context_variables_exist" {
  assert {
    condition = length(setsubtract(
      toset(["REGISTRY_TOKEN", "DEPLOY_KEY"]),
      toset([for variable in data.circleci_context_environment_variables.deploy.environment_variables : variable.name]),
    )) == 0
    error_message = "The deploy context is missing an environment variable the deploy job needs."
  }
}
