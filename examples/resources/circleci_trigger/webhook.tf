# Webhook event source: CircleCI mints a URL, and any POST to it starts the
# pipeline. `event_source_web_hook_url` is Computed and Sensitive — read it out of
# the resource and hand it to whatever should call it; it cannot be set.
#
# As with `schedule`, `event_name`, `checkout_ref` and `config_ref` are required and
# `event_preset` must be omitted. `event_source_web_hook_sender` names who is
# expected to call the URL; it is a free-form label, not a fixed enumeration.
#
# `data.circleci_project.api` and `circleci_pipeline_definition.build` are declared
# in the primary example above.
resource "circleci_trigger" "release" {
  project_id             = data.circleci_project.api.id
  pipeline_definition_id = circleci_pipeline_definition.build.id

  event_source_provider        = "webhook"
  event_name                   = "release-published"
  event_source_web_hook_sender = "github"
  checkout_ref                 = "main"
  config_ref                   = "main"
}

output "circleci_release_webhook_url" {
  value     = circleci_trigger.release.event_source_web_hook_url
  sensitive = true
}
