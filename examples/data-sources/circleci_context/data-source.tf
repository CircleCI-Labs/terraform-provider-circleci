data "circleci_organization" "acme" {
  slug = "gh/acme"
}

# Look a context up by name. The API has no lookup-by-name route, so the provider
# lists the organization's contexts and matches exactly — which is why a lookup by
# name also needs the organization, as `org_id` (or the deprecated
# `organization_id`). Names are case-sensitive and unique within an organization.
data "circleci_context" "deploy" {
  name   = "production-deploy"
  org_id = data.circleci_organization.acme.id
}

# Or look one up by id, when the UUID is what you have. Set exactly one of `id` and
# `name`; the organization is neither needed nor consulted in this form.
data "circleci_context" "by_id" {
  id = "00000000-0000-0000-0000-000000000000" # illustrative: a context UUID
}

# `restrictions` is the reason to read a context rather than list them: it reports
# which projects, groups or expressions may use it. A freshly created context
# carries one `group` restriction named "All members" whose value equals the
# organization's own UUID — the *permissive* default, meaning every organization
# member may use the context. An EMPTY list is the opposite: it means every group
# grant has been removed, which per CircleCI's documentation restricts the context
# to organization administrators only.
output "circleci_deploy_context" {
  value = {
    id         = data.circleci_context.deploy.id
    created_at = data.circleci_context.deploy.created_at

    permitted_projects = [
      for restriction in data.circleci_context.deploy.restrictions :
      restriction.project_id
      if restriction.type == "project"
    ]
  }
}

# Is the context open to every organization member? Look for a `group` entry
# whose value equals the organization's own UUID; do not use the list's length
# for this — an empty list means the opposite (administrators only).
output "circleci_context_is_open_to_all_members" {
  value = anytrue([
    for restriction in data.circleci_context.deploy.restrictions :
    restriction.type == "group" && restriction.value == data.circleci_organization.acme.id
  ])
}
