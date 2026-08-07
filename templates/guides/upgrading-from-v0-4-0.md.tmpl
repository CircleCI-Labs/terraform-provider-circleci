---
page_title: "Upgrading from v0.4.0"
subcategory: "Guides"
description: |-
  What changes when you upgrade from v0.4.0: a webhook signing-secret fix you should act on, renamed types and attributes, three attributes that became sets, and several behaviour changes that alter existing plans.
---

# Upgrading from v0.4.0

This release goes from 11 resources and 10 data sources to several times that,
but almost none of the growth affects an existing configuration — new resources
are additive by definition. What follows is everything that changes for
configurations that already worked under v0.4.0.

## The short version

| Change | Action required |
|---|---|
| `circleci_webhook` never sent `signing_secret` | **Yes — see below, before anything else** |
| `circleci_pipeline` → `circleci_pipeline_definition` | Add a `moved` block (or wait — the old name still works) |
| `circleci_trigger.pipeline_id` → `pipeline_definition_id` | Optional rename, no `moved` block needed |
| `organization_id` → `org_id` | Optional rename, nothing else needed |
| `events` / `pr_only_branch_overrides` / `audience`: list → set | Only if your configuration indexes them |
| `build_fork_prs = true` without `forks_receive_secret_env_vars` | Set the second attribute explicitly |
| `oss` set in configuration | Remove it — the attribute is now read-only |
| Changing a trigger's pipeline definition | Now replaces the trigger; used to silently do nothing |
| `circleci_trigger` import IDs | Now three segments, not two |
| Any secret this provider writes | A write-only variant now exists — optional |

## First: the webhook signing secret

**Do this before reading the rest of this guide.** If you manage any
`circleci_webhook` resources, re-apply your configuration with this provider
version:

```console
terraform apply
```

Every webhook this provider created or updated at v0.4.0 or earlier was sent
with no signing secret at all, however carefully one was configured, because
the request used the JSON keys `signing-secret` and `verify-tls` (hyphenated)
where the CircleCI API reads `signing_secret` and `verify_tls`. The API silently
ignores keys it does not recognise, so `terraform apply` reported success while
sending neither field, and TLS verification quietly took the server-side
default.

The signing secret is what lets a receiver distinguish a genuine CircleCI
delivery from a forged one. If you configured one and your receiver verifies
signatures, every delivery to it was being rejected. If your receiver does not
verify signatures, treat this as having accepted unauthenticated requests for
however long the webhook has existed.

**The fix is a no-op in your configuration** — `signing_secret` does not need to
change — but `terraform plan` cannot show you that the secret was previously
missing, since from Terraform's point of view nothing about your configuration
is different. Re-applying is what actually sends the correct value for the
first time.

One limitation stays: `circleci_webhook.signing_secret` still cannot be read
back after it is set, because the API always masks it on `GET`. That is
cosmetic and unrelated to the fix above — it means Terraform cannot verify a
signing secret by reading it back, on this version or any other.

## Renamed types and attributes

### `circleci_pipeline` → `circleci_pipeline_definition`

CircleCI's API distinguishes a pipeline **definition** (where to check out
code, where the config file is) from a pipeline **run** (one execution of a
definition). `circleci_pipeline` named the definition resource with the word
that, in every other context, means the run — so this is the one rename with a
real migration step.

**`circleci_pipeline` still works.** Both the resource and the data source
accept the old name today, with a deprecation warning; neither is being
removed in this release. When you do migrate the resource, use a `moved`
block so the pipeline definition is re-addressed rather than destroyed:

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

`moved` blocks across resource *types* require **Terraform 1.8 or later**. On
an older Terraform, keep using `circleci_pipeline` until you can upgrade
Terraform itself — there is no other safe path to the new name.

The data source has no state, so renaming it is just editing the type name in
your configuration; there is nothing to move.

Full walkthrough, including the `circleci_trigger.pipeline_id` rename below and
the plan output you should expect to see:
[Renaming pipeline resources and data sources](renaming-pipeline-types).

