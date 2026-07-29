# Fetches the resolved (compiled) and original configuration for a pipeline
# run. Available on both CircleCI Cloud and CircleCI Server.
data "circleci_pipeline_run" "this" {
  project_slug = "gh/CircleCI-Public/api-preview-docs"
  number       = 25
}

data "circleci_pipeline_run_config" "this" {
  pipeline_run_id = data.circleci_pipeline_run.this.id
}

# Assert the compiled config actually contains an expected orb, rather than
# trusting that the checked-in YAML alone reflects what ran.
check "orb_was_expanded" {
  assert {
    condition     = strcontains(data.circleci_pipeline_run_config.this.compiled, "circleci/node")
    error_message = "Compiled pipeline config no longer references the circleci/node orb."
  }
}
