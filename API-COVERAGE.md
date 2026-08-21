<!-- Copyright (c) CircleCI -->
<!-- SPDX-License-Identifier: MPL-2.0 -->

# API coverage

Every route CircleCI's public API service exposes, and what this provider does with
it. The point of this file is to make "we cover the API" **checkable** instead of
asserted: if a route is missing here, nobody has looked at it.

## How this inventory was built, and what it does not cover

Routes were taken from **what the API actually serves**, not only from the published
OpenAPI spec, because the two do not match in either direction:

- The spec **omits routes that exist** — the GitHub App repository routes are absent
 from it despite being served. Group membership looked like the same story — its
 routes are absent from the spec too — but turned out to be the opposite case: see
 "Groups and access" below.
- The spec **describes routes that are never served** — `.../projects/{project_id}/groups/{group_id}`
 is specified, and implemented, but not reachable. That is why
 `circleci_project_group`'s Delete drops state with a warning instead of erroring.

Coverage was therefore checked against the routes CircleCI actually serves,
cross-checked against the published v2 spec (79 unique paths, 114 method+path
combinations, none marked deprecated) and against the API's real behaviour for status
codes and field names.

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
| `POST /organizations/:org_id/groups/:group_id/users` | not exposed on the public host — see below |
| `POST /organizations/:org_id/groups/:group_id/remove_users` | not exposed on the public host — see below |
| `GET /organizations/:org_id/groups/:group_id/users` | not exposed on the public host — see below |
| `GET /organizations/:org_id/projects/:project_id/groups` | `circleci_project_groups` |
| `POST /organizations/:org_id/projects/:project_id/groups` | `circleci_project_group` |
| `POST /organizations/:org_id/projects/:project_id/groups/:group_id/update-role` | `circleci_project_group` |

Group membership was briefly implemented as `circleci_group_membership` against
these public-looking routes, then removed (see `CHANGELOG.md`). All three
membership routes exist, but only the four anchored group and project-group
patterns above are reachable on the public host — the membership paths
(`.../groups/:group_id/users`, `.../groups/:group_id/remove_users`) answer only on a
separate
ingress and answer 404 on the public host, even for a group that demonstrably
exists. This was verified live: creating a group (200), then `GET`ting its
membership (404), then deleting the same group cleanly (204). Groups themselves
and project-group role grants are unaffected — both go through routes that are
anchored in the public table — so groups remain creatable and grantable through
this (public) API regardless.

