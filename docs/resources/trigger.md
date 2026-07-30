---
page_title: "circleci_trigger Resource - circleci"
subcategory: ""
description: |-
  Manages a CircleCI pipeline trigger.
---

# circleci_trigger (Resource)

Manages a CircleCI pipeline trigger. Triggers define when and how a pipeline runs — via GitHub events, webhooks, or a cron schedule.

A trigger with `event_source_provider = "schedule"` is the current way to run a
pipeline on a cron schedule. See
[Migrating scheduled pipelines](../guides/migrating-scheduled-pipelines) if you
are moving off the legacy schedule API or a community provider's
`circleci_schedule`.

## Availability

| CircleCI Cloud | CircleCI Server |
|---|---|
| yes | **no** |

~> **Not available on CircleCI Server** This resource uses the
`/api/v2/projects/{project_id}/triggers` endpoints, which a CircleCI Server
installation does not expose.

## Example Usage

One endpoint covers four event sources, and which attributes are required — even
which are *permitted* — depends on which one you pick. Each is shown separately
below rather than as one configuration with everything in it.

### GitHub App trigger

```terraform
# A trigger is created under a pipeline definition but read under the project, so
# both ids form its address — which is also why import needs all three segments.
# Referencing them by expression is what orders the graph correctly: the definition
# is created first, and the trigger is destroyed before it.
data "circleci_project" "api" {
  slug = "github/acme/api"
}

locals {
  # GitHub's own numeric repository id, which is what CircleCI's GitHub App event
  # sources are keyed by — not the repository name. The
  # circleci_github_app_repository data source reports it; so does
  # `gh api repos/acme/api --jq .id`.
  repo_external_id = "123456789"
}

resource "circleci_pipeline_definition" "build" {
  project_id  = data.circleci_project.api.id
  name        = "build"
  description = "Build and test on every push"

  config_source_provider         = "github_app"
  config_source_file_path        = ".circleci/config.yml"
  config_source_repo_external_id = local.repo_external_id

  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = local.repo_external_id
}

# GitHub App event source: run the pipeline on every push to the connected
# repository.
#
# `event_preset` selects which GitHub events fire the trigger, and is optional here
# (required for github_oauth, and rejected for webhook and schedule).
# `checkout_ref` and `config_ref` are deliberately absent: for a GitHub event source
# they are only expected when the event source repository differs from the pipeline
# definition's checkout or config repository, and must otherwise be omitted.
resource "circleci_trigger" "github_app" {
  project_id                    = data.circleci_project.api.id
  pipeline_definition_id        = circleci_pipeline_definition.build.id
  event_source_provider         = "github_app"
  event_source_repo_external_id = local.repo_external_id
  event_preset                  = "all-pushes"
  disabled                      = false
}

# `pipeline_id` is the deprecated spelling of `pipeline_definition_id` — it always
# took a pipeline *definition* id. Set exactly one of the two.
```

### GitHub Server trigger

```terraform
# GitHub Server (GitHub Enterprise Server) event source. It behaves exactly like
# `github_app` — same required attributes, same presets — but the repository ids
# belong to your own GitHub installation rather than to github.com, so they cannot
# be shared with a github_app pipeline definition. This example therefore stands on
# its own rather than reusing the one above.
#
# This concerns the *event source*, not where CircleCI runs: triggers are CircleCI
# Cloud only, whichever GitHub the organization is connected to.
data "circleci_project" "internal" {
  slug = "github/acme-internal/payments"
}

locals {
  # The repository id on the GitHub Server installation. These are small integers
  # allocated per installation, not github.com ids.
  internal_repo_external_id = "2259"
}

resource "circleci_pipeline_definition" "internal_build" {
  project_id  = data.circleci_project.internal.id
  name        = "build"
  description = "Build and test on every push"

  config_source_provider         = "github_server"
  config_source_file_path        = ".circleci/config.yml"
  config_source_repo_external_id = local.internal_repo_external_id

  checkout_source_provider         = "github_server"
  checkout_source_repo_external_id = local.internal_repo_external_id
}

resource "circleci_trigger" "github_server" {
  project_id                    = data.circleci_project.internal.id
  pipeline_definition_id        = circleci_pipeline_definition.internal_build.id
  event_source_provider         = "github_server"
  event_source_repo_external_id = local.internal_repo_external_id
  event_preset                  = "only-build-prs"
  disabled                      = false
}
```

### Scheduled trigger

```terraform
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
```

### Webhook trigger

```terraform
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
```

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `event_source_provider` (String) The event source provider: `github_app`, `github_server`, `github_oauth`, `webhook` or `schedule`.

