<!-- Copyright (c) CircleCI -->
<!-- SPDX-License-Identifier: MPL-2.0 -->

# API coverage

Every route CircleCI's public API service exposes, and what this provider does with
it. The point of this file is to make "we cover the API" **checkable** instead of
asserted: if a route is missing here, nobody has looked at it.

## How this inventory was built, and what it does not cover

Routes were taken from **what the API actually serves**, not only from the published
OpenAPI spec, because the two do not match in either direction:

- The spec **omits routes that exist** — group membership and the GitHub App repository
 routes are both absent from it, which is why earlier research wrongly concluded group
 membership could not be managed as code.
- The spec **describes routes that are never served** — `.../projects/{project_id}/groups/{group_id}`
 is specified, and implemented, but not reachable. That is why
 `circleci_project_group`'s Delete drops state with a warning instead of erroring.

Coverage was therefore checked against the routes CircleCI's API services actually
register, cross-checked against the published v2 spec (79 unique paths, 114
method+path combinations, none marked deprecated) and against each API's own
behaviour for status codes and field names.

Note that the service fronting the public API is a *proxy* and does not own the whole v2
surface: contexts, checkout keys, project environment variables, insights, policies,
OIDC claims, OpenTelemetry exporters, the URL orb allow list and schedules are served
elsewhere. Those were checked separately.

**Both the v2 and v3 inventories below are complete rather than best-effort.** The
caveat worth carrying is the one above — "in the spec" and "served in production" are
different sets, so neither alone is sufficient.

Legend: **yes** implemented · **read-only** data source only · **no** deliberate
omission, reason given · **gap** known, not yet built

---

## v2 — served through the public API

### Groups and access

| Route | Provider |
|---|---|
| `GET /organizations/:org_id/groups` | `circleci_groups` |
| `GET /organizations/:org_id/groups/:group_id` | `circleci_group` |
| `POST /organizations/:org_id/groups` | `circleci_group` |
| `DELETE /organizations/:org_id/groups/:group_id` | `circleci_group` |
| `POST /organizations/:org_id/groups/:group_id/users` | `circleci_group_membership` |
| `POST /organizations/:org_id/groups/:group_id/remove_users` | `circleci_group_membership` |
| `GET /organizations/:org_id/groups/:group_id/users` | `circleci_group_membership` (data source) |
| `GET /organizations/:org_id/projects/:project_id/groups` | `circleci_project_groups` |
| `POST /organizations/:org_id/projects/:project_id/groups` | `circleci_project_group` |
| `POST /organizations/:org_id/projects/:project_id/groups/:group_id/update-role` | `circleci_project_group` |

Group membership was originally documented as web-UI-only and impossible to express
as IaC. That was wrong — the routes exist, they are just absent from the published
spec. See `DESIGN.md`.

### Projects

| Route | Provider |
|---|---|
| `POST /project/:provider/:org/:project` | `circleci_project` — see note |
| `GET /project/:provider/:org/:project/settings` | `circleci_project_settings` |
| `PATCH /project/:provider/:org/:project/settings` | `circleci_project_settings` |

Note: two create routes exist. The provider uses
`POST /organization/{orgID}/project`, which takes an organization UUID, because the
slug-based route above cannot address a standalone (`circleci/<uuid>`) organization.

### Pipelines, definitions and triggers