`circleci_group_membership` is since **restored**, but against a different route
family entirely: CircleCI's private origin (`/private/ciam/orgs/{org_id}/groups/
{group_id}/users`, `add-users`, `delete-users` — see
`internal/circleci/group_membership.go` and `private.go`), not this one. Nothing
above changes — the public `.../groups/:group_id/users` family is still
unreachable, and still not what the resource uses.

**All three private-origin member operations were re-verified live against a real
standalone organization: list, add and remove all work with a plain personal token.**
`GET .../users` returns only `{"items": [...]}` — no `count`, and no
`next_page_token`, confirming that this route does not paginate at all (as opposed
to the org-level `/groups` route, which advertises pagination it does not
implement). Every member the response carries came back with `created_at` as the
Go zero time (`0001-01-01T00:00:00Z`) rather than a real timestamp, even for a
member added moments earlier — `updated_at`, not decoded by this provider, held
the real one. Treat `GroupMember.CreatedAt` as decorative.

**Groups and project-group grants behave differently on a `circleci` (standalone)
organization than on a `github`/`bitbucket` one, confirmed live on all four fixture
organizations (`gh-app-cci-1`, `gitlab-test` standalone; `gh-oauth-cci-1`,
`gh-oauth-cci-2` classic):**

* `GET .../groups` answers `200` with an empty `items` array on *every* organization
  type, including both classic fixtures — an empty list does not mean groups are
  unsupported there.
* `POST .../groups` (create) answers `403 Permission denied.` on both classic
  fixtures, confirmed with a token that demonstrably held full admin access to
  those organizations (it created and deleted a context there in the same
  session). `circleci_group` is therefore standalone-only in practice, not merely
  by documentation.
* `GET .../projects/{project_id}/groups` (project-group list, the read path behind
  `circleci_project_group` and `circleci_project_groups`) answers
  `400 Endpoint is not supported for this organization.` on a classic
  organization's project — a third status, distinct from both the `200` above and
  the `403` below.

**Absence is not 404 for these routes.** A single group, fetched by id, answers
`403 Permission denied.` for a group just deleted through the same token, for a
group id that never existed, and for a group requested under a bogus organization
id — all indistinguishable from a genuine permission problem, and confirmed
live and repeatably (create → 200, delete → 200, immediate re-`GET` → 403, never
404). `DELETE` on an already-deleted group answers the same 403. The project-group
list route answers the same way for a bogus organization or project id under an
otherwise-valid (standalone) organization: `403`, not the `404` an earlier version
of this file and of `project_group_resource.go`'s comments assumed. The provider's
handling was already safe either way — it never drops a resource from state on an
ambiguous signal — but the diagnostic wording and the fakes have been corrected to
stop asserting a status code that was never observed.

**Assigning a group to a project, and changing its role, both survive a real
`terraform apply` cleanly**: `circleci_group`, `circleci_group_membership` and
`circleci_project_group` were run end-to-end against `gh-app-cci-1` (create,
`terraform plan` after apply, a role change applied *in place* — confirmed
`update in-place`, not a replace — `terraform state rm` + `terraform import` for
both `circleci_group_membership` and `circleci_project_group`, and destroy).
Every plan after apply or import was empty. Destroying `circleci_group_membership`
actually removed the member from the group on the server; destroying
`circleci_project_group` left the grant live on the server exactly as its warning
says, confirmed by re-listing the project's groups afterward.

**The revoke route project-group grants are documented as needing genuinely does
not exist, confirmed live two ways**: `DELETE .../projects/{project_id}/groups/{group_id}`
(the documented per-group form) answers `404 {"message": "Not Found"}`, and
`DELETE .../projects/{project_id}/groups` with a `{"group_ids": [...]}` body (the
shape a previous investigation believed was implemented further in) answers
`404 {"message": "Route Not Found."}`. The grant was still present in the list
after both attempts. Neither shape works; `circleci_project_group`'s Delete
dropping state with a warning rather than erroring is the only honest option.

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
| `GET /projects/:project_id/pipeline-definitions` | `circleci_pipeline_definitions` |
| `POST /projects/:project_id/pipeline-definitions` | `circleci_pipeline_definition` |
| `GET /projects/:project_id/pipeline-definitions/:id` | `circleci_pipeline_definition` |
| `PATCH /projects/:project_id/pipeline-definitions/:id` | `circleci_pipeline_definition` |
| `DELETE /projects/:project_id/pipeline-definitions/:id` | `circleci_pipeline_definition` |
| `GET /projects/:project_id/pipeline-definitions/:id/triggers` | `circleci_triggers` |
| `POST /projects/:project_id/pipeline-definitions/:id/triggers` | `circleci_trigger` |
| `GET /projects/:project_id/triggers/:trigger_id` | `circleci_trigger` |
| `PATCH /projects/:project_id/triggers/:trigger_id` | `circleci_trigger` |
| `DELETE /projects/:project_id/triggers/:trigger_id` | `circleci_trigger` |
| `POST /project/:provider/:org/:project/pipeline/run` | **no** — triggering a run is a runtime action, not desired state |
| `POST /pipeline/search` | **no** — searches pipeline *runs*, not definitions, so it would not help `circleci_pipeline_definitions`. Selecting runs by criteria is a query, not desired state |
| `GET /pipeline/:pipeline_id/values` | `circleci_pipeline_run_values` |
| `GET /pipeline/{pipeline-id}/workflow` | `circleci_pipeline_run_workflows` — this was the missing middle of a chain exposed at both ends. `circleci_workflow` and `circleci_workflow_jobs` both need a workflow ID that nothing else could produce, so the only way in was to already know one. The chain is now `circleci_pipeline_run` → `circleci_pipeline_run_workflows` → `circleci_workflow_jobs` |
| `POST /pipeline/continue` | **no** — dynamic-config continuation, called from inside a running job |
| `GET /project/{project-slug}/pipeline/mine` | **no** — scoped to the calling token's own pipelines, which is not a declarable property |
| `POST /projects/{project_id}/rollback` | **no** — performs a rollback. `circleci_deploy_settings` manages `rollback_pipeline_definition_id`, which *is* desired state; triggering the rollback is a runtime action |
| `POST /jobs/{job-id}/cancel`, `POST /project/{slug}/job/{n}/cancel` | **no** — runtime actions |
| `GET /insights/{project-slug}/branches` | **gap** — "all branches for a project". Bounded and arguably useful for iterating branches, unlike the rest of insights |
| `GET`/`POST /owner/{id}/context/{ctx}/decision`, `/decision/{id}`, `/decision/{id}/policy-bundle` | **no** — decision audit logs and ad-hoc policy evaluation. `circleci_config_policy_settings` covers `/decision/settings`, which is the part that is configuration |

Pipeline definitions and triggers are **not available on CircleCI Server**: a Server
installation's gateway does not forward those routes.

### Users, pipeline runs, workflows and jobs

**Correction:** these were previously listed under the "v3 — complete" heading below,
mapped to plural, v3-styled paths (`/users`, `/jobs/:id`, `/workflows/:id`, `/runs/:id`).
That was wrong. `internal/circleci/user.go`, `pipeline_run.go`, `workflow.go` and
`job.go` each say plainly, in their own doc comments, that they call v2 — and every
one of them calls `Client.GetV2`, never `GetV3`. `job.go`'s comment gives the reason
for one of the four directly: the v2 UUID route (`GET /api/v2/jobs/{id}`) has no
CircleCI Server equivalent, so the provider uses the slug-and-number route
(`GET /api/v2/project/{slug}/job/{job-number}`) instead — a real, documented v2 route,
just not the one the API reference leads with. This was found while cross-checking
this file against the Go source; see the note on the
same finding.

| Route | Provider |
|---|---|
| `GET /me`, `GET /user/:id` | `circleci_user` |
| `GET /me/collaborations` | `circleci_user_collaborations` |
| `GET /pipeline/:id` | `circleci_pipeline_run` |
| `GET /project/:slug/pipeline/:number` | `circleci_pipeline_run` (lookup by project and pipeline number) |
| `GET /pipeline/:id/config` | `circleci_pipeline_run` (`config`/`compiled_config` attributes) |
| `GET /project/:slug/job/:job-number` | `circleci_job` |
| `GET /workflow/:id` | `circleci_workflow` |
| `GET /workflow/:id/job` | `circleci_workflow_jobs` |

**Second correction, same section:** `GET /jobs/:id/artifacts` was listed as
implemented, backing `circleci_job`'s `artifacts` attribute. `circleci_job` has no
such attribute — `job.go`'s own comment on `JobParallelRun` says artifacts and test
results are deliberately not exposed, "per-build detail that would make this data
source read as a partial build log rather than a job summary," and the data source's
own schema documentation says the same thing. There is no `artifacts` field on `Job`
to attach a route to. Removed rather than corrected to a different route, because
the capability itself doesn't exist in the provider today.

Not independently re-verified in this pass, so kept separate rather than folded into
the corrected table above: the "gap"/"no" rows for per-run test results, unbounded
log streaming, workflow cancel/rerun, and run search/facet-values. They plausibly
belong to the same v2 family — test results in particular are addressed the same way
(`GET /api/v2/project/{slug}/job/{job-number}/tests` is a real, documented v2 route)
— but that was not checked against source for this correction, only the four routes
this provider actually calls were.

| Route | Provider |
|---|---|
| `GET /project/:slug/job/:job-number/tests` | **gap** — per-run test results; reporting rather than state, same reasoning as the omitted insights endpoints |
| Job log streaming (stdout/stderr, condensed) | **no** — unbounded log output would churn state on every refresh |
| Workflow cancel, rerun | **no** — runtime actions, not desired state |
| Run search, facet-values | **gap** — faceted search over run history; reporting |

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

Users, pipeline runs, workflows and jobs were previously listed here and are not:
see the corrected "Users, pipeline runs, workflows and jobs" section under v2 above.

### Orbs

| Route | Provider |
|---|---|
| `GET /namespaces`, `GET /namespaces/:id` | `circleci_orb_namespace` |
| `POST /namespaces`, `POST /namespaces/:id/rename`, `DELETE /namespaces/:id` | `circleci_orb_namespace` — `rename`'s *shape* is a real in-place update (keeps id and orbs on success), but [NET] every account tested got `403 Forbidden` from both `rename` and `delete`, on namespaces both inside and outside the calling account's own organization; CircleCI's support docs describe rename/transfer as a support-ticket process, and this investigation found no self-service delete path at all. `name` is still left as a non-replacing update — see `circleci_orb_namespace`'s docs — precisely so this 403 is a clean failed apply, not Terraform destroying the namespace to retry as a replacement. |
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

### Runners

The runner admin API serves **two competing surfaces** for resource classes and
tokens: a legacy, flat one, and a newer JSON:API-style one mounted at
`/runner/resource-classes`, `/runner/tokens` and `/runner/agents`. **The provider
deliberately stays on the legacy surface** — this is a decision, not an oversight:

| Route | Provider |
|---|---|
| `GET/POST /runner/resource`, `GET/DELETE /runner/resource/:id` | `circleci_runner_resource_class`, `circleci_runner_resource_classes` |
| `GET/POST /runner/token`, `GET/DELETE /runner/token/:id` | `circleci_runner_token`, `circleci_runner_tokens`, `circleci_runner_token` (ephemeral) |
| `GET /runner` | `circleci_runners` |

**Correction:** this previously said neither surface has an update route at all.
That is wrong for the resource-class half. The newer JSON:API-style surface
registers `POST /runner/resource-classes/{id}/update`, a partial update of the
one mutable field a resource class has (`description`) — a real, in-place update,
not a resource re-creation. It does not exist on the legacy `/runner/resource`
surface this provider actually calls, and it does not exist for tokens on
*either* surface: a token is create/list/delete only regardless of which surface
serves it, so `circleci_runner_token` having no `Update` still stands. Only
`circleci_runner_resource_class` is affected by the correction.

**Consequence: this does not change which surface the provider uses.** The three
reasons below for staying on the legacy surface are about `circleci_runners` and
about CircleCI Server, and none of them are about resource classes specifically —
moving just the resource-class family to the newer surface for `Update`'s sake
would still drop Server support for it (Server serves only the legacy surface,
update route included), so `circleci_runner_resource_class` correctly keeps
`RequiresReplace` on `resource_class` and has no `Update` today. Worth revisiting
only alongside a full migration to the newer surface, not on its own — and even
then, the update this route offers is narrow: it can rename a resource class's
`description`, never its `resource_class` value, so replacement would still be
required for the one attribute practitioners are most likely to want to change in
place.

Why the legacy surface rather than the newer one:

- **Both are served by the same API**, so this is not a
 matter of one being deprecated infrastructure — the newer routes are the
 *canonical* surface going forward, and the legacy ones are explicitly the older
 shape.
- **CircleCI Server serves only the legacy surface.** Moving would either drop
 Server support or require maintaining both.
- **Migrating would be breaking and lossy for `circleci_runners`.** The newer
 `/runner/agents` route drops `hostname`, `ip` and `last_used` — all three are
 attributes on `Runner` today — and swaps the `status` string (`"busy"`/`"idle"`)
 for a boolean `is_busy`. Adopting it would remove attributes and change a type,
 with no way to reconstruct the dropped fields from the new response.

If the legacy surface is ever withdrawn, this needs revisiting; until then, staying
on it is the only option that does not regress `circleci_runners` or drop Server
support.

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

A larger management surface does exist behind the CircleCI web application,
authenticated with a browser session rather than an API token, and most of it is
correctly described as unreachable from a Terraform provider. **One piece of it is
not**, though, and it is worth being precise about which: `PATCH
/api/v2/deploy/projects/{id}/settings`, the write half of the one settings route
already read above, answers `404` on the public path but `400` — not `404`, not
`401` — for an empty body on the service's own private origin. A route that parses
and rejects an empty body is a route that exists and authenticated the token; it is
simply not forwarded publicly the way the `GET` on the same path already is. See
the client source for the full evidence.
**This provider cannot call it today, through the path CircleCI publishes** — which
is a narrower, more actionable claim than "cannot call any of it," and the fix is
correspondingly small.

The rest of the management surface is the bigger claim, and that one still holds —
nothing found while researching the deploy-settings write path suggests release
integrations, integration tokens, environment hierarchies or component writes are
reachable by any route, public or private, with a personal token:

| Capability | Would become |
|---|---|
| Release integrations, create/read/update | `circleci_release_integration` — the connection to a deployment target |
| Integration tokens, create/list/revoke | analogous to `circleci_runner_token` |
| Environment hierarchies and their assignments | pure configuration, a natural resource |
| Component update and archive | write access to components the provider can only read |
| Deploy settings write access at organization scope, if that turns out to be a separate route from the project-scope one above | writable `circleci_deploy_settings` at that scope too |

Correctly out of scope even if they were reachable: deploy, rollback, cancel, retry,
promote, restart, scale and version-restore are runtime actions; release, status,
insights and failed-release listings are reporting; and the agent and in-job APIs
authenticate as something other than a user.

`circleci_deploy_settings` being read-only today follows directly from this: the
public proxy forwards `GET` but not `PATCH`. That is also why a customer request for
centrally managed rollback configuration cannot be satisfied today — the provider
already reads `rollback_pipeline_definition_id` and needs only the existing `PATCH`
forwarded publicly to manage it.

---

## Beyond the routes above

These are served elsewhere in the API, so they are absent from the tables above.
Each was confirmed individually.

| Area | Provider | Version |
|---|---|---|
| Contexts — see below | `circleci_context`, `circleci_contexts`, `circleci_context_environment_variable`, `circleci_context_restriction`, `circleci_context_restrictions` | v2 |
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
| Raw project SSH keys | **no** — v1.1 only, and not served on CircleCI Server; checkout keys are the supported mechanism | v1.1 |
| Test suite, impact analysis, selection data | **no** — `/api/v2/tests/*`, `/api/v2/impact-analysis`, `/api/v2/selection-data` authenticate with TaskAuth and are meant to be called *from inside a running job*, not by an operator. `GET /api/v2/projects/{id}/impact-analysis` does accept a PAT, but it reports per-run analysis | v2 |
| LLM agents, LLM gateway proxy | **no** — `/api/v2/agents/*`, undocumented, and not infrastructure to declare | v2 |
| Dev sandboxes | **no** — `/api/v2/sandbox/*` provisions ephemeral dev environments; a developer inner-loop tool | v2 |

7 of the 10 insights endpoints are omitted: they answer "what happened in this run"
with unbounded row counts that would churn state on every refresh, and two are marked
deprecated.

### Contexts

Contexts are **v2**, not v3 — a previous version of this document had them under the
v3 heading. Both `/api/v2/context*` and a v3 contexts surface exist and are
registered separately; the provider calls the v2 family, singular ("context", not
"contexts") and nested rather than flat:

| Route | Provider |
|---|---|
| `GET/POST /context`, `GET/DELETE /context/:id` | `circleci_context`, `circleci_contexts` |
| `GET /context/:id/environment-variable`, `PUT/DELETE /context/:id/environment-variable/:name` | `circleci_context_environment_variable` |
| `GET/POST /context/:id/restrictions`, `DELETE /context/:id/restrictions/:id` | `circleci_context_restriction`, `circleci_context_restrictions` |

### In the spec, but served on a different path than the rest of v2

Some routes are in the v2 spec but are not reached the same way as most of v2. They are
easy to miss for exactly that reason: checking only where the bulk of v2 lives finds
nothing.

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
with token auth.

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
