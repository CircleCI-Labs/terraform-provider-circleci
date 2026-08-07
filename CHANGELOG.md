# Changelog

## Unreleased

### BREAKING CHANGES

* **`circleci_oidc_custom_claims.audience` is now a set rather than a list.**
  CircleCI does not preserve the order the audience was submitted in and reports
  it back in an order of its own choosing. Unlike ordinary refresh noise, this
  attribute round-trips through the API on every `Read`, so a reordering API
  response produced a **permanent** diff no `apply` could ever settle — the same
  bug fixed for `events` and `pr_only_branch_overrides` in 0.5.0, and the same
  fix: see that release's BREAKING CHANGES entry for the mechanics.

  **No state migration is needed and nothing already in state changes.** A list
  and a set of the same element type share one JSON encoding, so existing state
  decodes as a set untouched; the schema version stays at 0 and no
  `UpgradeState` was added. `TestListToSetNeedsNoStateUpgrade` now covers this
  attribute alongside the three it already proved.

  What does break is a configuration that treats `audience` as ordered —
  `audience[0]`, `element(...)`, or anything relying on the order surviving. It
  never did survive, so such a configuration was already producing a plan that
  never converged.

### BUG FIXES

* **`circleci_orb.categories` could show a spurious diff, or fail an update
  outright with "Provider produced inconsistent result after apply".** The
  registry gives no ordering guarantee for the categories an orb belongs to,
  and `categories` is a `Computed` list with no plan modifier, so the framework
  carries its value in state forward as the planned one whenever some other
  attribute (for example `is_listed`) is what triggers an update. When `Update`
  then rebuilt the list from a fresh API response ordered differently from the
  one `Create` had written, the apply's actual result disagreed with what was
  planned.

  `categories` is now sorted by name (falling back to id to break a tie) every
  time it is built, so the order the API happens to answer with can no longer
  disagree with what is already in state. This is a description-only schema
  change — the element type is unchanged, so no state migration is needed.

  Every existing test managed only one category, which cannot show a
  reordering at all; the regression test uses two.

## 0.5.0 (2026-08-06)

The provider goes from 11 resources and 10 data sources to **35 resources, 66 data
sources, 2 ephemeral resources and 3 provider functions**, and stops depending on
`circleci-sdk-go`.

### SECURITY

* **`circleci_webhook` never sent `signing_secret` or `verify_tls`.**
  `circleci-sdk-go` tags those fields `json:"signing-secret"` and
  `json:"verify-tls"` (hyphenated), while the CircleCI webhook API reads
  `signing_secret` and `verify_tls`. The API ignores keys it does not recognise, so
  **every webhook created or updated by this provider at v0.4.0 or earlier had no
  signing secret at all**, however carefully one was configured, and TLS
  verification silently took the server-side default. `terraform apply` reported
  success throughout.

  The signing secret is what lets a receiver distinguish a genuine CircleCI delivery
  from a forged request. If you configured one and your receiver verifies
  signatures, it was rejecting every delivery. If your receiver does *not* verify,
  treat it as having accepted unauthenticated input for the period concerned.

  **What to do:** re-apply with this version. The remediation is a no-op in your
  configuration — the correct value is now sent — but you cannot tell from
  `terraform plan` that it was ever missing, which is why this note exists.

  Fixed by migrating the resource onto the provider's own API client.
  `TestWebhookResourceUnit_SecretAndVerifyTLSReachTheWire` asserts both that the
  correct keys are sent, on create *and* on rotation, and that the hyphenated ones
  are absent, so this cannot silently regress.

  Note `circleci_webhook`'s `signing_secret` still cannot be read back — the API
  masks it. That is a separate, cosmetic issue.

### SECURITY (dependencies)

* **14 reachable vulnerabilities are fixed**, found by adding a reachability scan
  (`task vulncheck`, using `govulncheck`) rather than by version matching.

  Seven were in the **Go standard library** — `crypto/tls`, `crypto/x509`,
  `net/http`, `net/textproto` and `html/template` — reachable from the provider's
  own HTTP client and from `providerserver`. Fixed by moving the `toolchain`
  directive from `go1.26.1` to `go1.26.5`; the CI image moved with it.

  The rest were module-level: `google.golang.org/grpc` (an **authorization bypass**
  via a missing leading slash in `:path`, reachable from `providerserver.Serve`),
  plus `golang.org/x/net` and `golang.org/x/text`.

  Worth noting how this was missed: Dependabot reported 43 alerts, but against the
  default branch's dependency set, and the *reachable* set on this branch was
  different. `govulncheck` reports only what the code can actually call, which is
  both narrower and more actionable. It now runs in CI alongside the linter, so a
  provider binary cannot ship with reachable TLS or x509 vulnerabilities again
  without someone overriding a failing build.

### DEPENDENCIES