| Route | Provider |
|---|---|
| `GET /projects/:project_id/pipeline-definitions` | `circleci_pipelines` |
| `POST /projects/:project_id/pipeline-definitions` | `circleci_pipeline` |
| `GET /projects/:project_id/pipeline-definitions/:id` | `circleci_pipeline` |
| `PATCH /projects/:project_id/pipeline-definitions/:id` | `circleci_pipeline` |
| `DELETE /projects/:project_id/pipeline-definitions/:id` | `circleci_pipeline` |
| `GET /projects/:project_id/pipeline-definitions/:id/triggers` | `circleci_triggers` |
| `POST /projects/:project_id/pipeline-definitions/:id/triggers` | `circleci_trigger` |
| `GET /projects/:project_id/triggers/:trigger_id` | `circleci_trigger` |
| `PATCH /projects/:project_id/triggers/:trigger_id` | `circleci_trigger` |
| `DELETE /projects/:project_id/triggers/:trigger_id` | `circleci_trigger` |
| `POST /project/:provider/:org/:project/pipeline/run` | **no** — triggering a run is a runtime action, not desired state |
| `POST /pipeline/search` | **gap** — would enrich `circleci_pipelines` with server-side filtering |
| `GET /pipeline/:pipeline_id/values` | `circleci_pipeline_values` |
| `GET /pipeline/{pipeline-id}/workflow` | `circleci_pipeline_workflows` — this was the missing middle of a chain exposed at both ends. `circleci_workflow` and `circleci_workflow_jobs` both need a workflow ID that nothing else could produce, so the only way in was to already know one. The chain is now `circleci_pipeline_run` → `circleci_pipeline_workflows` → `circleci_workflow_jobs` |
| `POST /pipeline/continue` | **no** — dynamic-config continuation, called from inside a running job |
| `GET /project/{project-slug}/pipeline/mine` | **no** — scoped to the calling token's own pipelines, which is not a declarable property |
| `POST /projects/{project_id}/rollback` | **no** — performs a rollback. `circleci_deploy_settings` manages `rollback_pipeline_definition_id`, which *is* desired state; triggering the rollback is a runtime action |
| `POST /jobs/{job-id}/cancel`, `POST /project/{slug}/job/{n}/cancel` | **no** — runtime actions |
| `GET /insights/{project-slug}/branches` | **gap** — "all branches for a project". Bounded and arguably useful for iterating branches, unlike the rest of insights |
| `GET`/`POST /owner/{id}/context/{ctx}/decision`, `/decision/{id}`, `/decision/{id}/policy-bundle` | **no** — decision audit logs and ad-hoc policy evaluation. `circleci_config_policy_settings` covers `/decision/settings`, which is the part that is configuration |

Pipeline definitions and triggers are **not available on CircleCI Server**: its gateway
routes served does not forward them.

### GitHub App

| Route | Provider |
|---|---|
| `GET /github-app/organization/:org_id/installation` | `circleci_github_app_installation` |
| `GET /github-app/organization/:org_id/repositories` | `circleci_github_app_repository`, `circleci_github_app_repositories` |
| `POST /github-app/install` | **no** — installation is a browser consent flow; there is nothing for Terraform to converge on |

CircleCI marks the repository routes *"Internal / CLI-only … intentionally NOT
customer-facing"* and excludes them from the published spec. Use was approved by the
maintainer, and the documentation says so on the page.

### Orbs, namespaces and usage

| Route | Provider |
|---|---|
| `GET /orbs`, `GET /orbs/:orb_id` | `circleci_orbs`, `circleci_orb` (via v3) |
| `GET /orbs/:orb_id/badge` | **no** — a badge image URL, not state |
| `GET /orbs/:ns/:name` | **no** — serves `badges.circleci.com`, not an API route |
| `GET /namespaces/:name`, `POST /namespaces` | `circleci_orb_namespace` (via v3) |
| `POST /organizations/:org_id/usage_export_job` | `circleci_usage_export` (ephemeral) |
| `GET /organizations/:org_id/usage_export_job/:id` | `circleci_usage_export` (ephemeral) |

### Other

| Route | Provider |
|---|---|
| `POST /organizations/:org_id/projects/:project_id/circlebot/generate-config` | **no** — generates a config file with an LLM; non-deterministic, and the output belongs in the repository, not in state |
| `POST /hooks/github-app`, `POST /hooks/slack` | **no** — inbound webhook receivers, not a customer API |

---

## v3 — complete

### Users, jobs and workflows

