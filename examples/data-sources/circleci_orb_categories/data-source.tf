data "circleci_orb_categories" "all" {}

# circleci_orb.category_ids takes ids, so ids_by_name turns a human-readable
# category name into the value to set.
resource "circleci_orb" "example" {
  namespace_id = "11111111-1111-1111-1111-111111111111"
  name         = "deploy"

  category_ids = [
    data.circleci_orb_categories.all.ids_by_name["Build"],
  ]
}

output "category_names" {
  value = [for category in data.circleci_orb_categories.all.categories : category.name]
}
