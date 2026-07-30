---
page_title: "Renaming pipeline resources and data sources"
subcategory: "Guides"
description: |-
  Move from circleci_pipeline to circleci_pipeline_definition, and from pipeline_id to pipeline_definition_id on circleci_trigger.
---

# Renaming pipeline resources and data sources

CircleCI's API distinguishes two things that a UUID can identify:

| Concept | Route | What it is |
|---|---|---|
| Pipeline **definition** | `/projects/{project_id}/pipeline-definitions` | Where to check out, where to find configuration, which config file |
| Pipeline **run** | `/pipeline/{id}` | One execution of a definition, which spawns workflows and jobs |

In everyday CircleCI usage "pipeline" means the *run*. So naming this provider's
definition resource `circleci_pipeline` pointed the shorter, more familiar word
at the less familiar concept — and made this read correctly while being wrong:

```terraform
data "circleci_pipeline_run_workflows" "w" {
  run_id = circleci_pipeline.nightly.id # a definition id, not a run id
}
```

Both are UUIDs, so nothing rejects it at plan time. The names now say which
concept they mean.

## What changed

| Old name | New name | Kind |
|---|---|---|
| `circleci_pipeline` | `circleci_pipeline_definition` | Resource |
| `circleci_pipeline` | `circleci_pipeline_definition` | Data source |
| `circleci_trigger.pipeline_id` | `circleci_trigger.pipeline_definition_id` | Attribute |

-> **Nothing breaks yet.** All three old names are still accepted and still
work. Using one produces a deprecation warning, not an error. They will be
removed in the next major release.

`circleci_pipeline_run` is unchanged — it was already named for the right thing.

## Migrating the resource

`circleci_pipeline` is the only renamed resource, so it is the only place
Terraform has stored state under an old type name. Rename it in your
configuration and add a `moved` block:

```terraform
resource "circleci_pipeline_definition" "nightly" {
  project_id  = circleci_project.example.id
  name        = "nightly"
  description = "Nightly build"

  config_source_provider         = "github_app"
  config_source_repo_external_id = var.repo_id
  config_source_file_path        = ".circleci/config.yml"

  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = var.repo_id
}

moved {
  from = circleci_pipeline.nightly
  to   = circleci_pipeline_definition.nightly
}
```

Then run `terraform plan`. Terraform reports the move and plans **no changes**:

```console
Terraform will perform the following actions:

  # circleci_pipeline.nightly has moved to circleci_pipeline_definition.nightly
    resource "circleci_pipeline_definition" "nightly" {
        id = "44444444-4444-4444-4444-444444444444"
        # (10 unchanged attributes hidden)
    }

Plan: 0 to add, 0 to change, 0 to destroy.
```

Apply it. The state entry is re-addressed in place — **your pipeline definition
is not destroyed and not recreated**, so its id is preserved and any triggers
attached to it stay attached. Once applied you can delete the `moved` block.

~> Do not rename the resource *without* a `moved` block. Terraform would see an
unknown resource in state and plan to destroy your pipeline definition and
create a new one, which changes its id and orphans its triggers.

`moved` blocks across resource types require Terraform 1.8 or later. On an
earlier version, keep using `circleci_pipeline` until you upgrade — it still
works.

## Migrating the data source

Data sources hold no state; they are re-read on every plan. There is nothing to
migrate. Change the type name and the references to it:

```terraform
# Before
data "circleci_pipeline" "nightly" {
  id         = var.definition_id
  project_id = var.project_id
}

# After
data "circleci_pipeline_definition" "nightly" {
  id         = var.definition_id
  project_id = var.project_id
}
```

## Migrating `circleci_trigger`

`circleci_trigger.pipeline_id` has always held a pipeline **definition** id —
the trigger route is `/projects/{project_id}/pipeline-definitions/{id}/triggers`.
The old name was ambiguous in the worst way, because `pipeline_id` elsewhere in
this provider means a run id. Set `pipeline_definition_id` instead:

```terraform
resource "circleci_trigger" "on_push" {
  name                   = "on-push"
  project_id             = circleci_project.example.id
  pipeline_definition_id = circleci_pipeline_definition.nightly.id

  event_source_provider = "github_app"
  event_source_repo_id  = var.repo_id
}
```

Set exactly one of the two names. Setting both, or neither, is a plan-time
error. Switching from one to the other plans as no change — the trigger is not
replaced.

## Names that changed before release

These type and attribute names existed only in unreleased builds, so they were
renamed outright rather than deprecated. If you were tracking an unreleased
version, update them directly; there is no compatibility shim.

| Unreleased name | Current name |
|---|---|
| `circleci_pipelines` | `circleci_pipeline_definitions` |
| `circleci_pipeline_config` | `circleci_pipeline_run_config` |
| `circleci_pipeline_values` | `circleci_pipeline_run_values` |
| `circleci_pipeline_workflows` | `circleci_pipeline_run_workflows` |
| `pipeline_id` on `circleci_pipeline_run_values` and `circleci_pipeline_run_workflows` | `run_id` |
| `pipeline_run_id` on `circleci_pipeline_run_config` | `run_id` |
| `pipelines` on `circleci_pipeline_definitions` | `pipeline_definitions` |
| `pipeline_id` on `circleci_triggers` | `pipeline_definition_id` |
| nested `pipeline_id` on `circleci_deploy_component` | `run_id` |