* **`github.com/CircleCI-Public/circleci-sdk-go` is removed entirely.** The provider
  now speaks to the CircleCI API through its own client (`internal/circleci`).

  This is not housekeeping. **Eight bugs in this release were traced to that SDK**, and
  they were structural rather than incidental — a wrapper could not have fixed them:

  * hyphenated JSON tags against a snake_case API that ignores unrecognised keys, so
    fields were silently never sent or permanently empty. **Three separate instances**:
    `signing-secret`/`verify-tls` on webhooks (the security issue above),
    `public-key`/`created-at` on checkout keys, and `created-at` on project environment
    variables. Three of one mistake in one library is why this was a removal rather
    than a patch
  * untyped errors (every failure collapsed to one formatted string), which forced
    drift detection to match on the text `"404"` — and that also matches a 5xx whose
    body happens to contain it, **silently removing live resources from state**
  * the API version baked into the client's base URL, making "v3 on Cloud, v2 on
    Server" inexpressible, and a hardcoded `https://circleci.com` that made project
    creation impossible against CircleCI Server
  * `Configure` receiving a narrow service rather than a client, so `circleci_pipeline_definition`
    could not be deployment-gated *at all* — the type carried no deployment information
  * fields simply absent from the SDK's structs, making settings such as
    `build_prs_only` unreachable from Terraform no matter what the provider did

  Wire shapes were re-derived from what the API actually accepts and returns rather than
  ported from the SDK, since porting would have carried the tag bugs across intact.
  `CircleCI-Public/circleci-cli`'s `internal/apiclient` was the main reference — MIT and
  actively maintained — plus what the API itself accepts and returns.

  The SDK had zero releases, zero tags and no maintainer; this provider was effectively
  its only consumer.

### DEPRECATIONS

* **"Pipeline" now always says which pipeline it means.** CircleCI identifies two
  different things with a UUID, and this provider used one word for both:

  | Concept | Route | What it is |
  |---|---|---|
  | Pipeline **definition** | `/projects/{project_id}/pipeline-definitions` | Where to check out, where to find configuration, which config file |
  | Pipeline **run** | `/pipeline/{id}` | One execution of a definition, which spawns workflows and jobs |

  Colloquially "pipeline" means the *run*, so naming the definition resource
  `circleci_pipeline` aimed the familiar word at the unfamiliar concept. It made this
  read correctly while being wrong, with both values being UUIDs so nothing rejected it:

  ```terraform
  data "circleci_pipeline_run_workflows" "w" {
    run_id = circleci_pipeline.nightly.id # a definition id, not a run id
  }
  ```

  Three names shipped in v0.4.0 and are therefore **deprecated, not removed** — they
  still work and emit a warning:

  | Old | New | How to migrate |
  |---|---|---|
  | `circleci_pipeline` (resource) | `circleci_pipeline_definition` | Add a `moved {}` block |
  | `circleci_pipeline` (data source) | `circleci_pipeline_definition` | Edit the type name |
  | `circleci_trigger.pipeline_id` | `pipeline_definition_id` | Edit the attribute name |

  The resource is the only one holding state, so it is the only one needing `moved {}`.
  The provider implements `ResourceWithMoveState`, so the state entry is **re-addressed
  rather than destroyed** — the definition keeps its id and its triggers stay attached.
  `TestPipelineResourceRename_MovedBlockDoesNotDestroyTheDefinition` asserts an empty
  plan, an unchanged id, and zero `DELETE` requests reaching the API. Requires Terraform
  1.8 or later for cross-type `moved` blocks; on older versions keep the old name.

  Switching `circleci_trigger` from `pipeline_id` to `pipeline_definition_id` also plans
  as no change — both attributes are `Optional+Computed` and mirrored, so the trigger is
  not replaced. Set exactly one.

  See the [Renaming pipeline resources and data sources](docs/guides/renaming-pipeline-types.md)
  guide.

* **Renamed without deprecation, because they never shipped.** These existed only in
  unreleased builds, so there is no compatibility shim. If you were tracking an
  unreleased version, update them directly.

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

  Recorded because the first implementation got this wrong in an instructive way: it gave
  *every* rename the full deprecation treatment, which would have shipped brand-new data
  sources with already-deprecated attributes — telling practitioners not to use something
  they had never seen, and creating removal work for nobody's benefit. Two of them could
  not have worked at all: `pipelines` and the nested `pipeline_id` are `Computed`, and the
  framework only raises `DeprecationMessage` for a value present in *configuration*, so
  the warning could never fire. See `DESIGN.md`, "Deprecate only what shipped".

* **`organization_id` is deprecated in favour of `org_id`**, which matches CircleCI's
  own naming. This is **not a breaking change**:

  * Existing configurations keep working, unchanged and indefinitely. They gain a
    deprecation warning on plan, nothing more.
  * **Switching to `org_id` does not replace anything, and does not touch state.** No
    `moved {}` blocks, no `terraform state` commands, no schema upgrade.
  * Set exactly one of the two. Both together is an error, since they would be
    ambiguous if they disagreed.
  * `organization_id` will be removed in a future major release.

  Both names are accepted on **37 resources and data sources**, and on the two
  data sources where the organization is a read-only *output*
  (`circleci_project`, `circleci_ios_signing_certificate`) both are reported.

  Worth recording why this took care rather than being a one-line schema addition.
  `organization_id` carries `RequiresReplace` on twelve resources, because CircleCI
  has no route that moves an object between organizations. Implemented the obvious
  way — `org_id` as a second `Optional` attribute — a practitioner who followed the
  deprecation notice would remove `organization_id`, Terraform would plan it as
  `null`, see a change, and **destroy and recreate the resource**. On
  `circleci_project` that deletes the project and its build history. The
  "non-breaking" migration would have been more destructive than the breaking rename
  it was meant to avoid.

  That failure is reproducible: reverting to the naive shape fails the migration test
  with `expected NoOp, got action(s): [delete create]`. Two things prevent it, and
  both are needed — `Computed`, so the departing attribute retains its prior value
  rather than planning `null`, and `RequiresReplaceIfConfigured` rather than
  `RequiresReplace`, so a null configuration value never forces replacement while a
  genuine organization change still does.

  A third piece was needed for `circleci_runner_resource_class`, the one resource
  where changing the organization does *not* replace: without plan-time
  reconciliation, changing it produced a plan no `Update` could satisfy
  (`organization_id = B` alongside `org_id = A`). It reconciles both names from
  configuration in `ModifyPlan`.

  Every migrated type has a test asserting the switch is a **no-op plan**, and all of
  it — attributes, validators, resolution and reconciliation — lives in
  `internal/provider/org_id_deprecation.go`, so removing the old name later is one
  file and a compiler error list rather than an audit of forty schemas.

