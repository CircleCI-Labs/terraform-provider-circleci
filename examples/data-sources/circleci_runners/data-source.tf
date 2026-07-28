# Every runner agent registered to one resource class.
data "circleci_runners" "linux" {
  resource_class = "my-namespace/my-runner"
}

output "runner_hostnames" {
  value = [for runner in data.circleci_runners.linux.runners : runner.hostname]
}

# Every runner agent in an organization, across all of its namespaces.
data "circleci_runners" "all" {
  organization_id = "00000000-0000-0000-0000-000000000000"
}

# Agents left behind on an older release, so they can be upgraded.
output "outdated_agents" {
  value = [
    for runner in data.circleci_runners.all.runners : runner.name
    if runner.version != "3.1.2"
  ]
}
