# Grant a group a role on a project, so that every member of the group holds that
# role on the project.
resource "circleci_group" "security" {
  organization_id = "00000000-0000-0000-0000-000000000000"
  name            = "security"
  description     = "Security reviewers"
}

resource "circleci_project_group" "security_reviews_api" {
  organization_id = "00000000-0000-0000-0000-000000000000"
  project_id      = "44444444-4444-4444-4444-444444444444"
  group_id        = circleci_group.security.id

  # One of project-admin, project-contributor or project-viewer. The
  # organization-level roles are not valid here. This can be changed in place.
  role = "project-viewer"
}

# Restrict a set of projects to one group, with the same role on each.
locals {
  restricted_project_ids = [
    "44444444-4444-4444-4444-444444444444",
    "55555555-5555-5555-5555-555555555555",
  ]
}

resource "circleci_project_group" "security_admin" {
  for_each = toset(local.restricted_project_ids)

  organization_id = "00000000-0000-0000-0000-000000000000"
  project_id      = each.value
  group_id        = circleci_group.security.id
  role            = "project-admin"
}