### BREAKING CHANGES

* **`circleci_trigger` import IDs now take three segments:
  `project_id/pipeline_id/trigger_id`** (was `project_id/trigger_id`).

  This only affects `terraform import`; no state migration is needed and nothing
  already in state changes. The reason is that the old form could not produce working
  state: a trigger is *created* under a pipeline definition but *read* under the
  project, and the read response carries no reference back to the definition — so
  `pipeline_id`, a required attribute, stayed null after every import and the next plan
  had no way to converge short of hand-editing state. Supplying the definition id is
  the only way to import a trigger usefully.

  The two-segment form now reports an error explaining this rather than silently
  importing something broken.

* **`build_fork_prs = true` now requires `forks_receive_secret_env_vars` to be set
  explicitly**, on both `circleci_project` and `circleci_project_settings`. A
  configuration that enables fork builds without naming it fails at validate time with
  an error that explains the exposure.

  This is breaking only in that a configuration which used to plan now does not. It is
  deliberate: the provider no longer writes settings a configuration does not mention
  (see BUG FIXES), so an unset `forks_receive_secret_env_vars` takes CircleCI's default
  — which is **`true` on a private project**. Enabling fork builds without deciding that
  question would hand the project's environment variables, secrets and build cache to
  anyone who can open a pull request. CircleCI gates the exposure on both settings, so
  the check fires for exactly that combination and nothing else. Set the value you want;
  `false` keeps secrets out of fork builds.

* **Three attributes holding unordered collections are now sets rather than lists:**
  `events` on `circleci_webhook`, and `pr_only_branch_overrides` on `circleci_project`
  and `circleci_project_settings`. The matching data source attributes changed with
  them (`circleci_webhook`, `circleci_webhooks`).

  **No state migration is needed and nothing already in state changes.** A list and a
  set of the same element type share one JSON encoding, so existing state decodes as a
  set untouched; the schema version stays at 0 and no `UpgradeState` was added.
  `TestListToSetNeedsNoStateUpgrade` drives the provider's real
  `UpgradeResourceState` RPC with prior list-shaped state to prove it rather than
  assert it.

  What does break is a configuration that treats either attribute as ordered:
  `events[0]`, `element(...)`, or anything relying on the order surviving. It never
  did survive — see BUG FIXES — so such a configuration was already producing a plan
  that never converged. Use `for` / `contains` / `tolist(...)` instead.

* **Changing `pipeline_definition_id` (or the deprecated `pipeline_id`) on
  `circleci_trigger` now destroys and recreates the trigger.** It used to plan an
  in-place update and do nothing at all.

  The definition id is a path segment on the create route and appears nowhere else:
  `PATCH /api/v2/projects/{project_id}/triggers/{trigger_id}` has no field for it, and
  the `GET` does not return one. So editing the attribute produced a PATCH the API
  accepted and ignored — `terraform apply` reported success, state recorded the new
  value, and **the trigger was still attached to the old pipeline definition**. No
  refresh could catch it either: with no definition in the read response, the provider
  carries the value forward from state, so state confidently reported a definition the
  API had discarded.

  What to expect now: `terraform plan` shows `# forces replacement`, and applying it
  **deletes the trigger and creates a new one**. The new trigger has a **new id**, and
  for a `webhook` event source a **new `event_source_web_hook_url`** — so anything
  posting to the old URL has to be reconfigured. Any pipeline runs in flight from the
  old trigger are unaffected; nothing else about the trigger is lost, because a trigger
  holds no history of its own.

  This is the right trade — silently discarding a change the practitioner asked for is
  worse than making the cost visible — but it is a behaviour change, so it is here
  rather than in BUG FIXES. Renaming `pipeline_id` to `pipeline_definition_id` is
  unaffected: that is still not a change and still replaces nothing
  (`TestTriggerResourceUnit_SwitchingPipelineAttributeIsNoop`), and neither is the
  first plan after upgrading from 0.4.0, whose state predates
  `pipeline_definition_id` entirely (`TestTriggerPlanUpgradingFrom04StateDoesNotReplace`).

* **`circleci_trigger` now validates per-provider attribute combinations at plan time
  instead of during apply**, and one combination it used to reject is now accepted.

  Rules such as "`parameters` is only valid for `schedule`", "`github_app` requires
  `event_source_repo_external_id`" and "`event_preset` must be omitted for `webhook`"
  need nothing from the API to decide, but they lived in `Create` and `Update`, so
  `terraform plan` succeeded and `terraform apply` failed — after review, and for
  anyone running plan and apply as separate stages, after the point of no return. They
  now run in `ValidateConfig`, and `event_source_provider` and `event_preset` have
  `OneOf` validators built from one exported list each
  (`circleci.TriggerEventSourceProviders`, `circleci.TriggerEventPresets`), so a typo
  is caught with the accepted values named.

  Breaking in two directions, both small. A configuration that used to reach `apply`
  before failing now fails earlier, with a diagnostic naming the attribute rather than
  the resource. And two rules changed:

  * `event_source_provider = "github_oauth"` **now works.** It was documented, and
    rejected at apply by a `default` arm that listed the other four providers.
  * `event_preset` is **no longer required** for `github_app` and `github_server`. The
    old check ran the preset through a validity test that an empty string fails, making
    it mandatory in code while every piece of documentation in this repository — and
    the API — called it optional. It is optional now, and still required for
    `github_oauth`, where only `all-pushes` and `only-build-prs` are accepted.

  One rule was dropped as unimplementable rather than moved: `Create` also tested
  `event_source_web_hook_url` for null, which could never fire (the attribute is
  Computed, so it is unknown in a plan and null in every configuration).

