# The flaky tests CircleCI has recorded for a project. A flake is a test that both
# passed and failed at the same commit.
data "circleci_insights_flaky_tests" "api" {
  project_slug = "gh/acme/api"
}

# total_flaky_tests counts unique tests. The flaky_tests list holds one entry per
# flake instance, so a single test that flaked five times contributes five entries
# but one to the total — gate on the total, not on length().
check "flakiness_under_control" {
  assert {
    condition     = data.circleci_insights_flaky_tests.api.total_flaky_tests <= 5
    error_message = "The project has ${data.circleci_insights_flaky_tests.api.total_flaky_tests} flaky tests, over the budget of 5."
  }
}

# The worst offenders, deduplicated back to one entry per test.
output "flakiest_tests" {
  value = {
    for t in data.circleci_insights_flaky_tests.api.flaky_tests :
    "${t.classname}.${t.test_name}" => t.times_flaked...
  }
}

# Which jobs the flakes live in, to point an owner at.
output "flaky_jobs" {
  value = distinct([
    for t in data.circleci_insights_flaky_tests.api.flaky_tests : t.job_name
  ])
}

# time_wasted is null when CircleCI has not costed a flake, so it needs a default
# before being summed.
output "seconds_wasted_on_flakes" {
  value = sum(concat([0], [
    for t in data.circleci_insights_flaky_tests.api.flaky_tests :
    coalesce(t.time_wasted, 0)
  ]))
}
