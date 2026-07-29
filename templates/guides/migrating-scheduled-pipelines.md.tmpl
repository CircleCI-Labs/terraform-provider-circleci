---
page_title: "Migrating scheduled pipelines"
subcategory: "Guides"
description: |-
  Express a scheduled pipeline as a circleci_trigger instead of the legacy schedule API.
---

# Migrating scheduled pipelines

CircleCI has had three generations of scheduling. This guide explains which one
to use with Terraform and how to move off the older two.

| Generation | Where it lives | Use with Terraform? |
|---|---|---|
| Scheduled workflows (`triggers:` in `.circleci/config.yml`) | Your config file | No — it is config, not API state |
| Scheduled pipelines (`/api/v2/project/{slug}/schedule`) | Legacy API | No — superseded |
| **Schedule triggers** (a trigger on a pipeline definition) | Current API | **Yes — `circleci_trigger`** |

This provider deliberately does not implement a `circleci_schedule` resource.
The legacy schedule API is available only for organizations of type `bitbucket`
and `github` (OAuth), and for GitHub organizations it now creates schedule
triggers anyway. Modelling it would add a resource that is unavailable to
standalone (`circleci/<uuid>`) organizations and to every GitHub App project.

## Expressing a schedule as a trigger

Set `event_source_provider = "schedule"` on a `circleci_trigger` attached to a
pipeline definition:

```terraform
resource "circleci_pipeline_definition" "nightly" {
  project_id  = var.project_id
  name        = "nightly"
  description = "Nightly build"

  config_source_provider         = "github_app"
  config_source_file_path        = ".circleci/config.yml"
  config_source_repo_external_id = var.repo_external_id

  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = var.repo_external_id
}

resource "circleci_trigger" "nightly" {
  project_id             = var.project_id
  pipeline_definition_id = circleci_pipeline_definition.nightly.id
  event_name             = "nightly"

  event_source_provider                   = "schedule"
  event_source_schedule_cron_expression   = "0 4 * * *"
  event_source_schedule_attribution_actor = "system"

  checkout_ref = "main"
  config_ref   = "main"
}
```

`event_name` is required for `schedule` event sources.
`event_source_schedule_attribution_actor` is either `system` or `current`, and
determines whose permissions the triggered pipeline runs with.

## Mapping the legacy fields

| Legacy schedule field | Trigger equivalent |
|---|---|
| `name` | `event_name` |
| `description` | `circleci_pipeline_definition.description` |
| `timetable` | `event_source_schedule_cron_expression` |
| `attribution-actor` | `event_source_schedule_attribution_actor` |
| `parameters` | `parameters` |
| `parameters.branch` | `checkout_ref` |

A legacy `timetable` is a structured object (`per-hour`, `hours-of-day`,
`days-of-week`). A trigger takes a standard five-field cron expression instead,
so `{"per-hour": 1, "hours-of-day": [4], "days-of-week": ["MON"]}` becomes
`0 4 * * 1`. The provider validates the expression.

## Migrating from `circleci_schedule`

If you are coming from `kelvintaywl/circleci` or `healx/circleci`:

1. Note each schedule's timetable, parameters and attribution actor.
2. Remove it from state without deleting it:
   `terraform state rm circleci_schedule.nightly`
3. Delete the schedule in the CircleCI web application, or via
   `DELETE /api/v2/schedule/{schedule-id}`. A schedule and an equivalent trigger
   would otherwise both fire.
4. Add `circleci_pipeline_definition` and `circleci_trigger` resources as above
   and apply.

There is no import path from a legacy schedule to a trigger — they are different
objects — so the old schedule must be removed explicitly.

-> **CircleCI Server** `circleci_pipeline_definition` and `circleci_trigger` are
unavailable on CircleCI Server, which does not expose the pipeline definition and
trigger endpoints. Server installations must continue to use scheduled workflows
in `.circleci/config.yml`.

~> **Deprecation** From 2026-08-01, `pipeline.trigger_parameters.circleci.*` and
`pipeline.trigger_parameters.github_app.*` values reach end of life. Review any
configuration that reads them.
