# Lists every restriction on a context. An empty list means the context is
# unrestricted, so every project in the organization can use it.
# Available on both CircleCI Cloud and CircleCI Server.
data "circleci_context_restrictions" "deploy" {
  context_id = "00000000-0000-0000-0000-000000000000"
}

# Which projects may use the context. project_id is only set for `project`
# restrictions; use `value` for `group` and `expression` ones.
output "circleci_permitted_project_ids" {
  value = [
    for restriction in data.circleci_context_restrictions.deploy.restrictions : restriction.project_id
    if restriction.type == "project"
  ]
}

# Fail the plan if a context that should be locked down is not.
check "deploy_context_is_restricted" {
  assert {
    condition     = length(data.circleci_context_restrictions.deploy.restrictions) > 0
    error_message = "The deploy context is unrestricted, so every project in the organization can use it."
  }
}
