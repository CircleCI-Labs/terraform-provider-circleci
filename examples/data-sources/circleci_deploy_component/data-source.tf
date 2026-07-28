# Fetches one deploy/release component by id, including every version
# published for it. Available on CircleCI Cloud only.
data "circleci_deploy_component" "release_agent" {
  id = "00000000-0000-0000-0000-000000000000"
}

# The version currently live in each environment.
output "circleci_release_agent_live_versions" {
  value = {
    for v in data.circleci_deploy_component.release_agent.versions : v.environment_id => v.name
    if v.is_live
  }
}
