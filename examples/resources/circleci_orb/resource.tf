resource "circleci_orb_namespace" "example" {
  name   = "acme"
  org_id = "00000000-0000-0000-0000-000000000000"
}

# An orb is a named container in a namespace. Creating it publishes no source;
# use circleci_orb_version for that.
resource "circleci_orb" "example" {
  namespace_id = circleci_orb_namespace.example.id
  name         = "node"
}

# A private orb is visible only inside the owning organization.
resource "circleci_orb" "internal" {
  namespace_id = circleci_orb_namespace.example.id
  name         = "internal-tools"
  is_private   = true
}

# Category ids come from the circleci_orb_categories data source.
data "circleci_orb_categories" "all" {}

resource "circleci_orb" "categorised" {
  namespace_id = circleci_orb_namespace.example.id
  name         = "deploy"
  is_listed    = true

  category_ids = [
    data.circleci_orb_categories.all.ids_by_name["Build"],
    data.circleci_orb_categories.all.ids_by_name["Notifications"],
  ]
}
