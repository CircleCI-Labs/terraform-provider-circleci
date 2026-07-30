# A scheduled trigger is the current way to run a pipeline on a cron schedule, and
# the migration target for the legacy schedule API and for a community provider's
# `circleci_schedule`.
#
# The required attributes differ from a GitHub event source, because one endpoint
# covers several contracts: `event_name`, `checkout_ref`, `config_ref` and
# `event_source_schedule_cron_expression` are all required here, `event_preset` must
# be omitted, and `parameters` is supported for `schedule` only.
#
# `data.circleci_project.api` and `circleci_pipeline_definition.build` are declared
# in the primary example above.
resource "circleci_trigger" "nightly" {
  project_id             = data.circleci_project.api.id
  pipeline_definition_id = circleci_pipeline_definition.build.id

  event_source_provider = "schedule"
  event_name            = "nightly-build"
  checkout_ref          = "main"
  config_ref            = "main"

  # Five fields, evaluated in UTC. This one is 02:00 every day.
  event_source_schedule_cron_expression = "0 2 * * *"

  # "system" attributes the runs to CircleCI itself. "current" attributes them to
  # the user whose API token created the trigger, so the pipeline runs with that
  # user's permissions — and stops working when they leave the organization.
  event_source_schedule_attribution_actor = "system"

  # Pipeline parameters passed to every run. Clearing this map replaces the trigger:
  # the API has no way to remove parameters from an existing one.
  parameters = {
    run_integration_tests = "true"
    deploy_target         = "staging"
  }
}
