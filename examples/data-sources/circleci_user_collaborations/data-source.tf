# Every organization the configured token can reach, with the UUID and slug of
# each. Nearly every CircleCI resource is keyed by organization_id, a UUID that is
# otherwise only visible in the web UI, so this is the way to discover one from
# Terraform.
data "circleci_user_collaborations" "mine" {}

# Standalone organizations only, and only the ones CircleCI already knows about: an
# organization that exists on the VCS but has never been used on CircleCI reports a
# null id and cannot be referenced by other resources yet.
locals {
  standalone_orgs = {
    for c in data.circleci_user_collaborations.mine.collaborations :
    c.name => c.id
    if c.vcs_type == "circleci" && c.id != null
  }
}

output "standalone_organization_ids" {
  value = local.standalone_orgs
}

# Look one up by name and use its UUID directly.
data "circleci_organization_settings" "acme" {
  organization_id = local.standalone_orgs["acme"]
}

# The slug is what the Insights data sources take, and it is not the same shape as
# a project slug.
locals {
  org_slugs = {
    for c in data.circleci_user_collaborations.mine.collaborations :
    c.name => c.slug
  }
}

data "circleci_insights_summary" "acme" {
  organization_slug = local.org_slugs["acme"]
}
