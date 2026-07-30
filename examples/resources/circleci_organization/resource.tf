# vcs_type = "github" (or "bitbucket") ADOPTS an organization that already exists
# on the VCS. The API only verifies that you are an admin and synchronizes
# CircleCI's record of it; nothing is created on GitHub or Bitbucket. Because
# create only ever adopted it, `terraform destroy` releases it from state with a
# warning instead of deleting it — the API's delete would take the organization,
# every project in it and all of their build history with it.
resource "circleci_organization" "acme" {
  name     = "acme" # must match the organization's existing name on the VCS
  vcs_type = "github"
}

# vcs_type = "circleci" genuinely creates a new standalone organization, with a
# slug of the form "circleci/<uuid>". This one IS deleted by `terraform destroy`.
# Creating it is not idempotent: a duplicate name returns HTTP 409, so
# re-applying after losing state fails rather than re-adopting.
resource "circleci_organization" "sandbox" {
  name     = "acme-sandbox"
  vcs_type = "circleci"
}

# Almost every other resource in this provider is keyed by organization UUID, so
# `id` is what the rest of a configuration consumes.
resource "circleci_context" "sandbox_ci" {
  org_id = circleci_organization.sandbox.id
  name   = "sandbox-ci"
}

# A standalone ("circleci" type) organization is also the only kind that can use
# CircleCI RBAC groups and the GitHub App data sources.
resource "circleci_group" "sandbox_admins" {
  org_id      = circleci_organization.sandbox.id
  name        = "sandbox-admins"
  description = "Engineers who administer the sandbox organization"
}

# `slug` is computed: "gh/acme" for the adopted organization above, and
# "circleci/<uuid>" for the standalone one.
output "organization_slugs" {
  value = {
    acme    = circleci_organization.acme.slug
    sandbox = circleci_organization.sandbox.slug
  }
}

# If all you need is an existing organization's UUID, use the data source
# instead. Adopting an organization as a *resource* puts it in your state file
# for no benefit.
data "circleci_organization" "other" {
  slug = "gh/other-org"
}
