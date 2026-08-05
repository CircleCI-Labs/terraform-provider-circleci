---
page_title: "Importing an existing organization"
subcategory: "Guides"
description: |-
  Bring projects, pipelines, triggers, contexts and secrets that already exist under Terraform, with generated configuration and an honest account of what cannot come along.
---

# Importing an existing organization

Every organization this provider will ever manage already has something in it.
Projects were created by hand, pipelines were wired up from the CircleCI web
application, contexts were populated one variable at a time. Whether that
history is worth turning into Terraform configuration — rather than starting
fresh and only writing new things going forward — is usually decided by one
question: **how much of it can be imported, versus retyped?**

This guide is the answer, worked end to end: the two ways to import, which data
sources let you find what to import, a worked example across the objects that
depend on each other, a complete reference of every import ID format this
provider supports, and an honest account of the two kinds of things that
cannot come along — secrets, and ephemeral resources.

If you have not read [Getting started](./getting-started) or [How CircleCI's
objects relate](./object-model), the worked example below will make more sense
after either.

## The two ways to import

### `terraform import`, the imperative form

You write the resource block yourself, then tell Terraform which live object it
corresponds to:

```terraform
resource "circleci_project" "api" {
  name   = "api"
  org_id = "00000000-0000-0000-0000-000000000000"
}
```

```shell
terraform import circleci_project.api "github/acme/api"
```

This works, and every resource in this provider supports it. Its cost shows up
at scale: you have to write the resource block — every `Required` argument,
with its real value — before Terraform will let you import into it, for every
object you are adopting. For one project that is nothing. For an organization
with forty projects, each with a pipeline definition and two triggers, it is a
lot of typing you can get wrong.

### The `import` block, and generating config from it

Since Terraform 1.5, an `import` block does the same thing declaratively, and
it can go in any `.tf` file:

```terraform
import {
  to = circleci_project.api
  id = "github/acme/api"
}
```

That alone still requires a `circleci_project.api` resource block to exist —
`import` blocks populate state into a resource your configuration already
declares. The difference that matters for bulk adoption is what Terraform 1.6
adds on top: given only the `import` block, it can **write the resource block
for you**.

```shell
terraform plan -generate-config-out=generated.tf
```

Terraform reads the live object, fills in a `resource "circleci_project" "api"
{ ... }` block with what it found, and writes it to `generated.tf` — leaving
your own `.tf` files untouched. Review the file, move what you want into your
real configuration, and run `terraform plan` again to confirm it is empty. This
is the path this guide uses throughout, because it is the one that makes
importing forty projects tractable: write forty `import` blocks — easily
generated from whatever list of repositories or slugs you already have — run
one `plan -generate-config-out`, and read the result instead of typing forty
resource blocks by hand.

Two things worth knowing before you rely on it:

- **A `null` `Optional` attribute is omitted, not written.** If a setting was
  never configured on the live object, the generated block does not mention it
  at all — it does not write `setting = null`. This matters a great deal for
  this provider, because several resources leave attributes `null` after
  import *on purpose* (see "What to expect on the first plan after import"
  below), and it is why the generated config for those is shorter than the
  full schema.
- **Generated config is not always valid config as-is.** Where an attribute's
  configured form and its read-back form differ — see `circleci_trigger`'s
  `event_source_schedule_attribution_actor` under "Secrets" below for a
  concrete, non-secret example — the generated literal can fail plan-time
  validation on the very next `plan`. Read what the file's own documentation
  says about its `## Import` section before trusting the generated block
  verbatim.

Remove the `import` block once the object is safely in state; leaving it in
place is harmless (Terraform treats importing an already-tracked resource as a
no-op) but it is one more thing to read later.

## Finding what to import

An `import` block needs an id you already have. This provider's plural data
sources are how you get a list of ids instead of hunting for them one at a
time — most of them read something you did not create in Terraform just as
well as something you did, so they work equally well for discovery.

For the spine this guide's worked example walks — organization, project,
pipeline definition, trigger, context, context environment variable — the
relevant ones, and what they need to be scoped by:

