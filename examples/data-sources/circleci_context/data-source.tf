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
# which projects, groups or expressions may use it. An empty list is the *permissive*
# case — an unrestricted context is usable by every project in the organization.
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

output "circleci_context_is_unrestricted" {
  value = length(data.circleci_context.deploy.restrictions) == 0
}