| Route | Provider |
|---|---|
| `GET /users` | `circleci_user`, `circleci_user_collaborations` |
| `GET /jobs`, `GET /jobs/:id` | `circleci_job` |
| `GET /jobs/:id/artifacts` | `circleci_job` (`artifacts` attribute) |
| `GET /jobs/:id/tests` | **gap** — per-run test results; reporting rather than state, same reasoning as the omitted insights endpoints |
| `GET /jobs/:id/stdout`, `/stderr`, `/stdout/condensed` | **no** — unbounded log output would churn state on every refresh |
| `POST /workflows/:id/cancel`, `/rerun` | **no** — runtime actions, not desired state |
| `GET /workflows`, `GET /workflows/:id` | `circleci_workflow`, `circleci_workflow_jobs` |
| `GET /runs`, `GET /runs/:id` | `circleci_pipeline_run` |
| `POST /runs` | **no** — triggering a run is a runtime action |
| `POST /runs/search`, `GET /runs/facet-values`, `POST /runs/facet-values/search` | **gap** — faceted search over run history; reporting |

### Orbs

| Route | Provider |
|---|---|
| `GET /namespaces`, `GET /namespaces/:id` | `circleci_orb_namespace` |
| `POST /namespaces`, `POST /namespaces/:id/rename`, `DELETE /namespaces/:id` | `circleci_orb_namespace` — `rename` is a real in-place update |
| `GET /orb/packages`, `GET /orb/packages/:id` | `circleci_orbs`, `circleci_orb` |
| `POST /orb/packages` | `circleci_orb` |
| `POST /orb/packages/:id/set-listed` | `circleci_orb` (`listed`) |
| `POST /orb/packages/:id/add-category`, `/remove-category` | `circleci_orb` (`categories`) |
| `POST /orb/packages/validate` | client method, used to validate before publish |
| `GET /orb/versions`, `GET /orb/versions/:id` | `circleci_orb_version` |
| `POST /orb/versions` | `circleci_orb_version` |
| `GET /orb/versions/:id/source` | `circleci_orb_version` (`source`) |
| `POST /orb/versions/:id/promote` | **no** as a resource — promotion creates a *new* version rather than mutating one, so it has no idempotent Terraform shape. Client method exists and is tested. |
| `GET /orb/categories` | `circleci_orb_categories` |
| `POST /namespaces/import`, `/orb/packages/import`, `/orb/versions/import` | **no** — one-shot migration operations between installations, not convergent state |

### Organizations and projects

| Route | Provider |
|---|---|
| `GET /orgs`, `GET /orgs/:id` | `circleci_organization` |
| `GET /orgs/:id/settings`, `POST /orgs/:id/update-settings` | `circleci_organization_settings` |
| `GET /projects`, `GET /projects/:id` | `circleci_project` |
| `GET /projects/:id/settings`, `POST /projects/:id/update-settings` | `circleci_project_settings` |
| `GET/POST/DELETE /projects/:id/environment-variables` | `circleci_project_environment_variable` |
| `DELETE /projects/:id/dlc` | **no** — purges the Docker layer cache. A one-shot side effect with nothing to read back, so it cannot be a resource; Terraform has no primitive for "run this once". |

### Contexts

| Route | Provider |
|---|---|
| `GET/POST /contexts`, `GET/DELETE /contexts/:id` | `circleci_context`, `circleci_contexts` |
| `GET /contexts/:id/env-vars`, `POST /contexts/:id/env-vars/set`, `DELETE /contexts/:id/env-vars` | `circleci_context_environment_variable` |
| `GET/POST /context-restrictions`, `DELETE /context-restrictions/:id` | `circleci_context_restriction`, `circleci_context_restrictions` |

### Runners

| Route | Provider |
|---|---|
| `GET /runner/resource-classes`, `GET /runner/resource-classes/:id` | `circleci_runner_resource_class`, `circleci_runner_resource_classes` |
| `POST /runner/resource-classes`, `POST /runner/resource-classes/:id/update`, `DELETE …/:id` | `circleci_runner_resource_class` |
| `GET /runner/tokens`, `GET /runner/tokens/:id`, `POST`, `DELETE` | `circleci_runner_token`, `circleci_runner_tokens`, `circleci_runner_token` (ephemeral) |
| `GET /runner/agents` | `circleci_runners` |

### Notifications

