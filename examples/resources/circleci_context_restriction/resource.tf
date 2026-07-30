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

# One restriction per permitted project. An unrestricted context is usable by
# *every* project in the organization, so adding the first restriction is what
# actually locks the context down — not a separate setting.
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
#   type = "group"      `value` is a group id. CircleCI has two unrelated things
#                       called "group" — VCS security groups (GitHub OAuth
#                       organizations only) and CircleCI RBAC groups (see
#                       circleci_group; standalone organizations only) — and the
#                       API does not document which one this type expects. Verify
#                       against your own organization before relying on it.
#
#   type = "expression" `value` is a CircleCI restriction expression. The
#                       expression grammar is not published, so copy a working
#                       expression out of a restriction created in the web
#                       application instead of guessing at one.
