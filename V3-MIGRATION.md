<!-- Copyright (c) CircleCI -->
<!-- SPDX-License-Identifier: MPL-2.0 -->

# v2 → v3 parity

What of this provider's v2 traffic can move to v3 on CircleCI Cloud while CircleCI
Server keeps using v2, what cannot move today and exactly why, and what v3 already
offers that the provider has never implemented.

This is a merge of five independent surveys, each given the same v3 OpenAPI spec and
the same instruction: read the spec text and the provider's Go source side by side,
field by field, and say what changes. They disagreed in places and hedged in others.
Both are kept rather than resolved — see "Where the surveys hedged or disagreed" —
because a survey that quietly picks a side when the spec itself is ambiguous is less
useful than one that says so.

Nothing here was verified against a live v3 endpoint. Every "PARTIAL" and every open
question below is a reading of the spec text, the same caveat `API-COVERAGE.md`
carries for the public v2 spec: the spec and what a service actually serves are not
guaranteed to match. Treat this as the list of things to check live, not as a finished
verdict.

Verdicts follow `API-COVERAGE.md`'s vocabulary, extended by one:

- **FULL** — v3 covers the field/route shape the provider needs, no loss.
- **PARTIAL** — some of the provider's current reads or writes have no v3 equivalent.
- **MISSING** — no usable v3 route exists for what the provider needs.
- **V3_ONLY** — already implemented against v3 only; nothing left on v2 to reconcile.
- **NEW** — a v3 capability the provider does not implement at all today.

No capability in this survey came back FULL or MISSING outright — the real spread is
PARTIAL (something is missing, but not everything), V3_ONLY (already done), and NEW
(unbuilt). `context-restrictions` is the one PARTIAL that reads as functionally
MISSING for anything but write — its own entry says so.

---

## Summary

| Verdict | Count | Of which blocking |
|---|---|---|
| PARTIAL | 12 | 11 |
| V3_ONLY | 6 | 0 |
| NEW | 7 | 0 |

"Blocking" means: this is not a nice-to-have gap, it removes a read or write path a
shipped resource or data source depends on today, and dual-pathing it to v3 on Cloud
as-is would regress that resource for Cloud users.

| Capability | Provider types | Verdict | Blocking |
|---|---|---|---|
| contexts (context + env vars) | `circleci_context`, `circleci_contexts`, `circleci_context_environment_variable` | PARTIAL | no — two open questions, not confirmed losses |
| context-restrictions | `circleci_context_restriction`, `circleci_context_restrictions` | PARTIAL (near-MISSING for reads) | **yes** |
| projects (core resource) | `circleci_project` | PARTIAL | **yes** |
| projects (settings) | `circleci_project_settings` | PARTIAL | no — mappable with caveats |
| projects (env vars) | `circleci_project_environment_variable` | PARTIAL | **yes** — loses `created_at` and single-item read |
| projects (new settings toggles) | — | NEW | no |
| usage export | `circleci_usage_export` | PARTIAL | **yes** |
| pipeline definitions | `circleci_pipeline_definition`, `circleci_pipeline_definitions` | PARTIAL | **yes** |
| triggers | `circleci_trigger`, `circleci_triggers` | PARTIAL | **yes** |
| runs | `circleci_pipeline_run`, `circleci_pipeline_config`, `circleci_pipeline_values` | PARTIAL | **yes** |
| workflows | `circleci_workflow`, `circleci_pipeline_run_workflows` | PARTIAL | **yes** |
| jobs | `circleci_job`, `circleci_workflow_jobs` | PARTIAL | **yes** |
| jobs — per-step detail | — | NEW | no — not Terraform-shaped |
| orbs | `circleci_orb`, `circleci_orbs`, `circleci_orb_version`, `circleci_orb_categories` | V3_ONLY | no |
| orb namespaces | `circleci_orb_namespace` | V3_ONLY | no |
| orb catalog offerings | `circleci_catalog_offerings` | V3_ONLY | no |
| orb cross-installation import | — | NEW | no — not Terraform-shaped |
| runner (canonical surface) | `circleci_runner_resource_class(es)`, `circleci_runner_token(s)`, `circleci_runners` | PARTIAL | **yes** |
| runner resource-class description update | `circleci_runner_resource_class` | NEW | no |
| iOS signing | `circleci_ios_signing_certificate(s)`, `circleci_ios_signing_config(s)` | V3_ONLY | no |
| organizations (core CRUD) | `circleci_organization` | PARTIAL | **yes** |
| organization settings | `circleci_organization_settings` | V3_ONLY | no |
| users | `circleci_user`, `circleci_user_collaborations` | PARTIAL | **yes** |
| notifications | `circleci_notification_*` (6 types) | V3_ONLY | no |
| analysis (job/test reporting) | — | NEW | no — not Terraform-shaped |
| metrics (counts/distributions) | — | NEW | no — not Terraform-shaped |
| sidecar (dev sandboxes) | — | NEW | no — not Terraform-shaped |

