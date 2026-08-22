# Each value has a plan-enforced minimum and maximum, reported after the first
# apply or refresh as the matching cache/workspace/artifact_retention_days_min
# and _max attributes. Configuring a value outside that range fails the apply
# outright (CircleCI rejects it; it does not clamp to the nearest bound), with
# a message that does not say which field was the problem.
resource "circleci_storage_retention" "example" {
  org_id = "00000000-0000-0000-0000-000000000000"

  cache_retention_days     = 15
  workspace_retention_days = 15
  artifact_retention_days  = 30
}