Nothing else in this release is breaking. In particular, the renames required by
CircleCI's v3 API conventions (`organization_id` → `org_id`, `pipeline` → `run`) are
deliberately **not** here; they are batched into a planned 1.0 with a state migration
and a `moved {}` guide, so one upgrade absorbs all of them instead of several releases
each breaking something.

### FEATURES

Access control and organization management:

* **New resource:** `circleci_group`, `circleci_project_group`
* **New resource:** `circleci_organization_settings`
* **New resource:** `circleci_organization_contacts` — manages an
  organization's technical (primary) and security contact email lists via
  `GET`/`PUT /api/private/organization/{org_id}/contacts`, an unofficial route
  with no published specification. CircleCI Cloud only.
* **New resource:** `circleci_storage_retention` — manages how many days
  CircleCI retains an organization's build cache, workspace data and job
  artifacts via `GET`/`PUT /private/orgs/{org_id}/storage-retention-controls`,
  another unofficial route with no published specification. CircleCI Cloud
  only. CircleCI clamps a value outside the organization's plan-enforced
  bounds rather than rejecting it, so this resource always reads the record
  back after writing it and warns when the stored value differs from what was
  configured.
* **New resource:** `circleci_budget` — manages a CircleCI spend budget, for
  an organization or for one project within it, via
  `GET`/`PUT /private/orgs/{org_id}/budgets` and
  `DELETE /private/orgs/{org_id}/budgets/{budget_id}`, another unofficial
  route with no published specification. CircleCI Cloud only. `credits` is
  the only field the write route accepts — `enforcement_type` (`warn` or
  `block`) is reported read-only, since there is nowhere to send a change to
  it; it can only be set in the CircleCI web UI.
* **New data source:** `circleci_budgets`
* **New resource:** `circleci_group_membership` — manages the full member list
  of a CircleCI group. This was briefly implemented against the public-looking
  `/api/v2/organizations/{org_id}/groups/{group_id}/users` family and removed
  after a live probe showed it 404ing for a group that demonstrably exists (the
  published spec confirms it, with a per-route host override pointing at
  internal-only traffic). It is restored here against
  `GET`/`POST /private/ciam/orgs/{org_id}/groups/{group_id}/users`,
  `add-users` and `delete-users` — the routes CircleCI's own web application
  uses for its group management UI, plus CircleCI's org-migration tooling,
  which adds users to a group in production the same way. CircleCI Cloud only,
  and only for `circleci` type (standalone) organizations, same as
  `circleci_group`.
* **New data source:** `circleci_group`, `circleci_groups`,
  `circleci_project_groups`, `circleci_group_membership`,
  `circleci_organization_settings`
* `circleci_context_restriction` now accepts `type = "group"` in addition to
  `project` and `expression`

Project configuration:

* **New resource:** `circleci_project_settings` (previously a data source only),
  `circleci_checkout_key`
* **New data source:** `circleci_checkout_keys`, `circleci_project_settings`

Orbs:

* **New resource:** `circleci_orb_namespace`, `circleci_orb`, `circleci_orb_version`,
  `circleci_url_orb_allow_list_entry`
* **New data source:** `circleci_orb`, `circleci_orbs`, `circleci_orb_namespace`,
  `circleci_orb_version`, `circleci_orb_categories`,
  `circleci_url_orb_allow_list`

Governance and compliance:

* **New resource:** `circleci_config_policy_bundle`,
  `circleci_config_policy_settings`, `circleci_oidc_custom_claims`,
  `circleci_otel_exporter`, `circleci_audit_log_config`
* **New data source:** `circleci_otel_exporters`, `circleci_audit_log_configs`,
  `circleci_audit_log_access`

Notifications:

* **New resource:** `circleci_notification_channel_config`,
  `circleci_notification_preferences`, `circleci_notification_integration_status`
* **New data source:** `circleci_notification_channel_config`,
  `circleci_notification_channel_configs`, `circleci_notification_integrations`,
  `circleci_notification_links`

iOS code signing:

* **New resource:** `circleci_ios_signing_certificate`, `circleci_ios_signing_config`
* **New data source:** `circleci_ios_signing_certificate`,
  `circleci_ios_signing_certificates`, `circleci_ios_signing_configs`

Runners:

* **New data source:** `circleci_runners`, `circleci_runner_resource_classes`,
  `circleci_runner_tokens`, `circleci_runner_task_counts`

Discovery, deploys and observability:

