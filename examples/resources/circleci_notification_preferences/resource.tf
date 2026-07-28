# Manage a couple of the calling user's own notification preferences by id.
# Any preference id not listed in `updates` is left exactly as CircleCI has
# it. Discover ids from `preferences` in state after a first apply that sets
# no toggles, e.g. `terraform apply` with an empty `updates = {}`, then
# `terraform state show circleci_notification_preferences.mine`.
resource "circleci_notification_preferences" "mine" {
  scope = "user"

  updates = {
    "33333333-3333-3333-3333-333333333333" = false
    "44444444-4444-4444-4444-444444444444" = true
  }
}

# The same, scoped to a project instead of the calling user.
resource "circleci_notification_preferences" "project" {
  scope      = "project"
  project_id = "11111111-1111-1111-1111-111111111111"
  org_id     = "00000000-0000-0000-0000-000000000000"

  updates = {
    "55555555-5555-5555-5555-555555555555" = false
  }
}

# The full preference catalog, including rows this configuration does not
# manage, so their ids can be found for a future `updates` entry.
output "my_preference_catalog" {
  value = circleci_notification_preferences.mine.preferences
}
