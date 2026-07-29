# Lists every trigger on a pipeline definition.
# CircleCI Cloud only: CircleCI Server does not route the trigger endpoints.
#
# Use the circleci_pipeline_definitions data source to discover the pipeline
# definitions on a project, since pipeline_definition_id scopes this listing.
data "circleci_triggers" "build" {
  project_id             = "00000000-0000-0000-0000-000000000000"
  pipeline_definition_id = "11111111-1111-1111-1111-111111111111"
}

# Scheduled runs on this pipeline definition.
output "circleci_schedules" {
  value = {
    for trigger in data.circleci_triggers.build.triggers :
    trigger.name => trigger.event_source_schedule_cron_expression
    if trigger.event_source_provider == "schedule"
  }
}

# Triggers that exist but will never fire.
output "circleci_disabled_triggers" {
  value = [
    for trigger in data.circleci_triggers.build.triggers : trigger.name
    if trigger.disabled
  ]
}
