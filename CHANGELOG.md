# Changelog

## 0.5.0 (Unreleased)

The provider goes from 11 resources and 10 data sources to **31 resources, 63 data
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
  are absent, so this cannot silently regress. ([#25](../../issues/25))

  Note `circleci_webhook`'s `signing_secret` still cannot be read back — the API
  masks it. That is a separate, cosmetic issue ([#21](../../issues/21)).

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

  Wire shapes were re-derived from what the API accepts and returns rather than ported
  from the SDK, since porting would have carried the tag bugs across intact.
  `CircleCI-Public/circleci-cli`'s `internal/apiclient` was the main reference — MIT and
  actively maintained — plus the API and the API the real wire format.

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

Nothing else in this release is breaking. In particular, the renames required by
CircleCI's v3 API conventions (`organization_id` → `org_id`, `pipeline` → `run`) are
deliberately **not** here; they are batched into a planned 1.0 with a state migration
and a `moved {}` guide, so one upgrade absorbs all of them instead of several releases
each breaking something.

### FEATURES

Access control and organization management:

* **New resource:** `circleci_group`, `circleci_group_membership`,
  `circleci_project_group`
* **New resource:** `circleci_organization_settings`
* **New data source:** `circleci_group`, `circleci_groups`,
  `circleci_group_membership`, `circleci_project_groups`,
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
  `circleci_webhooks`, `circleci_project_environment_variables`

Ephemeral resources and functions — both interfaces were asserted on the provider
and returning empty lists:

* **New ephemeral resource:** `circleci_runner_token`, `circleci_usage_export`
* **New function:** `project_slug`, `parse_project_slug`, `orb_ref`

### ENHANCEMENTS

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
* **`circleci_project` could not create a project on CircleCI Server.**
  `circleci-sdk-go` sent the required v1.1 follow request to a hardcoded
  `https://circleci.com`, ignoring the configured host, so creation against a Server
  installation either failed or silently followed a project on Cloud. It now honours
  `host`. The follow is also correctly skipped for standalone
  (`circleci/<uuid>`) organizations, which follow the project as part of creating it.
  ([#5](../../issues/5))
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
  form. ([#10](../../issues/10))
* **Every config policy that existed read as absent.** `GetPolicyDocument` decoded
  the single-document route as if it returned the bundle route's name-keyed map.
  ([#11](../../issues/11))
* **An orb version could not be resolved by reference.** `filter[ref]` requires a
  fully qualified `namespace/orb@version`; a bare version was being sent, and the
  server ignores `filter[orb_id]` when `filter[ref]` is present.
  ([#12](../../issues/12))
* **A missing group answers HTTP 403, not 404**, so drift detection never worked for
  groups. Note the provider deliberately does *not* treat 403 as "gone" in general:
  the conflation is intentional anti-enumeration on CircleCI's side, and treating a
  permission loss as a deletion would let the provider recreate live objects.
* **Contexts answer 403 for absence too, and nothing modelled it.** Every route that
  addresses a context by id sits behind the API's `the context-resolution step` middleware, which
  resolves the id and maps *every* failure — deleted, wrong organization, or no
  permission — to **403**, before the route's own handler runs. Context drift detection
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
  characterized in tests as known-broken, [#26](../../issues/26)): `project_id` and
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
  provider does with each route, built from the API' own route
  registration tables. `DESIGN.md` records why the provider is shaped the way it is.
