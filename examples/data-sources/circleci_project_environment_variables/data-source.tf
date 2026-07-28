# Lists every environment variable on a project, following pagination internally.
# Available on both CircleCI Cloud and CircleCI Server.
#
# Values are masked by the API: four "x" characters followed by the last four
# characters of the real value. Use this to discover which variables are set, not
# what they are set to.
data "circleci_project_environment_variables" "project" {
  project_slug = "circleci/AbCdEfG/HiJkLmN"
}

output "circleci_environment_variable_names" {
  value = [for variable in data.circleci_project_environment_variables.project.environment_variables : variable.name]
}

# Fail the plan when a variable the pipeline depends on has not been set.
check "required_environment_variables_exist" {
  assert {
    condition = length(setsubtract(
      toset(["DEPLOY_TARGET", "REGISTRY_HOST"]),
      toset([for variable in data.circleci_project_environment_variables.project.environment_variables : variable.name]),
    )) == 0
    error_message = "The project is missing an environment variable the deploy job needs."
  }
}
