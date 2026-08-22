# A context belongs to exactly one organization, and the API keys it by the
# organization's UUID. Resolving that UUID from the slug you already have keeps the
# literal out of the configuration — it is otherwise only visible in the CircleCI
# web application, under Organization Settings.
data "circleci_organization" "acme" {
  slug = "gh/acme"
}

resource "circleci_context" "example" {
  org_id = data.circleci_organization.acme.id
  name   = "production-deploy"
}

# `organization_id` is the deprecated spelling of `org_id`; set exactly one of the
# two, since a configuration that sets both is rejected. Changing whichever one you
# set replaces the context, because no API route moves a context between
# organizations — and so does changing `name`.

# A context is NOT empty when created: CircleCI adds a `group` restriction
# named "All members" (permissive default) as part of creating it, so every
# member of the organization may use the context from the start. Add
# circleci_context_restriction with type = "project" to limit which projects
# may use it; deleting "All members" instead of adding a project restriction
# locks the context down to organization administrators only.
output "circleci_context_id" {
  value = circleci_context.example.id
}

output "circleci_context_created_at" {
  value = circleci_context.example.created_at
}
