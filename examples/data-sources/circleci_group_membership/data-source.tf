# Read the users that are members of a group.
data "circleci_group_membership" "developers" {
  organization_id = "00000000-0000-0000-0000-000000000000"
  group_id        = "11111111-1111-1111-1111-111111111111"
}

# user_ids is the shape circleci_group_membership consumes, so an existing group's
# membership can be copied to another group.
resource "circleci_group_membership" "mirror" {
  organization_id = "00000000-0000-0000-0000-000000000000"
  group_id        = "22222222-2222-2222-2222-222222222222"
  user_ids        = data.circleci_group_membership.developers.user_ids
}

# members carries the display fields the API returns alongside each id.
output "developer_emails" {
  value = [for member in data.circleci_group_membership.developers.members : member.email]
}