| To list | Use | Scoped by |
|---|---|---|
| Pipeline definitions on a project | `circleci_pipeline_definitions` | `project_id` |
| Triggers on a definition | `circleci_triggers` | `project_id` + `pipeline_definition_id` |
| Contexts in an organization | `circleci_contexts` | `org_id` |
| Restrictions on a context | `circleci_context_restrictions` | `context_id` |
| Environment variable *names* on a project | `circleci_project_environment_variables` | `project_slug` |
| GitHub App repositories CircleCI can see | `circleci_github_app_repositories` | `org_id` |

Beyond the spine, most other object types have an equivalent: `circleci_groups`
and `circleci_project_groups` for access control, `circleci_checkout_keys` and
`circleci_webhooks` for a project's credentials and outbound hooks,
`circleci_runner_resource_classes` and `circleci_runner_tokens` for self-hosted
runners, `circleci_orbs` for a namespace's published orbs, and one plural data
source apiece for audit log configs, budgets, OpenTelemetry exporters, iOS
signing certificates and configurations, notification channel configs and the
URL orb allow list. `circleci_organization` (singular) resolves a slug to the
UUID everything else above is scoped by.

-> **Two real gaps, stated plainly rather than hidden.** There is no plural
data source that lists every **project** in an organization, and none that
lists a **context's** environment variable *names* (unlike a project's, which
`circleci_project_environment_variables` does report, masked). For projects,
the practical source of truth is the CircleCI web application's project list,
or the repository list from `circleci_github_app_repositories` cross-checked
against it — a repository the GitHub App can see is not necessarily a
CircleCI project yet. For a context's variable names, the CircleCI web
application or the API directly is the only option through this provider's
current data sources.

## Worked example: a project, its pipeline, a trigger, a context and a secret

This walks the same five objects [Getting started](./getting-started) creates
from nothing, in the same order, but imports them instead. Assume a GitHub App
organization with a repository `acme/api` that already has a pipeline
definition, a trigger, and a context supplying it a secret through
`REGISTRY_TOKEN`.

First, the organization — you need its UUID for everything that follows:

```terraform
data "circleci_organization" "acme" {
  slug = "circleci/00000000-0000-0000-0000-000000000000"
}
```

Then the import blocks. Each `id` is the exact format that resource's own
`ImportState` expects — verified against `internal/provider` for this guide,
and given in full in the reference table below.

```terraform
import {
  to = circleci_project.api
  id = "github/acme/api"
}

import {
  to = circleci_pipeline_definition.build
  id = "11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222"
}

import {
  to = circleci_trigger.on_push
  id = "11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222/33333333-3333-3333-3333-333333333333"
}

import {
  to = circleci_context.build
  id = "${data.circleci_organization.acme.id}/44444444-4444-4444-4444-444444444444"
}

import {
  to = circleci_context_environment_variable.registry_token
  id = "44444444-4444-4444-4444-444444444444/REGISTRY_TOKEN"
}
```