* **New data source:** `circleci_github_app_installation`,
  `circleci_github_app_repository`, `circleci_github_app_repositories`,
  `circleci_user`, `circleci_user_collaborations`, `circleci_catalog_offerings`,
  `circleci_job`, `circleci_workflow`, `circleci_workflow_jobs`,
  `circleci_pipeline_run`, `circleci_pipeline_run_config`,
  `circleci_pipeline_run_values`, `circleci_pipeline_run_workflows`,
  `circleci_deploy_component`,
  `circleci_deploy_components`, `circleci_deploy_environment`,
  `circleci_deploy_environments`, `circleci_deploy_settings`,
  `circleci_insights_summary`, `circleci_insights_workflows`,
  `circleci_insights_flaky_tests`

  `circleci_pipeline_run_workflows` closes a chain that was previously unusable:
  `circleci_workflow` and `circleci_workflow_jobs` both need a workflow ID, and
  nothing in the provider could produce one. It is now
  `circleci_pipeline_run` → `circleci_pipeline_run_workflows` → `circleci_workflow_jobs`.

Plural list data sources — there was not a single one before this release, despite
every API entity having a `List`:

* **New data source:** `circleci_contexts`, `circleci_context_restrictions`,
  `circleci_context_environment_variables`, `circleci_webhooks`,
  `circleci_project_environment_variables`

Ephemeral resources and functions — both interfaces were asserted on the provider
and returning empty lists:

* **New ephemeral resource:** `circleci_runner_token`, `circleci_usage_export`
* **New function:** `project_slug`, `parse_project_slug`, `orb_ref`

### ENHANCEMENTS

* **Documentation is now verified rather than hoped for.** The registry renders the
  `docs/` directory from the release tag, so a wrong example ships until the next
  version. Nothing checked them before this release: the test suite never touched
  `examples/`, and `terraform fmt` only proves HCL parses.

  Five independent checks each found broken examples on the same day — an attribute
  that does not exist on `circleci_trigger`, a `policy_context` value the schema
  rejects, `file()` calls reading absent files, a data source attribute renamed
  months ago. Every one was syntactically perfect.

  So the examples are now checked by machine:

  * `task validate-examples` builds the provider and runs `terraform validate`
    against all 96 example directories, in CI, hermetically — no network, no
    credentials, and the CircleCI environment variables explicitly unset so a
    failure cannot be a missing token in disguise.
  * Four guard tests in `internal/provider/examples_test.go`: every example
    directory has the entry filename `tfplugindocs` actually reads; every
    `{{ tffile }}` path resolves; every registered type has an example; and every
    example file is referenced by a template.

  That last guard was written before the fix it motivated, and reported exactly the
  21 files that were affected — a useful reminder that a guard nobody has watched
  fail is not yet a guard.

  Fixed along the way: **29 example files that were never rendered** (wrong filename,
  or no template referenced them, so pages showed an unvalidated inline copy while
  the good file sat unused), **45 attribute assignments still using the deprecated
  `organization_id`** so every copied example warned immediately, and four examples
  that could not have worked at all.

* **Four new guides**, joining the three migration guides:
  [Getting started](docs/guides/getting-started.md) walks from nothing to a running
  pipeline in dependency order; [CircleCI object model](docs/guides/object-model.md)
  maps how the objects relate and why a pipeline definition is not a pipeline run;
  [Managing secrets](docs/guides/managing-secrets.md) covers what reaches Terraform
  state and how to keep secrets out of it; and
  [Self-hosted runners](docs/guides/self-hosted-runners.md) covers namespace →
  resource class → token.

  The provider index now opens with a complete working configuration rather than
  provider boilerplate, and every page has a hand-written template — two previously
  rendered as bare attribute lists.

  Also corrected: `circleci_otel_exporter` claimed it worked on CircleCI Server
  because its route is v2. That inference is disproven inside this provider —
  `circleci_pipeline_definition` is v2 and unavailable on Server — so the page now
  says unverified. The Cloud/Server matrix carries a new note that **the whole Server
  column is reasoned rather than measured**, since no Server installation has been
  available to test against. That caveat was missing everywhere.

* **New provider attribute `deployment`** (`cloud` | `server`, default `cloud`).
  CircleCI Server does not route the v3 API, so resources that require it are
  unavailable there. They now fail at **plan** time with an explicit diagnostic
  instead of failing mid-apply with an opaque HTTP 404 — a CI plan check catches the
  mistake before anything is applied. Destroy is deliberately exempt, so a resource
  stranded in state by a deployment change stays removable.
* `host` now takes a bare origin. The previously documented
  `https://circleci.com/api/v2` still works — the version suffix is stripped
  automatically — but emits a warning. This is what lets one provider speak v3 on
  Cloud and v2 on Server.
* Every provider attribute, resource and data source attribute now carries a
  `MarkdownDescription`. `docs/index.md` previously rendered with no descriptions at
  all.
* `CIRCLE_HOST`, `CIRCLE_TOKEN` and `CIRCLE_RUNNER_HOST` are documented.
* `circleci_project` gained `build_prs_only`, unreachable before because
  `circleci-sdk-go`'s settings struct has no field for it.
* Each resource's documentation now states its Cloud/Server availability, its
  required organization type, and the API route behind it. The README carries a
  two-axis compatibility matrix separating "does the VCS support this" from "has the
  provider implemented it".
* Two migration guides: from the community providers (`mrolla`, `kelvintaywl`,
  `SectorLabs`), and from legacy scheduled pipelines to schedule triggers.
* **`circleci_context_environment_variable` and
  `circleci_project_environment_variable` accept `value_wo`**, a write-only
  alternative to `value` (Terraform 1.11 or later): the secret is sent to CircleCI
  and then discarded, appearing in neither state nor the plan file. Set exactly one
  of the two; `value` is unchanged and not deprecated. Because nothing derived from
  the value is stored, `value_wo_version` is required alongside it and must be
  incremented to rotate the secret — a change to `value_wo` on its own is not a
  change Terraform can see.
