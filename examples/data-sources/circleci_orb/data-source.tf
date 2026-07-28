# Look up a single orb by its fully qualified name.
data "circleci_orb" "node" {
  full_name = "circleci/node"
}

output "latest_node_orb" {
  value = "${data.circleci_orb.node.full_name}@${data.circleci_orb.node.latest_version}"
}

# Or by UUID.
data "circleci_orb" "by_id" {
  id = "33333333-3333-3333-3333-333333333333"
}