The pipeline definition and trigger ids are UUIDs you get from
`circleci_pipeline_definitions` and `circleci_triggers` (above) once you know
the project's UUID — which, at this point in an import, you do not have yet
either. In practice you either already know it (from the CircleCI web
application's URL, which embeds it) or resolve it first with a `data
"circleci_project"` block keyed by the same slug, before writing the rest of
the `import` blocks.

Run generation:

```shell
terraform plan -generate-config-out=generated.tf
```

What comes back, in outline:

- `circleci_project.api` — every setting toggle omitted. See "What to expect"
  below; this is by design, not a bug in generation.
- `circleci_pipeline_definition.build` — fully populated: nothing on this
  resource is secret or left deliberately unmanaged.
- `circleci_trigger.on_push` — fully populated for a `github_app` trigger. If
  this had instead been a `schedule` or `webhook` trigger, two narrow,
  documented exceptions apply — see "Secrets" below.
- `circleci_context.build` — fully populated; nothing about a context itself
  is secret.
- `circleci_context_environment_variable.registry_token` — `context_id` and
  `name` populated, `value` **absent**. This is the hole "Secrets" below is
  about, and it is the one place in this worked example the generated
  configuration is not something you can apply as-is.

Fill in that one hole — with `value_wo` and an ephemeral value, ideally, not a
retyped `value` — and `terraform plan` against the rest is empty.

## Reference: import ID formats

Every format below comes from the resource's own `ImportState` in
`internal/provider`, cross-checked against its `## Import` doc section.
`org_id` and `organization_id` are interchangeable wherever both appear in this
provider (see `org_id_deprecation.go`); the table uses whichever the source
sets by name.

| Resource | Import ID |
|---|---|
| `circleci_project` | `vcs-type/org-name/repo-name` (the project slug) |
| `circleci_project_settings` | Same project slug |
| `circleci_project_environment_variable` | `project_slug/env_var_name` |
| `circleci_checkout_key` | `project_slug/fingerprint` (MD5, colon-separated) |
| `circleci_webhook` | `scope_id/webhook_id` |
| `circleci_pipeline_definition` | `project_id/pipeline_definition_id` |
| `circleci_trigger` | `project_id/pipeline_definition_id/trigger_id` |
| `circleci_context` | `organization_id/context_id` |
| `circleci_context_environment_variable` | `context_id/env_var_name` |
| `circleci_context_restriction` | `context_id/restriction_id` |
| `circleci_organization` | `organization_id` |
| `circleci_organization_settings` | `organization_id` |
| `circleci_organization_contacts` | `organization_id` |
| `circleci_group` | `organization_id/group_id` |
| `circleci_group_membership` | `organization_id/group_id` |
| `circleci_project_group` | `organization_id/project_id/group_id` |
| `circleci_oidc_custom_claims` | `organization_id` (org-level), or `organization_id/project_id` (project-level) |
| `circleci_audit_log_config` | The config's own `id`, alone |
| `circleci_budget` | `org_id` (organization-level), or `org_id/project_id` (per-project) |
| `circleci_storage_retention` | `org_id`, alone |
| `circleci_runner_resource_class` | `namespace/name` (the `resource_class` string itself) |
| `circleci_runner_token` | `resource_class/token_id`, e.g. `namespace/name/token_id` |
| `circleci_orb_namespace` | `organization_id/namespace_name`, or `organization_id/namespace_id` |
| `circleci_orb` | `namespace/orb` (fully qualified name), or the orb's UUID |
| `circleci_orb_version` | The orb version's UUID, alone |
| `circleci_url_orb_allow_list_entry` | `organization/entry_id` (split on the *last* slash — a slug organization has one of its own) |
| `circleci_config_policy_bundle` | `owner_id`, alone (assumes the `config` policy context), or `owner_id/policy_context` |
| `circleci_config_policy_settings` | Same as `circleci_config_policy_bundle` |
| `circleci_otel_exporter` | `organization_id/exporter_id` |
| `circleci_notification_channel_config` | The config's own `id`, alone |
| `circleci_notification_integration_status` | The integration's `id`, alone |
| `circleci_notification_preferences` | `user` (the caller's own preferences), or `organization_id/project_id` |
| `circleci_ios_signing_certificate` | The certificate's `id`, alone |
| `circleci_ios_signing_config` | `organization_id/config_id` |

## Secrets, honestly

CircleCI never discloses a secret it already holds, on any route — not the one
that reads it back, and not the one that lists many of them at once. That is
true independent of Terraform; import does not change what the API is willing
to say, so it inherits the gap.

| Attribute | Resource | What import leaves it as |
|---|---|---|
| `value` | `circleci_context_environment_variable` | `null` — never returned, on any route |
| `value` | `circleci_project_environment_variable` | `null` — the API only ever returns a masked form (`xxxx1234`), and this provider does not treat that as the real value |
| `signing_secret` | `circleci_webhook` | `null` — masked on every response |
| `certificate_blob`, `certificate_password` | `circleci_ios_signing_certificate` | `null` — there is no read route for either, at all |
| `provisioning_profiles[*].blob` | `circleci_ios_signing_config` | `null` — the list route reports only `file_name` |
| `headers` | `circleci_otel_exporter` | `null` — every value comes back as the placeholder `xxxx` |
| `token` | `circleci_runner_token` | `null` — the runner API discloses a token's value exactly once, at creation |

A generated configuration has a hole at exactly each of these. `terraform plan`
against it fails — most of these attributes participate in an "exactly one of
`x` or `x_wo`" validator, so a config supplying neither is invalid, not merely
incomplete.

**The good answer is not to retype the secret from memory or from wherever you
last saw it.** Point the practitioner at the write-only form and an ephemeral
value instead — the secret was never in this provider's state to begin with,
and with this path it never needs to be:

```terraform
ephemeral "vault_kv_secret_v2" "registry" {
  mount = "secret"
  name  = "circleci/registry"
}

resource "circleci_context_environment_variable" "registry_token" {
  context_id = circleci_context.build.id
  name       = "REGISTRY_TOKEN"

  value_wo         = ephemeral.vault_kv_secret_v2.registry.data["token"]
  value_wo_version = 1
}
```

Every secret-bearing resource in the table above has a write-only counterpart —
`value_wo`, `signing_secret_wo`, `certificate_blob_wo` plus
`certificate_password_wo`, `provisioning_profiles_wo`, `headers_wo` — each
paired with a version counter to tell Terraform when to send a fresh value
(one counter each, except the certificate pair, which shares a single
`certificate_wo_version` for both fields, since a `.p12` and the password that
decrypts it are one rotatable unit). [Managing secrets](./managing-secrets)
covers all of them, including where each is available and how the version
counters behave; use it once the hole above is the thing you are filling in.

### The first apply after filling the hole is not the same for every resource

Supplying the missing secret is not uniformly a quiet update. Two different
things can happen, and which one applies depends on the resource:

- **In place, silently.** `circleci_context_environment_variable` (a `PUT`
  upsert) and `circleci_webhook`'s `signing_secret`/`signing_secret_wo` (a
  full-replace `PUT`) write the new value without recreating anything. The risk
  here is the opposite of destructive: because there is still no read to
  compare against, a configured value that is merely *wrong* — not missing —
  overwrites the real secret with no error and no warning.
- **Destroy and recreate.** `circleci_project_environment_variable`,
  `circleci_otel_exporter`, and both credential attributes on
  `circleci_ios_signing_certificate` and `circleci_ios_signing_config` all
  force replacement the moment the secret is supplied: `value` carries
  `RequiresReplace` directly, and on the `_wo` spelling it is
  `value_wo_version` that does — either way, going from the `null` import left
  behind to a real value is a change to a `RequiresReplace` attribute like any
  other. The very next `terraform plan` after you add the secret plans a
  **replace** — not an empty plan, and not an in-place update. For the two iOS
  signing resources this is documented on their own pages with a `!>` danger
  callout and confirmed against a real plan
  (`TestAccIOSSigningCertificateResource_ImportForcesReplacement` and its
  sibling on `circleci_ios_signing_config`); it follows from the same
  mechanics on the other two.

Neither behavior is a defect to work around — they are what each API's actual
update route allows, in one case an upsert and in the other nothing at all.
But they are different enough in consequence that it is worth knowing, before
you fill in that first missing secret, which class the resource you are
importing falls into.

### One more generated-config hole that has nothing to do with secrets

`circleci_trigger`'s `event_source_schedule_attribution_actor` has the same
"generated config does not plan cleanly" shape as the secrets above, for an
unrelated reason: a `schedule` trigger is created with the alias `system` or
`current`, but read back as the actor's resolved literal id, which is all
`-generate-config-out` has to write. The literal id fails this provider's own
validator on the next `plan` — a clear error, not a silent wrong value, but
still something to fix by hand before applying. See the "Every other attribute
round-trips" note on `circleci_trigger`'s own `## Import` section for this one
and its sibling caveat on webhook triggers' `event_source_web_hook_url`
(redacted when the importing token lacks permission to see it).

## What to expect on the first plan after import

Most resources in this provider plan **empty** immediately after import, with
no configuration changes required beyond what generated the `import` block in
the first place. A smaller set leaves attributes `null` **on purpose**, so that
importing does not silently start managing something nobody configured. That
is not a bug to route around; it is the same design as `circleci_project`'s
settings, applied everywhere the same shape recurs:

| Resource | What is left `null`, and why |
|---|---|
| `circleci_project` | All nine settings toggles (`auto_cancel_builds`, `build_fork_prs`, `build_prs_only`, `disable_ssh`, `forks_receive_secret_env_vars`, `set_github_status`, `setup_workflows`, `write_settings_requires_admin`, `pr_only_branch_overrides`) — every one is `Optional`, none is adopted from a read, so a configuration naming none of them plans empty against the imported state. |
| `circleci_project_settings` | Every setting it manages, for the identical reason — this resource exists specifically for a project Terraform did not create, so adopting values nobody configured would be worse here, not better. |
| `circleci_organization_settings` | All 15 toggles (`enable_ai_agents` through `is_user_checkout_keys_disabled`) — same pattern, one organization-wide resource instead of one per project. |
| `circleci_notification_preferences` | `updates` — it manages a sparse, configuration-chosen set of rows rather than the whole preference matrix, so import leaves it empty rather than claiming ownership of every row CircleCI reports. |
| `circleci_orb` | `category_ids` — left `null` (unmanaged) so that categories set through the CircleCI UI are not silently reconciled away the first time Terraform touches an imported orb. The orb's actual categories stay readable through the computed `categories` attribute regardless. |
| `circleci_runner_resource_class` | `organization_id`/`org_id` — the runner API's resource class representation carries no organization field to read one back from. Supplying one after import is a **benign, one-time, in-place update** (this resource does not force replacement on an organization change), unlike the settings toggles above, which stay null until you choose to manage them and never force anything either way. |

Add a setting to your configuration only once you have decided this resource
should manage it. Adding one that already holds the value CircleCI reports is
a no-op; adding one with a different value writes it on the next apply, same
as it would for a resource you created rather than imported.

Everything else round-trips through import cleanly — with one narrow, timing
related exception worth naming rather than treating as a defect:
`circleci_context_restriction`'s `name` is resolved by CircleCI
*asynchronously*, out of band, and is genuinely absent immediately after a
`create`. Import is unaffected, because import always performs a fresh `Read`
rather than relying on a `create` response — by the time you import a
restriction, CircleCI has had time to resolve its name.

## What cannot be imported at all

**Ephemeral resources hold no state.** `circleci_ephemeral_runner_token` and
`circleci_usage_export` are created fresh on every `Open` and torn down again
on `Close` — that is the entire reason they are safe to hand a credential to.
There is nothing in a Terraform state file to import *into*: this is not a gap
in this provider's implementation, it is what "ephemeral" means in Terraform
core, and no provider can offer `terraform import` for one.

If what you actually need is for a runner token to survive between runs — the
usual reason someone reaches for "import" here — the answer is the *resource*
next to the ephemeral one, `circleci_runner_token`, not an import of the
ephemeral kind. Note its own gap while you are there: an imported
`circleci_runner_token` has an empty `token`, because the runner API never
discloses a token's value after the response that created it. If you need the
value itself, create a new token; see [Self-hosted runners](./self-hosted-runners).

## Related

- [Getting started](./getting-started) — the same five objects, created rather
  than imported.
- [How CircleCI's objects relate](./object-model) — the map of what owns what,
  useful when an import ID needs an id you have to resolve first.
- [Managing secrets](./managing-secrets) — the write-only attributes this guide
  points at, in full, including the ones no import touches at all.
- [Migrating from a community CircleCI provider](./migrating-from-community-providers)
  — a related but different problem: state that already exists under another
  provider's resource types, rather than no Terraform state at all.