~> The required attributes differ per provider, because this one endpoint covers several contracts. `event_name` is required for `webhook` and `schedule` only. `checkout_ref` and `config_ref` are required for `webhook` and `schedule`. `event_preset` is required for `github_oauth` and accepts only `all-pushes` or `only-build-prs` there, is optional for `github_app` and `github_server`, and must be omitted for `webhook` and `schedule`. `disabled` is unsupported for `github_oauth`, and `parameters` is supported only for `schedule`.

GitLab and Bitbucket Cloud pipelines cannot be given triggers through this API.
- `project_id` (String) The ID of the project this trigger belongs to.

### Optional

- `checkout_ref` (String) The ref to use when checking out code for pipeline runs created from this trigger. Always required when `event_source_provider` is `webhook` or `schedule`. When `event_source_provider` is `github_app` or `github_server`, only expected if the event source repository differs from the checkout source repository of the associated pipeline definition. Otherwise, must be omitted.
- `config_ref` (String) The ref to use when fetching configuration for pipeline runs created from this trigger. Always required when `event_source_provider` is `webhook` or `schedule`. When `event_source_provider` is `github_app` or `github_server`, only expected if the event source repository differs from the config source repository of the associated pipeline definition. Otherwise, must be omitted.
- `disabled` (Boolean) Whether the trigger is disabled. Defaults to `false`.
- `event_name` (String) The event name. Required when `event_source_provider` is `webhook` or `schedule`.
- `event_preset` (String) The event preset for GitHub triggers. Required when `event_source_provider` is `github_app` or `github_server`. Valid values: `all-pushes`, `only-tags`, `default-branch-pushes`, `only-build-prs`, `only-open-prs`, `only-labeled-prs`, `only-merged-prs`, `only-ready-for-review-prs`, `only-branch-delete`, `only-build-pushes-to-non-draft-prs`, `only-merged-or-closed-prs`, `pr-comment-equals-run-ci`, `non-draft-pr-opened`, `pushes-to-merge-queues`.
- `event_source_repo_external_id` (String) The external ID of the event source repository. Required when `event_source_provider` is `github_app` or `github_server`. This is the GitHub repository numeric ID.
- `event_source_schedule_attribution_actor` (String) Attribution actor for the schedule event source. Required when event_source_provider is schedule. Must be "system" or "current".
- `event_source_schedule_cron_expression` (String) Cron expression for the schedule event source. Required when event_source_provider is schedule.
- `event_source_web_hook_sender` (String) The webhook sender identifier. Required when `event_source_provider` is `webhook`.
- `parameters` (Map of String) Pipeline parameters to pass when running pipelines from this trigger. Only supported when `event_source_provider` is `schedule`.
- `pipeline_definition_id` (String) Unique identifier (UUID) of the [`circleci_pipeline_definition`](pipeline_definition) this trigger creates pipeline runs from.

This is a pipeline **definition** — where to check out code and where to find configuration — not a pipeline run. Same field as the deprecated `pipeline_id`; set exactly one of the two.
- `pipeline_id` (String, Deprecated) Unique identifier (UUID) of the pipeline definition this trigger creates pipeline runs from.

~> **Deprecated in favour of `pipeline_definition_id`.** This attribute has always taken the id of a [`circleci_pipeline_definition`](pipeline_definition), not of a pipeline run, which is what `pipeline_id` means on the run-scoped data sources. Both names work and mean the same thing; set exactly one. Switching to `pipeline_definition_id` does not replace the trigger.

### Read-Only

- `created_at` (String) The timestamp when the trigger was created.
- `event_source_repo_full_name` (String) The full name of the event source repository.
- `event_source_web_hook_url` (String, Sensitive) The webhook URL for webhook-based triggers.
- `id` (String) The unique identifier of the trigger.

## Import

Import is supported using `project_id/pipeline_definition_id/trigger_id`:

```shell
terraform import circleci_trigger.example "<project_id>/<pipeline_definition_id>/<trigger_id>"
```

-> **Why the pipeline definition id is part of the address.** A trigger is *created*
under a pipeline definition (`POST .../pipeline-definitions/{pipeline_definition_id}/triggers`)
but *read* under the project (`GET /projects/{project_id}/triggers/{trigger_id}`), and
the read response carries no reference back to the definition. The definition id
therefore cannot be recovered from the API and has to be supplied.

~> **Changed in 0.5.0.** Earlier versions accepted `project_id/trigger_id`. That form
left the pipeline definition id unset in state after import, leaving the next plan with
no way to converge short of editing state by hand. Supplying the third segment is what
makes import produce usable state; the two-segment form now reports an error explaining
this rather than silently importing something broken.
