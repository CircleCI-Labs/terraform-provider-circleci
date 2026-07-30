# A trigger is read under its project, so both ids are required. Resolving the
# project id from its slug keeps one of the two literals out of the configuration.
#
# CircleCI Cloud only: a CircleCI Server installation does not route the trigger
# endpoints.
data "circleci_project" "api" {
  slug = "github/acme/api"
}

# Trigger ids are not otherwise discoverable — use the circleci_triggers data source
# to list the triggers on a pipeline definition and pick one out.
data "circleci_trigger" "nightly" {
  id         = "00000000-0000-0000-0000-000000000000" # illustrative: a trigger UUID
  project_id = data.circleci_project.api.id
}

# The schedule attributes are populated only for `event_source_provider = "schedule"`,
# and the repository attributes only for a GitHub event source. Everything else reads
# back as null.
output "circleci_nightly_trigger" {
  value = {
    created_at            = data.circleci_trigger.nightly.created_at
    event_name            = data.circleci_trigger.nightly.event_name
    event_source_provider = data.circleci_trigger.nightly.event_source_provider
    checkout_ref          = data.circleci_trigger.nightly.checkout_ref
    parameters            = data.circleci_trigger.nightly.parameters

    cron_expression   = data.circleci_trigger.nightly.event_source_schedule_cron_expression
    attribution_actor = data.circleci_trigger.nightly.event_source_schedule_attribution_actor
  }
}

# Note the names: the read shape spells the repository attributes
# `event_source_repository_name` and `event_source_repository_external_id`, and the
# webhook URL `event_source_webhook_url` — where circleci_trigger the *resource*
# spells them `event_source_repo_full_name`, `event_source_repo_external_id` and
# `event_source_web_hook_url`.
output "circleci_nightly_trigger_event_source" {
  value = {
    repository_name        = data.circleci_trigger.nightly.event_source_repository_name
    repository_external_id = data.circleci_trigger.nightly.event_source_repository_external_id
    event_preset           = data.circleci_trigger.nightly.event_preset
  }
}
