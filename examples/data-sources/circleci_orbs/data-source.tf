# Every orb in one namespace.
data "circleci_orbs" "ours" {
  namespace_id = "11111111-1111-1111-1111-111111111111"
}

# Private orbs are not returned unless asked for.
data "circleci_orbs" "private" {
  namespace_id = "11111111-1111-1111-1111-111111111111"
  visibility   = "private"
}

# Only the CircleCI-certified orbs.
data "circleci_orbs" "certified" {
  certified = true
}

output "our_orb_names" {
  value = [for orb in data.circleci_orbs.ours.orbs : orb.full_name]
}