---

## 1. What can move to v3 on Cloud today, and in what order

Every full capability group in this survey that touches a resource still on v2 has at
least one confirmed or suspected loss — see section 2. There is no group where the
honest answer is "dual-path the whole thing now, no caveats." What follows is a
readiness order for the pieces that come closest, and a dependency-ordered plan for
the rest once the gaps in section 2 are closed. It assumes the standard shape already
established for `circleci_orb`, `circleci_orb_namespace` and the rest of the
`V3_ONLY` group: `deployment == "cloud"` calls v3, `deployment == "server"` keeps
calling v2, gated the same way `requireCloud` already gates the existing v3-only
resources.

### Phase 0 — verify, then dual-path (no API change needed)

These need a live check against v3, not a new route from CircleCI. Once verified,
each can move independently of the others.

1. **`circleci_project_settings`.** The boolean toggles map to v3's
   `update-settings` fields one-for-one by name (`autocancel_builds` →
   `enable_auto_cancel_redundant_workflows`, and seven more of the same shape). The
   spec gives every field a bare `boolean` type with no description, so the mapping
   is inferred from naming convention, not stated — confirm each pair live before
   shipping. Two things to check specifically while there: whether `is_oss` is
   actually writable in v3 (v2's `oss` is confirmed dead — the v2 `PATCH` 400s on it
   — so a writable `is_oss` would be a genuine improvement, not just parity), and
   whether v3 shares v2's known bug of silently ignoring an attempt to clear
   `pr_only_branch_overrides` to `[]`.
2. **`circleci_context` and `circleci_context_environment_variable`** (not
   `circleci_context_restriction` — see section 2). Core CRUD and the env-var list
   match what the provider reads today field for field. Two things block calling
   this done: whether `GET /contexts` (list) can be scoped to one organization at
   all — the spec documents only cursor paging, no owner filter, and `ListContexts`
   and `FindContextByName` both depend on being able to scope a listing — and
   whether `POST /contexts/{id}/env-vars/set` returns anything, since the resource
   currently reads its own write's `created_at`/`updated_at` back from that same
   call. If the write truly answers empty, moving costs an extra `GET` per write,
   not a route swap.

Both of these are additive to what the provider does today; neither is blocked on
anything but a live spec check.

### Phase 1 — the run/workflow/job chain

`circleci_pipeline_run` → `circleci_pipeline_run_workflows` → `circleci_workflow_jobs`
is one lookup chain, so treat runs, workflows and jobs as one migration unit rather
than three. This is also the one place where `API-COVERAGE.md` currently says the
provider is *already* on v3 for this chain (`circleci_pipeline_run`,
`circleci_workflow`, `circleci_job` are all listed under "v3 — complete") while the
Go source's own doc comments and route constants say v2 — see section 7. Fix the
documentation regardless of what happens with the migration; it is wrong today either
way.

Priority order within the chain, highest first: **jobs**, because the identifier the
provider's `Get(project_slug, job_number)` and the resource's whole lookup key depend
on has no v3 path at all — not renamed, absent — and the only way to an opaque job
UUID in v3 runs backward through workflow → run → pipeline UUIDs the caller may not
have. **runs** next, for the same reason at one link up the chain (no
project-slug-plus-number route, and `circleci_pipeline_config`/`circleci_pipeline_values`
have nothing to move to at all). **workflows** last within this group — its losses
(who-did-what collapsing to one `user` reference, pagination needing a follow-up `GET`
per item) are real but narrower.

### Phase 2 — pipeline definitions and triggers together