### `circleci_trigger.pipeline_id` → `pipeline_definition_id`

The same ambiguity, on an attribute: `pipeline_id` on `circleci_trigger` has
always held a pipeline *definition* id, while the same name on the run-scoped
data sources holds a pipeline *run* id. Both are UUIDs, so nothing caught a
mix-up.

Both names are accepted; set exactly one. Switching from one to the other
plans as **no change** — the attributes are `Optional+Computed` and mirrored,
so the trigger is not replaced:

```terraform
resource "circleci_trigger" "on_push" {
  name                   = "on-push"
  project_id             = circleci_project.example.id
  pipeline_definition_id = circleci_pipeline_definition.nightly.id # was pipeline_id

  event_source_provider         = "github_app"
  event_source_repo_external_id = var.repo_id
  event_preset                  = "all-pushes"
}
```

If your state was written by v0.4.0, it predates `pipeline_definition_id`
entirely — that attribute is simply null in it. The first plan you run under
this version still does not replace the trigger, whether or not anything else
in the same plan changes; the provider reconciles both names from
configuration before deciding whether a replacement is warranted.

### `organization_id` → `org_id`

Cosmetic only, and the least disruptive rename in this release: `org_id`
matches CircleCI's own naming. Available on 37 resources and data sources.

- Existing configurations using `organization_id` keep working, unchanged and
  indefinitely — no removal is planned for this release.
- **Switching to `org_id` does not replace anything and does not touch
  state.** No `moved` block, no `terraform state` command, no schema upgrade.
- Set exactly one of the two on any given resource. Setting both is a
  plan-time error, since they would be ambiguous if they disagreed.

```terraform
resource "circleci_context" "build" {
  name   = "build-secrets"
  org_id = data.circleci_organization.example.id # was organization_id
}
```

This one is deliberately unhurried *because* it is safe to be: the attribute
carries `RequiresReplace` on twelve resource types (CircleCI has no route that
moves an object between organizations), so the naive version of this migration
— dropping `organization_id` and adding `org_id` as an ordinary `Optional`
attribute — would have destroyed and recreated every one of those resources
the moment you followed the deprecation notice. On `circleci_project` that
means deleting the project and its build history. What is shipped instead is
`Computed` plus `RequiresReplaceIfConfigured`, specifically so that removing
the old name is not itself the destructive event.

### Names that never shipped

If you were tracking an unreleased build rather than v0.4.0, a few more names
changed outright, with no deprecation shim, because they never reached a
release:

| Unreleased name | Current name |
|---|---|
| `circleci_pipelines` | `circleci_pipeline_definitions` |
| `circleci_pipeline_config` | `circleci_pipeline_run_config` |
| `circleci_pipeline_values` | `circleci_pipeline_run_values` |
| `circleci_pipeline_workflows` | `circleci_pipeline_run_workflows` |
| `pipeline_id` on the two data sources above | `run_id` (now `Required`) |
| `pipelines` on `circleci_pipeline_definitions` | `pipeline_definitions` |
| `pipeline_id` on `circleci_triggers` | `pipeline_definition_id` |
| nested `pipeline_id` on `circleci_deploy_component` | `run_id` |

If you were on an actual v0.4.0 release, this table does not apply to you.

## Attributes that became sets

`circleci_webhook.events`, and `pr_only_branch_overrides` on both
`circleci_project` and `circleci_project_settings`, changed from a Terraform
list to a set in 0.5.0. The corresponding data source attributes
(`circleci_webhook`, `circleci_webhooks`) changed with them.

`circleci_oidc_custom_claims.audience` follows in the next release, for the same
reason and with the same consequences. It is listed here rather than in a
separate guide because the fix and the remedy are identical; if you are reading
this while running 0.5.0, that attribute is still a list for you.

