# Manage the complete membership of a group. Users not listed here are removed
# from the group on the next apply.
resource "circleci_group" "developers" {
  org_id      = "00000000-0000-0000-0000-000000000000"
  name        = "developers"
  description = "Engineers who deploy to staging"
}

resource "circleci_group_membership" "developers" {
  org_id   = "00000000-0000-0000-0000-000000000000"
  group_id = circleci_group.developers.id

  # Users are addressed by UUID. A login or email address is not accepted.
  user_ids = [
    "11111111-1111-1111-1111-111111111111",
    "22222222-2222-2222-2222-222222222222",
  ]
}

# An empty set empties the group.
resource "circleci_group_membership" "contractors" {
  org_id   = "00000000-0000-0000-0000-000000000000"
  group_id = "33333333-3333-3333-3333-333333333333"
  user_ids = []
}