`circleci_trigger` is created against a `pipeline_id`, and pipeline definitions have
no v3 update route, so any edit to a definition's config forces a destroy+recreate
that mints a new pipeline UUID and cascades to every trigger that references it. Fix
pipeline definitions' missing update route before moving triggers, or a trigger
migration inherits a recreate hazard it didn't create. Within triggers, the schedule
type (`cron_expression`, `attribution_actor`) and the `parameters` field are the
parts with no v3 representation at all, not merely a rename — flag those to the API
team as their own line items (section 2) rather than bundling them with the
repo/webhook fields, which do map cleanly.

### Phase 3 — organization and project core CRUD

Both are blocked the same way: v3 has no create route and no delete route for either
object. List them together because an organization is a project's parent — if either
ever gets a create/delete route, do the organization first, since project creation
needs an organization id to create against. Note for whoever picks this up: v3's
organization read also drops `vcs_type`, which is exactly the field
`organizationIsStandalone` reads to decide whether `Delete` is safe to call at all —
that is not a cosmetic field to re-derive later, it is the provider's own
destroy-safety check.

### Phase 4 — usage export

Isolated — nothing else depends on it. Deliberately placed after the phases above
because the risk here is a silent one: the ephemeral resource's polling loop and its
user-facing failure message both depend on values (`state`, `error_reason`) whose v3
replacements (`phase`, `outcome`) are undocumented free-text fields with no stated
enum. A wrong assumption here doesn't fail loudly — it either polls forever or reports
the wrong outcome. Confirm both fields' exact value sets against a live v3 job before
moving this one at all.

### Phase 5 — context-restrictions, users, runner (lowest priority)

Each is narrow and none blocks anything else:

- **context-restrictions** only affects one resource pair and is otherwise isolated
  from the rest of the contexts group (phase 0).
- **users** backs one data source plus a single field lookup (resolving a workflow's
  "started by" id); nothing else in the provider depends on it.
- **runner (canonical surface)** has no urgency: the legacy surface the provider uses
  today still works, `DESIGN.md` already documents why staying on it is deliberate,
  and moving would trade a working Server-compatible surface for a Cloud-only one
  with less documented detail (see section 2). Move only if the legacy surface is
  ever actually withdrawn — the one new fact this survey adds, a genuine in-place
  `description` update on the canonical surface, is not by itself a reason to switch.

---

## 2. What cannot move, and exactly what v3 is missing

This is the part meant to become a request to the API teams. Each gap names the
field or behaviour, the route it's missing from, and the concrete consequence for
this provider — not just "this field is absent."

### contexts — `circleci_context_restriction`, `circleci_context_restrictions`

- **No single-item read.** `GET /api/v3/context-restrictions/{id}` does not exist —
  only `DELETE /{id}` does. There is no way to re-read one restriction's attributes
  after creating it, which is what `circleci_context_restriction`'s `Read` needs on
  every plan and what `terraform import` needs on day one.
- **List returns ids only.** `GET /api/v3/context-restrictions` answers
  `{data: [{id}]}` — no `restriction_type`, `match_pattern`, `created_at`, or context
  reference — despite its own description saying it lists a context's restrictions.
  No `context_id` (or any) filter is documented on the operation either, so which
  context a given call even lists is unclear from the spec.
- **`project_id` is unrecoverable.** The create response carries
  `created_at`/`match_pattern`/`restriction_type` but never `project_id`, which v2
  supplies for project-type restrictions and which `circleci_context_restriction`
  stores. Combined with the previous two points, a project-scoped restriction's
  `project_id` can only ever be known at the moment it was created — there is no way
  to read it back afterward.
- **Delete's scoping is undocumented.** `DELETE /context-restrictions/{id}`'s own
  summary says it is "scoped to the required `context_id` filter," but no
  `context_id` parameter — path or query — is defined anywhere on the operation.

### projects — `circleci_project`

- **No create route.** No `POST` exists anywhere under `/api/v3/projects`.
- **No delete route.** No `DELETE` exists anywhere under `/api/v3/projects/{id}`.
- **Read drops nearly every field the resource stores.** `GET /projects/{id}`
  returns only `{name, references.org.id}`. `slug`, `organization_name`,
  `organization_slug`, and all of `vcs_info` (`vcs_url`, `provider`,
  `default_branch`) have no v3 counterpart anywhere in the spec.
- **List is a slug resolver, not a list.** `GET /projects` requires `filter[slug]`
  and returns a bare id — there is no route to list an organization's projects.

### projects — `circleci_project_environment_variable`

