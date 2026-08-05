# The organization-level budget: omit project_id entirely. Applying this a
# second time with a different credits value updates the existing budget in
# place rather than creating a duplicate.
resource "circleci_budget" "org" {
  org_id  = "00000000-0000-0000-0000-000000000000"
  credits = 2000000
}

# A per-project budget: set project_id to scope it to one project instead of
# the whole organization.
resource "circleci_budget" "checkout_service" {
  org_id     = "00000000-0000-0000-0000-000000000000"
  project_id = "11111111-1111-1111-1111-111111111111"
  credits    = 50000
}

# enforcement_type, consumption, percentage and threshold_exceeded are all
# read-only: CircleCI reports them, but this resource cannot set or reset
# them. See the resource's own warning about enforcement_type specifically.
output "checkout_service_budget_enforcement" {
  value = circleci_budget.checkout_service.enforcement_type
}

output "checkout_service_budget_usage" {
  value = "${circleci_budget.checkout_service.consumption} of ${circleci_budget.checkout_service.credits} credits (${circleci_budget.checkout_service.percentage}%)"
}
