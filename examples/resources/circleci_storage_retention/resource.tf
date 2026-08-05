# CircleCI clamps these to the organization's plan-enforced bounds rather than
# rejecting an out-of-range value, so the values actually stored may differ from
# what is configured here. The provider warns when that happens; the
# cache/workspace/artifact_retention_days_min and _max attributes report the
# current bounds after the first apply or refresh.
resource "circleci_storage_retention" "example" {
  org_id = "00000000-0000-0000-0000-000000000000"

  cache_retention_days     = 15
  workspace_retention_days = 15
  artifact_retention_days  = 30
}