- **`created_at` is gone.** Confirmed against the client: it's a real `Computed`
  attribute populated on `Create` and `Read` today. No field in any v3
  request/response schema for `/projects/{id}/environment-variables` carries it.
- **No single-variable read.** v2's get-by-name has no v3 equivalent — only
  list-all and delete-by-name exist. `Read` becomes list-and-filter, and the list
  response carries no pagination object at all (no `page` key, unlike the contexts
  env-var route), so there is no documented cap on how many variables can come back
  in one response.

### usage export — `circleci_usage_export`

- **`state` enum replaced by an undocumented one.** v2's four-value enum
  (`created`/`processing`/`completed`/`failed`), which the client's
  `isTerminalUsageExportState` switches on directly, is replaced by a free-text
  `phase` string with no enum listed in the spec. Whether `phase` carries the same
  four values, more, or fewer cannot be confirmed from the spec text.
- **`error_reason` has no v3 field.** The ephemeral resource surfaces it verbatim in
  its failure diagnostic (`"Usage export job %s failed: %s"`). The closest v3
  candidate, `outcome`, is an undescribed nullable string with no enum — whether it
  carries the same failure detail or only a coarse success/failure label is
  unconfirmed. If the latter, the specific failure reason is lost.

### pipeline definitions — `circleci_pipeline_definition`, `circleci_pipeline_definitions`

- **No update route at all.** No `PATCH` or equivalent exists anywhere under
  `/api/v3/pipelines` or `/api/v3/pipelines/{id}` — only list, create, get-by-id and
  delete. v2's update (name, description, `config.file_path`, checkout
  provider/repo) has nothing to send to.
- **Consequence:** any edit to those fields forces destroy+recreate under v3. That
  mints a new pipeline UUID, and every `circleci_trigger` addressed by that
  `pipeline_id` would need recreating too — a one-field rename today becomes a
  cascading destroy/recreate under v3 as specified.

### triggers — `circleci_trigger`, `circleci_triggers`

- **`parameters` has no v3 field anywhere** — not on the create body, the update
  body, or the get response. v2's default pipeline parameters, read and written on
  every trigger type, have nothing to move to.
- **`cron_expression` and `attribution_actor` have no v3 field.** v3's
  `event.schedule` is typed "array of integer" with no documented semantics —
  categorically different from an arbitrary cron string — and there is no field
  resembling an attribution actor on the trigger at all.
- **`event_name`** (deprecated but still read/written, used to detect drift on
  webhook triggers) has no clear v3 counterpart; the closest candidates,
  `event.type` (a discriminator) and `webhook.name`, are not the same thing.

### runs — `circleci_pipeline_run`, `circleci_pipeline_config`, `circleci_pipeline_values`

- **No number-based lookup.** No `number` field exists anywhere in the v3 run
  schema, and no route accepts a project slug plus a pipeline number. That is the
  human-facing identifier practitioners copy from CircleCI's UI — v3 only offers an
  opaque run UUID or a project-scoped list with no number filter.
- **`GetConfig` has no v3 equivalent.** Nothing under `/api/v3/runs` or
  `/api/v3/pipelines` exposes `source`/`compiled`/`setup_config`/`compiled_setup_config`
  — a full-text search of the spec for "compiled" and "setup_config" returns zero
  hits.
- **`GetValues` has no v3 equivalent.** No route anywhere exposes the flat
  `pipeline.*` values map `circleci_pipeline_values` reads today.
- **`updated_at` and `trigger_parameters` are both gone**, with only `created_at`
  and no per-run supplied-parameters field anywhere in the run or trigger schemas.
- **`state` becomes a coarser cross-product.** v2's single descriptive string
  (`created`/`errored`/`setup-pending`/`setup`/`pending`/`running`/`success`/`canceled`/…)
  is replaced by `phase` (3 values: `queued`/`started`/`ended`) crossed with
  `current_outcome` (6 values), and no v2 state string maps 1:1 onto that. In
  particular the queued-for-setup vs. queued-for-run distinction is not visible in
  `phase` alone.

### workflows — `circleci_workflow`, `circleci_pipeline_run_workflows`

- **No pipeline/run number** — only `run.id` (UUID) — and **no project slug** — only
  `project.id` (UUID). Every human-readable identifier is gone.
- **`started_by`/`canceled_by`/`errored_by` collapse into one `references.user.id`.**
  v3 cannot say who started a workflow separately from who canceled it or who caused
  an error.