| Route | Provider |
|---|---|
| `GET /notification/integrations`, `GET …/:id` | `circleci_notification_integrations` |
| `POST /notification/integrations/:id/set-status`, `DELETE …/:id` | `circleci_notification_integration_status` |
| `GET/POST /notification/preferences` | `circleci_notification_preferences` |
| `GET/POST /notification/channel-configs`, `GET/POST …/:id/update`, `DELETE …/:id` | `circleci_notification_channel_config`, `circleci_notification_channel_configs` |
| `GET /notification/links` | `circleci_notification_links` (data source only) |
| `DELETE /notification/links` | **no** — deliberately not a resource. There is no create route: a link is established by the Slack user-linking OAuth flow, a browser consent step Terraform cannot drive, and DELETE takes no id (it unlinks by criteria). A resource whose Create can never run is worse than no resource |

### Signing (iOS)

| Route | Provider |
|---|---|
| `GET/POST /signing/certificates`, `GET/DELETE /signing/certificates/:id` | `circleci_ios_signing_certificate`, `circleci_ios_signing_certificates` |
| `GET/POST /signing/configs`, `DELETE /signing/configs/:id` | `circleci_ios_signing_config`, `circleci_ios_signing_configs` |

### Analytics, deploy and other

| Route | Provider |
|---|---|
| `GET /catalog/offerings` | `circleci_catalog_offerings` |
| `POST /usage/exports`, `GET /usage/exports/:id` | `circleci_usage_export` (ephemeral) |
| `POST /analysis/tests`, `POST /analysis/jobs` | **gap** — reporting queries with unbounded result sets |
| `POST /metric/counts`, `POST /metric/distributions` | **gap** — same |
| `POST /deploy/hooks/:id/validate` | **no** — an inbound webhook ingest endpoint, not state |
| `/sidecar` (catch-all) | **no** — provisions ephemeral dev sandboxes; a developer inner-loop tool, not infrastructure to declare |
| `GET /static/openapi.yaml`, `/openapi.html`, `/live`, `/ready` | **no** — service metadata |

---

## Deploys and releases — the read API is public, the management API is not

This deserves its own section because "we cover deploys" is true and misleading at the
same time.

**The provider implements every public deploy route.** There are six, and they are all
`GET`:

| Route | Provider |
|---|---|
| `GET /deploy/components` | `circleci_deploy_components` |
| `GET /deploy/components/{id}` | `circleci_deploy_component` |
| `GET /deploy/components/{id}/versions` | `circleci_deploy_component` (`versions`) |
| `GET /deploy/environments` | `circleci_deploy_environments` |
| `GET /deploy/environments/{id}` | `circleci_deploy_environment` |
| `GET /deploy/projects/{id}/settings` | `circleci_deploy_settings` |

A management surface does exist, but it is served only to the CircleCI web
application, authenticated with a browser session rather than an API token — so **a
Terraform provider cannot call any of it.** This is a missing public API, not an
unimplemented provider feature.

What sits behind that boundary and would be worth building the moment it is reachable
with a token:

| Capability | Would become |
|---|---|
| Release integrations, create/read/update | `circleci_release_integration` — the connection to a deployment target |
| Integration tokens, create/list/revoke | analogous to `circleci_runner_token` |
| Environment hierarchies and their assignments | pure configuration, a natural resource |
| Component update and archive | write access to components the provider can only read |
| Deploy settings at project and organization scope | writable `circleci_deploy_settings` |

Correctly out of scope even if they were reachable: deploy, rollback, cancel, retry,
promote, restart, scale and version-restore are runtime actions; release, status,
insights and failed-release listings are reporting; and the agent and in-job APIs
authenticate as something other than a user.

`circleci_deploy_settings` being read-only follows directly from this: no public write
route exists. It is also why a customer request for centrally managed rollback
configuration cannot be satisfied today — the provider already reads
`rollback_pipeline_definition_id` and needs only a public write route to manage it. See
`NEEDS-FROM-MAINTAINER.md`.

---

## Beyond the public API service

These are served by the v2 API and other services, so they are absent from the
route tables above. Each was confirmed against its own handler.

