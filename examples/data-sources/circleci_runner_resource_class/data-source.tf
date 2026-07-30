# The runner API accepts an organization UUID only — never a slug — so resolve the
# UUID from the slug you actually know rather than pasting a literal.
data "circleci_organization" "acme" {
  slug = "gh/acme"
}

# Read one resource class by its "<namespace>/<class>" name. Useful for a class
# created outside Terraform, or one owned by a different configuration, so that
# neither has to declare it as a resource twice.
#
# The lookup lists the namespace and matches the full name client-side, and it
# reports an error — rather than an empty result — when no class of that name
# exists in the namespace.
data "circleci_runner_resource_class" "builders" {
  org_id         = data.circleci_organization.acme.id
  resource_class = "acme/builders"
}

# Issue a token against a class this configuration does not own, without copying
# its UUID anywhere.
resource "circleci_runner_token" "agent" {
  org_id         = data.circleci_organization.acme.id
  resource_class = data.circleci_runner_resource_class.builders.resource_class
  nickname       = "linux-agent-01"
}

# `id` is the class's own UUID, for modules and API calls that take an ID rather
# than a "<namespace>/<class>" name.
output "builders" {
  value = {
    id          = data.circleci_runner_resource_class.builders.id
    description = data.circleci_runner_resource_class.builders.description
  }
}

# Use circleci_runner_resource_classes (plural) instead when you want to discover
# what exists — every class in an organization, or every class in one namespace —
# rather than resolve a name you already know.
data "circleci_runner_resource_classes" "all" {
  org_id = data.circleci_organization.acme.id
}

output "all_resource_class_names" {
  value = [
    for class in data.circleci_runner_resource_classes.all.resource_classes :
    class.resource_class
  ]
}