**Your state needs no migration, and nothing in it changes.** A list and a set
of the same element type share one JSON encoding, Terraform state is stored as
JSON, and the schema version for all of them stayed at `0` — no
`UpgradeResourceState` was added because none was needed. This is not merely
asserted: `TestListToSetNeedsNoStateUpgrade` feeds v0.4.0-shaped, list-typed
prior state through the provider's real `UpgradeResourceState` RPC for each of
these attributes and checks that a set comes back holding the right elements.
Nothing you need to do here.

~> **What does break** is a configuration that relied on these attributes
holding an order, because CircleCI itself never preserved one — the API
returns both in an order of its own choosing, which is the underlying bug this
change fixes (see below). If your configuration indexes into either
attribute, it was already producing a plan that never converged; it now also
fails to parse, because indexing is not defined on a set.

```terraform
# Before — silently never worked, and no longer valid HCL either
locals {
  primary_event = circleci_webhook.deploys.events[0]
}

# After — order-independent
locals {
  has_workflow_event = contains(circleci_webhook.deploys.events, "workflow-completed")
  events_as_list     = tolist(circleci_webhook.deploys.events) # if you need a stable order yourself
}
```

Use `contains(...)`, a `for` expression, or `tolist(...)` if you need to iterate
in some order you impose yourself — CircleCI's own order is not meaningful and
is not guaranteed to stay stable across API changes.

## Behaviour changes that alter existing plans

None of these need a `moved` block or a state operation. Each is a change in
what the provider sends to, or reads from, the API — so the plan you see after
upgrading may differ from the plan you saw before, even with configuration
unchanged.

### `build_fork_prs = true` now requires `forks_receive_secret_env_vars`

On both `circleci_project` and `circleci_project_settings`, enabling fork
builds without also setting `forks_receive_secret_env_vars` now fails
`terraform plan` with an error naming the exposure:

```terraform
resource "circleci_project_settings" "example" {
  project_id     = circleci_project.example.id
  build_fork_prs = true
  # forks_receive_secret_env_vars intentionally omitted -> plan-time error
}
```

This exists because of a related fix: the provider no longer writes a setting
your configuration does not mention (see below), so an unset
`forks_receive_secret_env_vars` now takes CircleCI's own default — which is
**`true` on a private project**. Enabling fork builds without deciding this
question would silently hand the project's environment variables, secrets and
build cache to anyone who can open a pull request. Set the value you actually
want:

```terraform
resource "circleci_project_settings" "example" {
  project_id                    = circleci_project.example.id
  build_fork_prs                = true
  forks_receive_secret_env_vars = false # keeps secrets out of fork builds
}
```

### `oss` can no longer be set

`oss` is read-only on the CircleCI API this provider now speaks — sending it
on a settings update fails the *entire* request with a `400`, which used to
mean every write silently failed for projects where a mocked test fixture had
been quietly accepting the field. `oss` is now `Computed` only, on both
`circleci_project` and `circleci_project_settings`. If your configuration sets
it:

```terraform
resource "circleci_project" "example" {
  # ...
  oss = true # remove this line
}
```

remove the line. `terraform plan` refuses to proceed while it is present, with
an "Invalid Configuration for Read-Only Attribute" error naming `oss`. Set the
project's open-source status in the CircleCI web application instead — there
is no API route this provider (or any tool) could write it through.

### Two defaults that were silently wrong now apply correctly

`circleci_project`'s `Create` used to write `false` for **every** boolean
setting a configuration left unset, regardless of what CircleCI defaults it
to — an unconfigured `Optional+Computed` attribute plans as *unknown*, not
null, and the old code treated unknown the same as a real `false`. Two of
those defaults were actively wrong: `set_github_status` defaults to `true` at
CircleCI, so a project created by Terraform without explicitly setting it
silently stopped reporting commit status; `pr_only_branch_overrides` defaults
to the repository's default branch, so it was cleared instead.

An unconfigured setting is now left out of the create request entirely, so
CircleCI's own default applies and is read back into state. This is not
retroactive: a project your v0.4.0 configuration already created has whatever
value CircleCI was actually given at the time, and upgrading does not rewrite
it. If commit status has been silently missing on a Terraform-created
project, this was almost certainly why — check the current value and, if you
want it changed, set the attribute explicitly and apply:

```terraform
resource "circleci_project" "example" {
  # ...
  set_github_status = true
}
```

### `events` and `pr_only_branch_overrides` stop showing a permanent diff

Related to the list-to-set change above, and the reason for it: both are
unordered on CircleCI's side, and the API answers with them in an order of its
own choosing on every read, so a configuration that got a plan-only diff every
run — with nothing ever actually applied — should now settle. If you were
carrying an `ignore_changes` for either attribute as a workaround, it is safe
to remove.

### `pr_only_branch_overrides` branch names are no longer quoted on the wire

If this attribute never seemed to take effect, the previous code sent branch
names with their Terraform *display* quoting intact — `main` reached the API
as the four characters `"main"`. That is fixed; no configuration change is
needed, but the first `apply` after upgrading may finally set overrides that
have been silently ignored until now.

### Changing a trigger's pipeline definition now replaces the trigger

Editing `pipeline_definition_id` (or the deprecated `pipeline_id`) on an
existing `circleci_trigger` used to plan a harmless-looking in-place update
that the API silently ignored — the trigger stayed attached to its original
pipeline definition no matter what state said. That is now visible instead of
silent: `terraform plan` shows `# forces replacement`, and applying it
**deletes the old trigger and creates a new one**.

The new trigger gets a new id, and — for a `webhook` event source — a new
`event_source_web_hook_url`, so anything posting to the old URL needs to be
repointed. In-flight pipeline runs from the old trigger are unaffected. This
is not a concern for the *rename* above: switching between `pipeline_id` and
`pipeline_definition_id` on the same trigger is still a no-op and still
replaces nothing, whether or not your state predates the new attribute name.

### `circleci_trigger` validates more, and validates earlier

Rules like "`parameters` only applies to `schedule` triggers" or "`github_app`
requires `event_source_repo_external_id`" used to be enforced in `Create` and
`Update`, so `terraform plan` could succeed on a configuration that was always
going to fail at `apply` — after review, and after any other resources in the
same apply had already been created. These rules now run during
`ValidateConfig`, so the same mistakes surface at `plan` time instead, with a
diagnostic naming the specific attribute.

Two rules also changed outcome, not just timing:

- `event_source_provider = "github_oauth"` **now works.** It was documented as
  valid and rejected at apply by code that had simply not been updated to
  include it.
- `event_preset` is **no longer required** for `github_app` and
  `github_server` triggers. It remains required for `github_oauth`, which
  accepts only `all-pushes` or `only-build-prs` there.

If you have a `github_oauth` trigger, or one for `github_app`/`github_server`
that omits `event_preset`, expect `terraform plan` to behave differently —
usually by no longer failing where it used to.

### `circleci_trigger` import IDs take three segments now

`terraform import` for a trigger used to take `project_id/trigger_id`; it now
requires `project_id/pipeline_definition_id/trigger_id`:

```console
terraform import circleci_trigger.on_push \
  11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222/33333333-3333-3333-3333-333333333333
```

This only affects future `terraform import` runs — nothing already in state is
affected, and no migration is needed for a trigger you imported previously.
The old two-segment form could never have produced working state: a trigger's
`GET` response carries no reference back to the pipeline definition it was
created under, so `pipeline_definition_id` — a value every plan needs — stayed
null after import with no way to converge. The two-segment form now fails
`terraform import` outright, with an error explaining why, rather than
importing something that could never plan cleanly.

## Secrets: write-only variants now exist for everything this provider writes

Every secret-bearing attribute this provider can write now has a write-only
counterpart, on both resources that existed at v0.4.0:

| Resource | State-backed (unchanged) | Write-only (new) |
|---|---|---|
| `circleci_context_environment_variable` | `value` | `value_wo` + `value_wo_version` |
| `circleci_project_environment_variable` | `value` | `value_wo` + `value_wo_version` |
| `circleci_webhook` | `signing_secret` | `signing_secret_wo` + `signing_secret_wo_version` |

