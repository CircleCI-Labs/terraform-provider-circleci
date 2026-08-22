# Lists every restriction on a context. An empty list is the MOST restricted
# state, not the least: it means every group grant has been removed, which
# per CircleCI's documentation leaves the context usable by organization
# administrators only. A freshly created context instead carries one `group`
# restriction named "All members" on every context as part of
# creating it (see the circleci_context_restriction resource description) —
# the permissive default meaning every organization member may use the
# context. That is a members restriction ("which org members may use this
# context"), not a projects restriction — check for a `project` entry
# specifically (see the check block below) when the goal is confirming which
# projects may use the context. Available on both CircleCI Cloud and CircleCI
# Server.
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

# Fail the plan if the context has been accidentally locked down to
# organization administrators only (every group grant removed), or if it is
# not actually locked down to specific projects. Checking only
# length(...) > 0 does not confirm a project lockdown: CircleCI creates a
# `group` restriction named "All members" on every context as part of
# creating it (see the circleci_context_restriction resource description), so
# a context can carry exactly one restriction and still be usable by every
# member of the organization — because "All members" governs which members
# may use the context, not which projects. What actually confirms a project
# lockdown is the presence of a `project` restriction.
#
# Do NOT delete "All members" to try to make a project restriction "take
# effect": per CircleCI's documentation and support the two combine as an AND
# already (any member, but only from the listed projects), and removing every
# `group` restriction instead narrows the context to organization
# administrators only, breaking scheduled workflows and bot-triggered
# pipelines (e.g. Renovate), which hold no group membership.
check "deploy_context_is_restricted" {
  assert {
    condition     = length(data.circleci_context_restrictions.deploy.restrictions) > 0
    error_message = "The deploy context's restrictions list is empty, meaning every group grant has been removed; per CircleCI's documentation the context is now usable by organization administrators only."
  }

  assert {
    condition = length([
      for restriction in data.circleci_context_restrictions.deploy.restrictions :
      restriction if restriction.type == "project"
    ]) > 0
    error_message = "The deploy context has no `project` restriction, so it is not locked down to specific projects (the default \"All members\" `group` restriction governs membership, not projects, and does not provide this)."
  }
}
