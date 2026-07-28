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

### BREAKING CHANGES

* None. Every change in this release is additive or a bug fix. The renames required
  by CircleCI's v3 API conventions (`organization_id` → `org_id`, `pipeline` →
  `run`) are deliberately **not** in this release; they are batched into a planned
  1.0 with a state migration and a `moved {}` guide, so that one upgrade absorbs all
  of them instead of several releases each breaking something.

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
  `circleci_pipeline_run`, `circleci_pipeline_config`, `circleci_pipeline_values`,
  `circleci_pipeline_workflows`, `circleci_deploy_component`,
  `circleci_deploy_components`, `circleci_deploy_environment`,
  `circleci_deploy_environments`, `circleci_deploy_settings`,
  `circleci_insights_summary`, `circleci_insights_workflows`,
  `circleci_insights_flaky_tests`

  `circleci_pipeline_workflows` closes a chain that was previously unusable:
  `circleci_workflow` and `circleci_workflow_jobs` both need a workflow ID, and
  nothing in the provider could produce one. It is now
  `circleci_pipeline_run` → `circleci_pipeline_workflows` → `circleci_workflow_jobs`.

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
  `circleci_pipeline` and `circleci_trigger` require.
* `API-COVERAGE.md` is a route-by-route inventory of the CircleCI API and what this
  provider does with each route, built from the API' own route
  registration tables. `DESIGN.md` records why the provider is shaped the way it is.
