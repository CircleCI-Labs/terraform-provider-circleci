data "circleci_budgets" "all" {
  org_id = "00000000-0000-0000-0000-000000000000"
}

# The organization-level budget is the entry with a null project_id, if the
# organization has one configured.
output "org_level_budget" {
  value = [for b in data.circleci_budgets.all.budgets : b if b.project_id == null]
}

output "project_budget_count" {
  value = length([for b in data.circleci_budgets.all.budgets : b if b.project_id != null])
}
