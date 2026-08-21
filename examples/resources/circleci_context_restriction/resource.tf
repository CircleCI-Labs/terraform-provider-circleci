# The context whose access these restrictions narrow. Referencing the context by
# expression rather than pasting its UUID is what makes Terraform create it
# first, and delete the restrictions before it on destroy — a restriction cannot
# outlive its context.
resource "circleci_context" "deploy" {
  org_id = "00000000-0000-0000-0000-000000000000" # the organization's UUID, from Organization Settings in the CircleCI web app
  name   = "production-deploy"
}

# For type = "project", `value` is the project's UUID. The circleci_project data
# source resolves that from the "vcs-type/org-name/repo-name" slug, which is what
# you actually have to hand.
data "circleci_project" "api" {
  slug = "github/acme/api"
}

data "circleci_project" "web" {
  slug = "github/acme/web"
}

# One restriction per permitted project. CircleCI also creates a `group`
# restriction named "All members" on every context as part of creating it (see
# the resource description above) — that is a separate, members axis, not a
# projects axis. Per CircleCI's documentation and support the two combine as
# an AND, so leaving "All members" in place alongside the project restrictions
# below means "any member of the organization, but only from these projects" —
# check with circleci_context_restrictions to see both. Do NOT delete "All
# members" to try to make the project restrictions "take effect": they already
# do. Removing every `group` restriction instead narrows the context to
# organization administrators only and breaks scheduled workflows and
# bot-triggered pipelines (e.g. Renovate), which hold no group membership.
#
# All three configurable attributes (context_id, type, value) force replacement
# when changed, because the API has no update route for a restriction.
resource "circleci_context_restriction" "deploy" {
  for_each = {
    api = data.circleci_project.api.id
    web = data.circleci_project.web.id
  }

  context_id = circleci_context.deploy.id
  type       = "project"
  value      = each.value
}

# `name` (the project slug, for a project restriction) is learned by CircleCI out
# of band: the create response never carries it, so it reads back as empty
# immediately after apply and only picks up its real value on a later refresh.
# Read it through the plural data source rather than expecting it straight from
# the resource.
data "circleci_context_restrictions" "deploy" {
  context_id = circleci_context.deploy.id

  depends_on = [circleci_context_restriction.deploy]
}

output "permitted_projects" {
  value = [
    for restriction in data.circleci_context_restrictions.deploy.restrictions :
    restriction.project_id
    if restriction.type == "project"
  ]
}

# The other two restriction types take their own kind of `value`, and are
# deliberately not shown with invented values:
#
#   type = "group"      `value` must be the restricted context's own
#                       organization UUID, and only succeeds against an
#                       OAuth-backed (classic gh/<org> or bitbucket/<org>)
#                       organization — it does not reach a VCS team or a
#                       circleci_group RBAC group, despite the name. See the
#                       resource description above.
#
#   type = "expression" `value` is a CircleCI restriction expression. Its
#                       grammar is checked, but the fields it names are not —
#                       a typo'd field name is accepted and creates a
#                       restriction that may not guard what you intended. Copy
#                       a working expression out of a restriction created in
#                       the web application instead of guessing at one, and
#                       verify its effect.