* **`circleci_webhook` accepts `signing_secret_wo`**, the same write-only treatment
  for the webhook signing secret (Terraform 1.11 or later), with
  `signing_secret_wo_version` required alongside it. Set exactly one of
  `signing_secret` and `signing_secret_wo`; `signing_secret` is unchanged and not
  deprecated (it becomes `Optional` rather than `Required`, so a configuration
  setting neither is still refused, now by a validator rather than by the schema). A
  rotation updates the webhook in place — it is not recreated.

  One detail is worth stating because getting it wrong is a live bug in another
  provider: the write-only secret is sent on **every** write the resource makes, not
  only on the one that bumped the version. CircleCI's update route is a full-replace
  PUT, so a body without `signing_secret` deletes the secret server-side, and there
  is nothing to recover it from — the API only ever returns a mask. Gating the send
  on the version is `hashicorp/terraform-provider-vault#2900`, where exactly that
  wiped `token_reviewer_jwt` whenever an unrelated field changed and broke
  Kubernetes auth logins.
  `TestWebhookWriteOnly_UnrelatedUpdateStillSendsTheSecret` renames a webhook and
  asserts the resulting PUT still carried the secret, so it cannot regress quietly.
* **`circleci_ios_signing_certificate` accepts `certificate_blob_wo` and
  `certificate_password_wo`** (Terraform 1.11 or later), which is the most valuable
  of these three: `certificate_blob` is the private half of an Apple code-signing
  identity, and on the state-backed path it sits in Terraform state in cleartext, so
  anyone who can read the state file can sign iOS builds as the organization. On the
  write-only path nothing is persisted, and the pair can be fed from an `ephemeral`
  block so the certificate never lands in a `.tfvars` file either. Set exactly one
  of `certificate_blob` and `certificate_blob_wo`; both spellings keep their
  password, and neither is deprecated (`certificate_blob` and
  `certificate_password` become `Optional` rather than `Required`, so a
  configuration setting neither blob is still refused, now by a validator).

  Rotation is driven by a single `certificate_wo_version` covering both write-only
  values, rather than one counter per attribute as AWS and Azure spell it. A `.p12`
  and its password are one rotatable unit — the password decrypts that specific
  file, so re-exporting a certificate always produces a new pair — and two counters
  would make an invalid combination expressible: bump the blob's and not the
  password's, and the provider would upload a new certificate with the old password,
  which CircleCI accepts and every later build fails on. The convention is really
  one trigger per rotatable unit; the Kubernetes provider's separate
  `data_wo_revision` and `binary_data_wo_revision` exist because those *are*
  independent secrets.

  Bumping the version replaces the resource rather than rewriting it in place,
  unlike the other two write-only pairs. That introduces no new concept: the
  certificate API has no update route, so every configurable attribute already
  forced replacement. It is also load-bearing — `Update` on this resource is a
  no-op, so an in-place plan would report success having uploaded nothing.
  `TestIOSSigningWriteOnly_SameRequestAsCertificateBlob` compares what the API
  received on both paths field by field, and
  `TestIOSSigningWriteOnly_RotationNeedsAVersionBump` covers both halves of the
  version contract.
* **`circleci_ios_signing_config` accepts `provisioning_profiles_wo`** (Terraform
  1.11 or later), a write-only alternative to the whole `provisioning_profiles`
  list, with `provisioning_profiles_wo_version` required alongside it. Set exactly
  one of the two; `provisioning_profiles` is unchanged and not deprecated (it
  becomes `Optional` rather than `Required`, so a configuration setting neither is
  still refused, now by a validator rather than by the schema).

  It is a parallel list rather than a write-only `blob` inside the existing one
  because the framework does not allow the latter: from
  `resource/schema/list_nested_attribute.go` in terraform-plugin-framework v1.19.0,
  a write-only nested attribute whose children are not all write-only is rejected at
  `GetProviderSchema` time with "Every child attribute of a WriteOnly nested
  attribute must also have WriteOnly set to true". The other way round is the same
  problem from the other side: Terraform Core requires every write-only value to be
  null in the response, and a nested object cannot be half-nulled. The alternative
  workaround — hoisting `file_name` and `blob` out of the nested block into
  top-level attributes — is breaking and caps the resource at one profile, so it was
  not taken.

  Consequently **`file_name` is write-only on that path too** and leaves state
  alongside `blob`. Nothing depends on it being there: `Read` never set it (the
  API's list response reports only profile names, which the resource deliberately
  does not fold back in), importing never recovered it, `certificate_file_name` and
  `certificate_type` come from the paired certificate, and the
  `circleci_ios_signing_configs` data source reads profile names from CircleCI
  rather than from this resource's state.

  One counter covers the whole list, following the same "one trigger per rotatable
  unit" rule as `certificate_wo_version` above: `provisioning_profiles` already
  carries `RequiresReplace` and a minimum size of 1, so adding, removing, renewing
  or reordering a profile is one indivisible change. A per-entry counter would have
  to live inside the nested object, where it would itself be write-only and
  therefore useless — nothing would persist it to compare against. Bumping the
  version replaces the resource, because there is no update route.