- **`tag`, `max_auto_reruns` and `auto_rerun_number` have no v3 field.**
- **List returns ids only, not full objects.** `GET /api/v3/workflows` (the route
  backing `circleci_pipeline_run_workflows`) answers bare `{id}` plus a cursor; v2's
  equivalent returns full `Workflow` objects inline. Every item now needs its own
  follow-up `GET /workflows/{id}`.

### jobs — `circleci_job`, `circleci_workflow_jobs`

- **No slug-plus-number lookup.** The only identifier `circleci_job`'s `Get` and its
  documented lookup key use today has no v3 path at all. v3 addresses a job only by
  an opaque UUID, reachable only via `GET /jobs?filter[workflow_id]=...` — which
  itself requires a workflow UUID, which requires a run UUID, which requires a
  pipeline UUID. There is no v3 route taking a project slug and a human job number at
  any point in that chain.
- **No dependency graph.** v2's `Dependencies`/`Requires` — the job-graph edges that
  are the entire reason `circleci_workflow_jobs` exists — have no v3 field anywhere.
- **No `resource_class`, no `approval_request_id`/`approved_by`, no project
  slug/name/`external_url`, no organization name, no workflow name, no `web_url`, no
  explicit parallelism count, no `queued_at`.** All present on v2's `Job` and absent
  from `GET /jobs/{id}`.
- **List returns ids only**, the same N+1 pagination-shape change as workflows.

### runner (canonical surface) — `circleci_runner_resource_class(es)`, `circleci_runner_token(s)`, `circleci_runners`

- **`GET /runner/agents`'s response schema documents no attributes at all** — only
  `data:[{id}]` — unlike Signing Certificates, Orgs or Runner Tokens in the same
  spec, which do carry full attribute objects. The already-known loss (`hostname`,
  `ip`, `last_used` dropped, `status` string replaced by boolean `is_busy`) could not
  be independently re-derived from this spec version for that reason — see section 7.
- **Resource-class read shape is equally undocumented.** Both list and get-by-id
  specify `data:[{id}]`/`data:{id}` only, even though the create body requires a
  `resource_class` slug and a `description`. Whether the real response carries them
  is unconfirmed from this spec.
- **Token creation loses slug addressing.** Legacy `CreateToken` takes a plain
  `"namespace/name"` resource-class slug. The canonical surface's create body
  instead requires `references.resource_class.id` — a UUID, not the slug — so a
  caller holding only the slug needs an extra resolution call first.
- **Pagination shape changes.** Legacy list routes answer a bare
  `{"items": [...]}` with no cursor; the canonical routes answer JSON:API
  `{"data": [...], "page": {...}}`. Any migration replaces the decoding, not just the
  URL.
- **CircleCI Server does not route `/api/v3` at all**, so this is the one group
  where moving to the documented surface leaves Server on a permanently
  less-capable path, not a temporary compatibility gap.

### organizations — `circleci_organization`

- **No create route.** No `POST /api/v3/orgs` exists. v2's find-or-create for a
  VCS-backed org and genuine create for a standalone one have nothing to move to.
- **No delete route.** No `DELETE /api/v3/orgs/{id}` exists. The provider only ever
  calls delete for a standalone org it created itself — never an adopted VCS org,
  since that would delete every project and all build history — and that call has
  no v3 equivalent.
- **Read drops `slug` and `vcs_type`.** `GET /orgs/{id}` documents only
  `{id, attributes:{name}}`. `vcs_type` is not cosmetic: it is exactly the field
  `organizationIsStandalone` reads to decide whether `Delete` is even safe to call.
  Losing it from the read model removes the provider's own safety check, not just a
  display field.
- **List is id-only.** `GET /orgs` supports only `filter[slug]` and returns a bare
  id per item, so even "look up an org and get something usable" needs the by-id
  follow-up, which itself still lacks `slug`/`vcs_type`.

### users — `circleci_user`, `circleci_user_collaborations`

- **No lookup by an arbitrary user id.** The spec states outright that only
  `filter[user_id]=me` is supported. `circleci_user`'s `Get(userID)` path — used
  when, e.g., resolving a workflow's "started by" id to a user — has nothing to
  call.
- **No collaborations route at all.** `circleci_user_collaborations` (which reads
  id/VCS-type/name/slug/avatar for every org the caller can collaborate on,
  including VCS-only orgs CircleCI has never seen) has no v3 route to dual-path to,
  full stop.