| Area | Provider | Version |
|---|---|---|
| Checkout keys | `circleci_checkout_key`, `circleci_checkout_keys` | v2 |
| Webhooks | `circleci_webhook`, `circleci_webhooks` | v2 |
| Config policies | `circleci_config_policy_bundle`, `circleci_config_policy_settings` | v2 |
| OIDC custom claims | `circleci_oidc_custom_claims` | v2 |
| OpenTelemetry exporters | `circleci_otel_exporter`, `circleci_otel_exporters` | v2 |
| URL orb allow list | `circleci_url_orb_allow_list_entry`, `circleci_url_orb_allow_list` | v2 |
| Insights | 3 of 10 endpoints — `circleci_insights_summary`, `_workflows`, flaky tests | v2 |
| Deploy markers and environments | `circleci_deploy_component(s)`, `circleci_deploy_environment(s)`, `circleci_deploy_settings` | v2 |
| Audit log configs | `circleci_audit_log_config`, `circleci_audit_log_configs`, `circleci_audit_log_access` — see `DESIGN.md` | v2 |
| Follow a project | internal to `circleci_project` | **v1.1** |
| Legacy scheduled pipelines | **no** — superseded by a schedule trigger; a migration guide exists | v2 |
| Raw project SSH keys | **no** — v1.1 only and absent from the gateway inventory; checkout keys are the supported mechanism | v1.1 |
| Test suite, impact analysis, selection data | **no** — `/api/v2/tests/*`, `/api/v2/impact-analysis`, `/api/v2/selection-data` authenticate with TaskAuth and are meant to be called *from inside a running job*, not by an operator. `GET /api/v2/projects/{id}/impact-analysis` does accept a PAT, but it reports per-run analysis | v2 |
| LLM agents, LLM gateway proxy | **no** — `/api/v2/agents/*`, undocumented, and not infrastructure to declare | v2 |
| Dev sandboxes | **no** — `/api/v2/sandbox/*` provisions ephemeral dev environments; a developer inner-loop tool | v2 |

7 of the 10 insights endpoints are omitted: they answer "what happened in this run"
with unbounded row counts that would churn state on every refresh, and two are marked
deprecated in the v2 API routes served.

### Served directly, bypassing the public API service

Some routes are in the v2 spec but are not served by the service that fronts the public
API — the gateway sends them straight to their API. They are easy to miss for
exactly that reason: looking only at what fronts the public API finds nothing.

| Route | Provider |
|---|---|
| `GET`/`POST /organizations/{org_id}/users` | **no** — see below |
| `GET`/`PATCH`/`DELETE /organizations/{org_id}/users/{user_id}` | **no** — see below |
| The audit-log config routes | `circleci_audit_log_config` |
| `GET`/`POST`/`DELETE /organizations/{org_id}/projects/{project_id}/groups/{group_id}` | **not served** — specified and implemented, but not reachable. This is why `circleci_project_group`'s Delete drops state with a warning rather than erroring |
| `POST /api/v3/triggers/{trigger_id}/events` | **no** — experimental, and triggering a pipeline is a runtime action |
| The v2 org-scoped iOS signing routes | superseded by the v3 `/signing/*` routes the provider uses |

**Organization member management is deliberately not implemented.** The routes support
listing members, inviting them with a role, changing a role and removing a member — but
they are served on a host reserved for internal use rather than through CircleCI's public
API, so the provider does not depend on them. Revisit if they are ever exposed publicly
with token auth. See `NEEDS-FROM-MAINTAINER.md`.

### No API exists

Documented as out of scope because there is no endpoint to call, not because they were
skipped: VCS connection setup, account creation, API token creation
(`POST /user/token` is session-only auth — a deliberate privilege boundary), SSO and
SAML configuration, and audit log *retrieval* (the configs that control export are
manageable; the log contents are not). The account and VCS steps are browser consent
flows by design.

**Correction:** user invitations were previously listed here. They are not out of
scope — see the table above. The error came from looking only at what fronts the
public API, which does not carry those routes.

## v1.1 exposure

The provider makes exactly one v1.1 call: following a project after creating it.
There is no v2 equivalent, and an unfollowed project never runs. v1.1 is the only API
version with an active deprecation initiative, so this is the one thing in the
provider with a known expiry date.