(The other three pairs — on `circleci_ios_signing_certificate`,
`circleci_ios_signing_config` and `circleci_otel_exporter` — are new resources
in this release, so they have no v0.4.0 configuration to migrate; see
[Managing secrets](managing-secrets) if you adopt them.)

Nothing here is deprecated, and nothing here requires action. `value` and
`signing_secret` are unchanged, still work, and are not going away. This is an
opt-in for configurations that want the secret to never land in Terraform
state at all:

```terraform
resource "circleci_context_environment_variable" "registry_token" {
  context_id = circleci_context.build.id
  name       = "REGISTRY_TOKEN"

  value_wo         = var.registry_token
  value_wo_version = 1
}
```

Set exactly one of `value` and `value_wo` on any given resource — setting
`value_wo` without its version is rejected at plan time, since without that
check the secret would be written once and become permanently unrotatable.

**The trade-off, plainly:** because the value is never stored, Terraform has
no copy to compare against, so it cannot tell on its own that `value_wo`
changed. You tell it, by incrementing `value_wo_version` (or
`signing_secret_wo_version`) at the same time you change the value:

```terraform
resource "circleci_context_environment_variable" "registry_token" {
  context_id = circleci_context.build.id
  name       = "REGISTRY_TOKEN"

  value_wo         = var.registry_token # the new value
  value_wo_version = 2                  # bumped — this is what tells Terraform to send it
}
```

~> **If you forget to bump the version**, changing `value_wo` alone produces
no plan at all — not a wrong plan, no plan. The new value is silently never
sent, CircleCI keeps whatever was written last, and there is nothing in
`terraform plan` output to suggest anything is wrong, because from Terraform's
perspective nothing changed. This is the entire cost of keeping the secret out
of state: you gain a value that never appears in a state file or a plan file,
and in exchange the version counter becomes the only signal Terraform has for
"send this again." Treat the counter as part of the secret's identity, not as
an afterthought — bump it in the same commit or the same `apply` that changes
the value, never separately.

One detail matters if you compare this against another provider: the
write-only secret is re-sent on **every** write the resource makes, not only
the one where the version changed. `circleci_webhook`'s update route is a
full-replace `PUT`, so a body that omitted `signing_secret` would delete it
server-side with nothing to recover it from — exactly the failure mode
recorded against `hashicorp/terraform-provider-vault#2900` for an unrelated
provider's write-only secret. `TestWebhookWriteOnly_UnrelatedUpdateStillSendsTheSecret`
renames a webhook using the write-only path and asserts the rename's `PUT`
still carried the secret, so this cannot regress unnoticed.

Full treatment — feeding a write-only value from an `ephemeral` block, what
each attribute pair looks like, and why two of the six pairs (the iOS signing
certificate and the OpenTelemetry exporter) are shaped slightly differently —
is in [Managing secrets](managing-secrets).

## Also worth knowing

A few fixes are pure corrections with nothing to configure, listed here so an
unexpected plan or an unexpected value has a place to be recognised rather
than investigated from scratch:

- **`circleci_checkout_key.public_key` and
  `circleci_project_environment_variable.created_at` were always empty**,
  because the removed SDK's struct tags did not match the API's field names.
  Both are `Computed`-only; the next `terraform plan` populates them with real
  values from CircleCI with nothing for you to do.
- **`circleci_context_environment_variable` gained `remote_updated_at`**, a new
  `Computed` attribute used to detect a value changed outside Terraform by
  comparing timestamps rather than the value itself (which the API never
  returns). It appears in state after your next refresh; nothing else about
  the attribute changes.
- **Drift detection that used to delete live resources from state no longer
  does.** Absence used to be detected by matching the literal text `"404"`
  inside an error message, which also matched some server errors that
  happened to contain that text — silently removing resources that were never
  actually gone. If a resource has been mysteriously reappearing in your plans
  as `# will be created` after being genuinely fine, this was almost
  certainly why.