- **Even the one covered case is thinner.** `GET /users` documents only
  `data:[{id}]` — `login`, `name` and `avatar_url`, all three fields `circleci_user`
  actually surfaces, are not in the schema, and there is no by-id follow-up route to
  fill them in.

---

## 3. Already on v3 — nothing to reconcile

Confirmed field-for-field against the provider's Go source; no v2 route exists for
any of these to migrate off of, or the migration already happened and the spec
matches what the client sends and decodes exactly.

| Capability | Provider types | Note |
|---|---|---|
| Orbs, orb versions, orb categories | `circleci_orb`, `circleci_orbs`, `circleci_orb_version`, `circleci_orb_categories` | Thin-list-vs-detail distinction the client already codes for matches the spec exactly. |
| Orb namespaces | `circleci_orb_namespace` | Rename is a genuine in-place update on both sides. |
| Catalog offerings | `circleci_catalog_offerings` | One spec-quality wrinkle only — see section 7. |
| iOS signing certificates and configs | `circleci_ios_signing_certificate(s)`, `circleci_ios_signing_config(s)` | No update route on either side; the provider's delete-and-recreate pattern matches the spec's absence of `PUT`/`PATCH`. |
| Organization settings | `circleci_organization_settings` | All 15 boolean settings match exactly; v3's "no PATCH, a sibling `/update-settings` verb" shape matches the provider's own comment about it. |
| Notifications (integrations, channel configs, preferences, links) | `circleci_notification_*` (6 types) | Never had a v2 route — v3-native from day one. `circleci_notification_links` is read-only, matching the deliberate no-create decision already on record. |

---

## 4. v3 capabilities the provider does not implement

### Worth building

| Capability | Route(s) | Why it's worth it |
|---|---|---|
| Runner resource-class description update | `POST /runner/resource-classes/{id}/update` | A genuine in-place update for the one mutable field on an otherwise-immutable object — exactly Terraform's `Update` shape. Currently blocked on the same canonical-surface losses in section 2, so it's a real gain, not a free one; see phase 5. |
| New project-settings toggles (`enable_ai_error_summarization`, `enable_unversioned_config`, `is_running_disabled`, possibly a writable `is_oss`) | `GET`/`POST /projects/{id}/(update-)settings` | Same shape as the existing toggles on `circleci_project_settings` — a partial-update boolean over a shared settings record. Add once (if) the rest of the projects group is viable on v3; see phase 0/3. |

### Correctly not built

| Capability | Route(s) | Why not |
|---|---|---|
| Namespace/orb/version cross-installation import | `POST /namespaces/import`, `/orb/packages/import`, `/orb/versions/import` | One-shot migration between installations, not convergent state — already excluded per `API-COVERAGE.md`. |
| Job per-step detail | `GET /jobs/{id}` (`parallel_executions[].steps[]`) | Live, per-run execution detail (exit codes, command output timestamps) that changes on every run. The provider's own comment on `job.go` already explains omitting step detail for the same reason artifacts and test results are excluded. |
| Job analysis / test analysis | `POST /analysis/jobs`, `/analysis/tests` | Unbounded reporting queries over a time window, no stable per-row identity, marked EXPERIMENTAL. `API-COVERAGE.md` already marks this a deliberate gap; the spec confirms the reasoning. |
| Metric counts / distributions | `POST /metric/counts`, `/metric/distributions` | Same shape as analysis — time-bucketed aggregates, empty buckets mean "no data yet," not "unchanged." Marked EXPERIMENTAL. Already a deliberate gap. |
| Sidecar (dev sandboxes) | `/sidecar/*` | No single-item `GET` for an instance at all, so a Terraform provider cannot refresh or detect drift for one even if it wanted to. The rest of the surface (`exec`, `ssh/add-key`) is a runtime action against an ephemeral box, not declarable state. `API-COVERAGE.md` already parks this as a deliberate non-resource; the spec supports that call. |

---

## 5. The provider's one v1.1 dependency