* **`circleci_otel_exporter` accepts `headers_wo`** (Terraform 1.11 or later), a
  write-only alternative to `headers`, which typically carries the OTLP collector's
  credentials, with `headers_wo_version` required alongside it. `WriteOnly` is
  permitted on a map attribute — only *set* nested attributes and set blocks are
  prohibited — so this one needed no restructuring. Bumping the version replaces the
  exporter, exactly as editing `headers` already did, since there is no update
  route.

  **Set at most one of `headers` and `headers_wo`, not exactly one**, unlike the
  other four pairs. `headers` has always been `Optional` and an exporter with no
  headers at all is ordinary — two of the exporters in this resource's own
  documented example have none — so `ExactlyOneOf` would have started rejecting
  working configurations. The validator is `Conflicting` instead.

  **The write-only path gives up the one kind of drift this resource could detect.**
  CircleCI answers every read with the placeholder `xxxx` for each header value but
  returns the header *names* in full, and on the `headers` path the provider
  compares the returned names against the names in state, so a header added or
  removed outside Terraform is visible even though a changed value is not. On the
  `headers_wo` path there are no names in state to compare against and a refresh has
  no access to configuration, so neither is detected. The provider therefore leaves
  `headers` null after a read on that path rather than adopting the API's map:
  adopting it would write the placeholder into a state-backed attribute that forces
  replacement, and every later plan would want to recreate the exporter forever.
  `TestOTelExporterWriteOnly_RefreshLeavesHeadersNull` and
  `TestOTelExporterWriteOnly_HeaderAddedOutsideTerraformIsInvisible` pin both halves
  of that, so the documentation cannot drift away from the behaviour.

  Neither of these two resources can reproduce
  `hashicorp/terraform-provider-vault#2900` — the bug described under
  `circleci_webhook` above — because neither has an update route at all: every write
  is a create. The secret is still sent on every write rather than being gated on the
  version, so that safety does not depend on the API staying that way.
* **`circleci_context_environment_variable` now detects a value changed outside
  Terraform.** The API returns no value on any route and its `truncated_value` is
  unusable for the purpose (rotating a secret while keeping its last four characters
  leaves it identical), so the resource compares timestamps: `updated_at` records
  what CircleCI reported the last time Terraform wrote the variable, the new
  `remote_updated_at` records what it reports now, and the next apply re-asserts the
  configured value when the second is later. Both paths, `value` and `value_wo`, are
  covered.

### BUG FIXES

* **`oss` was sent on every project create and settings update, and the API rejects
  it.** `oss` is read-only on v2: `PATCH /api/v2/project/{slug}/settings` with
  `{"advanced":{"oss":false}}` answers `400 Unexpected field 'advanced.oss'.` — and it
  rejects the *whole* request, so one unwritable field failed every write. The same
  request without `oss` succeeds. The published API reference documents `oss` as part of
  the request body, and the `GET` returns it, which is why it looked writable.

  It is now `Computed`-only on `circleci_project` and `circleci_project_settings` — read
  from the API, never sent — and `ProjectSettings.MarshalJSON` drops it unconditionally
  so it cannot be reintroduced by accident. The "CircleCI did not apply the oss setting"
  diagnostic is gone with it: it blamed a repository for not being open source when the
  real cause was that no write path exists. Set `oss` in the CircleCI web application.

  Every mocked test passed throughout, because the fakes accepted `oss`. They now reject
  it exactly as the API does.

* **`circleci_project` wrote `false` for every setting a configuration left out.** Each
  toggle in `Create` was guarded by `if !plan.X.IsNull()`, but these attributes are
  `Optional+Computed` and Terraform plans an omitted one as **unknown**, not null — so
  `IsNull()` was false, every guard was taken, and `ValueBoolPointer()` on an unknown
  value yields a pointer to `false`. Every toggle was therefore written as `false` on
  create whatever the configuration said.

  Two of those defaults were actively wrong: `set_github_status` defaults to **`true`**
  at CircleCI, so every Terraform-created project silently stopped reporting commit
  status, and `pr_only_branch_overrides` defaults to the repository's default branch, so
  it was cleared. An unconfigured setting is now left out of the request entirely and
  CircleCI's own default applies; the value it chose is read back into state. The real
  defaults are tabled in the documentation for both resources.

* **`circleci_project`: `pr_only_branch_overrides` sent quoted branch names.** The
  resource used `attr.Value.String()`, which renders Terraform's *display* form, so
  `main` reached the API as `"main"` — with the quotes. Likely the cause of the
  reported branch-override failures.

* **`events` and `pr_only_branch_overrides` showed a change on every plan, for ever,
  with nothing to apply.** Both are unordered collections on the CircleCI side, and
  the API answers with them in an order of its own choosing. Verified live:

  ```
  PATCH pr_only_branch_overrides ["zebra","alpha","main","beta"]
  → GET  pr_only_branch_overrides ["zebra","main","alpha","beta"]
  ```

  stable across subsequent reads, but not the order it was given. Declared as
  Terraform *lists*, the provider compared the configured order against the returned
  order and planned an update every single run — and on `circleci_project`, where the
  attribute is `Computed`, the apply failed outright with "Provider produced
  inconsistent result after apply".

  Both are now sets, on `circleci_webhook`, `circleci_project` and
  `circleci_project_settings`. See BREAKING CHANGES for what that means for a
  configuration, and note that `circleci_project_settings`'s data source already
  reported the attribute as a set — the resources had simply drifted from it.

  The whole fake-backed test suite passed throughout, because every fake echoed the
  submitted order straight back: the one behaviour the real API does not have. The
  fakes now answer in a different order, deliberately, so an order-sensitive
  regression fails immediately.

