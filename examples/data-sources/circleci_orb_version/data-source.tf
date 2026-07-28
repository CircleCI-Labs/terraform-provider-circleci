data "circleci_orb" "node" {
  full_name = "circleci/node"
}

# Resolve an exact version.
data "circleci_orb_version" "pinned" {
  orb_id  = data.circleci_orb.node.id
  version = "5.1.0"
}

# Or resolve an alias to whatever the newest version is.
data "circleci_orb_version" "newest" {
  orb_id  = data.circleci_orb.node.id
  version = "volatile"
}

# Or read one directly by id.
data "circleci_orb_version" "by_id" {
  id = "44444444-4444-4444-4444-444444444444"
}

output "published_source" {
  value = data.circleci_orb_version.pinned.source
}