`internal/circleci/project.go`'s `followProject` is the provider's only remaining
v1.1 call: a standalone organization must follow a project after creating it, or the
project never runs, and v2 has no route for that at all (`API-COVERAGE.md`, "v1.1
exposure").

**v3 offers no replacement.** None of the `/api/v3/projects*` paths in this spec
include anything resembling a follow action, and v3 has no create route for a
project in the first place (section 2) — so there is currently nothing to move
`followProject` to even in principle. This dependency stays exactly as-is regardless
of what else moves to v3. Since v1.1 is the only API version with an active
deprecation initiative, this is still the one thing in the provider with a known
expiry date and no v3-side fix in sight.

---

## 6. Where the surveys hedged or disagreed

Kept as open questions rather than resolved one way, because the spec text itself
does not settle them:

- **contexts:** whether `GET /contexts` can be scoped to one organization at all is
  unconfirmed — the list operation documents only cursor paging, no owner/org
  filter — and whether the env-var write route (`POST .../env-vars/set`) returns a
  body is undocumented (only error statuses are listed).
- **context-restrictions:** the delete operation's own description claims scoping
  by a required `context_id` that is not actually a documented parameter anywhere on
  that operation — a spec-internal inconsistency, not something this survey could
  resolve either way.
- **project settings:** the field-name mapping (`autocancel_builds` →
  `enable_auto_cancel_redundant_workflows` and seven more) is inferred from naming
  convention — every field in the spec is a bare typeless boolean with no
  description text at all. Whether `is_oss` is genuinely writable in v3 (an
  improvement over v2's confirmed-dead `oss`) and whether v3 shares v2's
  clear-to-`[]` bug on `pr_only_branch_overrides` are both unconfirmable from the
  spec.
- **pipeline definitions:** the v3 config schema makes `file_path`, `hosted`,
  `type` and `vcs` all jointly required, collapsing what v2 models as two distinct
  shapes (hosted config with no repo vs. a checkout-source config with one) into a
  single object. Whether that's a genuine new requirement or a spec-generation
  artifact from flattening a union type is unconfirmed.
- **triggers:** provider and preset values are unconstrained free strings in the
  spec — no enum lists anywhere — so narrower-enum losses that exist on v2 today
  (a preset restriction on one provider, a write gap on another) can neither be
  confirmed nor ruled out from the spec text.
- **usage export:** whether v3's `phase` string carries the same four values as
  v2's closed state enum, and whether `outcome` carries the same failure detail as
  v2's `error_reason`, are both unconfirmed — see section 1's phase 4 and section 2.
- **runner:** the GET response shape for both agents and resource classes documents
  no attributes at all in this spec version (`data:[{id}]` only), unlike every other
  attributes-bearing object in the same document. The already-known field losses for
  this group rest on a previously established finding (`DESIGN.md`), not on
  something independently re-derivable from this spec text — said explicitly rather
  than asserted as re-confirmed.
- **runner resource-classes:** this survey found `POST
  /runner/resource-classes/{id}/update`, which contradicts the current blanket claim
  in both `API-COVERAGE.md` and `DESIGN.md` that "neither surface has an update
  route for a resource class or a token." That claim needs correcting for the
  resource-class half; it still holds for tokens, which have no update route on
  either surface.
- **runs, workflows, jobs — a documentation disagreement, not a spec ambiguity.**
  `API-COVERAGE.md` lists `GET /runs`, `GET /workflows` and `GET /jobs` under its
  "v3 — complete" section as already backing `circleci_pipeline_run`,
  `circleci_workflow` and `circleci_job`. The provider's own source disagrees: each
  of `pipeline_run.go`, `workflow.go` and `job.go` has a doc comment stating plainly
  that it calls v2 (`pipeline/%s`, `workflow/%s`, `project/{slug}/job/{number}`), and
  `job.go`'s comment explains why — the v2 UUID route has no CircleCI Server
  fallback. This needs a documentation fix in `API-COVERAGE.md` independent of
  whatever happens with the migration in phase 1.
- **catalog offerings:** the spec types `linux`/`windows`/`macos`/`deprecated` as
  bare strings rather than the nested resource-class-to-image-list maps the client
  actually decodes and tests against. Flagged as the one place in this bucket where
  the spec text itself doesn't match the field types the client uses — read as a
  spec-generation artifact, not a real gap, since no data is actually missing from
  the route.
- **Live risk independent of parity:** `GET /notification/links` and both
  `/analysis/*` and `/metric/*` operations are explicitly marked EXPERIMENTAL in the
  spec — "may change without notice ... do not depend on this endpoint in
  production clients." That's worth a maintainer's attention for the already-shipped
  `circleci_notification_links` data source specifically, independent of any
  migration decision.

No files were changed, and no tests or documentation generation were run to produce
this document.
