# Look up a namespace this configuration does not manage, to get the
# namespace_id a circleci_orb needs.
data "circleci_orb_namespace" "example" {
  name = "circleci"
}

# Either key works; set exactly one.
data "circleci_orb_namespace" "by_id" {
  id = "11111111-1111-1111-1111-111111111111"
}