* **`circleci_webhook` did not validate event names.** The attribute's description
  has always claimed the valid values are `workflow-completed` and `job-completed`,
  but nothing enforced it, so a typo cost a round-trip and came back as an opaque
  HTTP 400. The names now come from one list in the API client, shared by the
  validator and the generated documentation.
* **`circleci_project` could not create a project on CircleCI Server.**
  `circleci-sdk-go` sent the required v1.1 follow request to a hardcoded
  `https://circleci.com`, ignoring the configured host, so creation against a Server
  installation either failed or silently followed a project on Cloud. It now honours
  `host`. The follow is also correctly skipped for standalone
  (`circleci/<uuid>`) organizations, which follow the project as part of creating it.
* **`circleci_organization` destroy deleted organizations it had merely adopted.**
  Create is a find-or-create for VCS-backed organizations, but destroy issued an
  unconditional `DELETE` — which tears down VCS connections and deletes every
  project and all build history. Destroy now mirrors create: an adopted organization
  is released from state, and only one the provider genuinely created is deleted.
* **A malformed project slug panicked the provider process.** Three unchecked
  `strings.Split(slug, "/")` indexings are now a validating parse that returns a
  diagnostic.
* **Drift detection silently deleted live resources from state.** Absence was
  detected with `strings.Contains(err.Error(), "404")`, which also matches a 5xx
  response whose body happens to contain "404". Absence is now a typed check.
* **`circleci_checkout_key.public_key` was always empty.** The struct tags were
  `public-key` and `created-at`; the API sends `public_key` and `created_at`.
* **`circleci_runners` decoded a bare JSON array** where the API returns
  `{"items": [...]}`.
* **`circleci_oidc_custom_claims` produced a permanently inconsistent result.** The
  API normalises `ttl` on write (`90m` is stored as `1h30m0s`), so every apply
  failed with "Provider produced inconsistent result after apply". `ttl` now uses
  semantic equality, so `90m`, `1h30m` and `5400s` all compare equal to the stored
  form.
* **Every config policy that existed read as absent.** `GetPolicyDocument` decoded
  the single-document route as if it returned the bundle route's name-keyed map.
* **An orb version could not be resolved by reference.** `filter[ref]` requires a
  fully qualified `namespace/orb@version`; a bare version was being sent, and the
  server ignores `filter[orb_id]` when `filter[ref]` is present.
* **A missing group answers HTTP 403, not 404**, so drift detection never worked for
  groups. Note the provider deliberately does *not* treat 403 as "gone" in general:
  the conflation is intentional anti-enumeration on CircleCI's side, and treating a
  permission loss as a deletion would let the provider recreate live objects.
* **Contexts answer 403 for absence too, and nothing modelled it.** Every route that
  addresses a context by id resolves that id in a preliminary step which maps *every*
  failure — deleted, wrong organization, or no permission — to **403**, before the
  route's own logic runs. Context drift detection
  therefore never worked. `Read` now reports a diagnostic naming all three causes rather
  than silently removing the context from state, because a token that merely lost
  permission must not cause Terraform to recreate a live context and its environment
  variables. `Delete` treats 403 and 404 alike as already-gone.
* **`circleci_project_environment_variable.created_at` was always empty.**
  `circleci-sdk-go` tagged it `json:"created-at"` against the API's `created_at`.
* **`circleci_context.organization_id` produced a silent, unapplied diff.** It was
  `Required` with no `RequiresReplace` and an empty `Update`, so changing it reported
  success while moving nothing. There is no API route that moves a context between
  organizations, so it now forces replacement.
* **`circleci_pipeline_definition` fixes enabled by dropping the SDK** (all four previously
  characterized in tests as known-broken): `project_id` and
  `config_source_repo_external_id` now force replacement instead of planning an in-place
  update that silently does nothing or targets the wrong project; a definition deleted
  outside Terraform now produces a clean recreate plan instead of a permanent refresh
  error; and both `circleci_pipeline_definition` and `circleci_trigger` can now be gated off
  CircleCI Server at plan time, which was impossible while `Configure` received an SDK
  service carrying no deployment information.
* **`circleci_runner_resource_class` never sent `org_id`.** Note the API
  ignores it anyway — it derives the organization from the resource class's namespace and
  the caller's permissions — but the schema requires it, so it is now sent and
  documented.

### NOTES

* **`circleci_schedule` is deliberately not implemented.** Scheduling is now
  expressed as a `circleci_trigger` with `event_source_provider = "schedule"`. The
  legacy schedule API works only for `github` (OAuth) and `bitbucket`
  organizations, so modelling it would add a resource unavailable to standalone
  organizations and every GitHub App project. A migration guide is included.
* **Some capabilities have no API and cannot be managed by any tool:** VCS
  connection setup, account creation, API token creation, SSO/SAML, audit log
  *retrieval*. The account and VCS steps are browser consent flows by design, which
  is the structural reason a CircleCI organization cannot be stood up end to end
  from Terraform.
* `circleci_github_app_repository`, `circleci_github_app_repositories` and
  `circleci_audit_log_config` sit on routes CircleCI does not publish in its OpenAPI
  spec. They are documented as unpublished and subject to change. The GitHub App
  routes are the only way to resolve `owner/repo` to the numeric `external_id` that
  `circleci_pipeline_definition` and `circleci_trigger` require.
* `API-COVERAGE.md` is a route-by-route inventory of the CircleCI API and what this
  provider does with each route, built from the routes CircleCI actually serves.
  `DESIGN.md` records why the provider is shaped the way it is.
