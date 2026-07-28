<!-- Copyright (c) CircleCI -->
<!-- SPDX-License-Identifier: MPL-2.0 -->

# Design decisions

Why this provider is shaped the way it is. Entries are append-only: if a decision
is reversed, the original stays with a note, because the reasoning is usually more
useful than the conclusion.

## Architecture

### The API version is a per-call prefix, not part of the base URL

`Host` is a bare origin and each verb helper prepends its own `/api/vN`. This is
the whole reason the provider can serve v3 on CircleCI Cloud and v2 on CircleCI
Server from one client.

`circleci-sdk-go` bakes the version into the client's base URL, so one client can
only ever speak one version — which makes the Cloud/Server split inexpressible.
That, plus its untyped errors (below), is why the provider owns its client.

### `internal/httpcl` is vendored, not authored

Copied verbatim from `CircleCI-Public/circleci-cli` (MIT). It lives under
`internal/` upstream so it cannot be imported. Divergences are enumerated in
`internal/httpcl/DIVERGENCES.md`; keeping the file identical is what makes it
replaceable in one step if CircleCI ever publishes a shared client module.

**Rule:** `internal/provider` imports only `internal/circleci`, never
`internal/httpcl`. Provider-specific behaviour composes on top.

### Errors are typed, and `IsNotFound` is the only way to test for absence

`circleci-sdk-go` collapses every failure into `fmt.Errorf("%s: %s", status, body)`,
which forced drift detection into `strings.Contains(err.Error(), "404")`. That also
matched 5xx responses whose bodies happened to contain "404", **silently dropping
live resources from Terraform state**.

`circleci.IsNotFound` covers HTTP 404 *and* the `ErrNotFound` sentinel, because v3
collections answer 200 with an empty array rather than 404 when a filter matches
nothing.

### `deployment` is explicit, not probed

`deployment = "cloud" | "server"` (default `cloud`). A probe that misclassified
would break every resource at once, and Server users already know they are on
Server. It selects the API version per entity and gates the resources that cannot
work.

## Recurring patterns

### Settings resources are `Optional`, never `Optional+Computed`

Applies to `circleci_project_settings`, `circleci_organization_settings` and any
future settings object.

With `Optional+Computed`, Terraform adopts the API's current value into state, and
"not managed" becomes indistinguishable from "managed as false" — so a toggle
nobody declared gets written on the next apply. That is unacceptable for
organization-wide switches, several of which are security controls. Only non-null
values are sent, so two configurations can manage disjoint toggles on one object.

A test asserts no settings attribute is Computed, so this cannot regress quietly.

### Destroy must mirror what create actually did

Not a stylistic preference — it prevented data loss. `circleci_organization`'s
create is a find-or-create for VCS-backed organizations (it adopts one that already
exists), while `DELETE` tears down VCS connections and deletes every project and all
build history. Destroy now releases an adopted organization from state and only
deletes one it genuinely created.

The same logic drives the several resources whose `Delete` makes no API call:
settings objects cannot be deleted, orb versions cannot be unpublished, and a
project-group grant has no revoke route.

### Cloud-only gating happens at plan time, not apply time

`requireCloud` in CRUD alone let `terraform plan` succeed and the apply fail. For
infrastructure as code that is the wrong end of the pipeline: a CI plan check would
pass and the failure would land mid-apply. `ModifyPlan` is the earliest hook with a
configured client.

**Destroy is exempt.** A resource stranded in state by a deployment change must
stay removable.

### `requireCloud` is not only for v3 routes

Every other use of `requireCloud` exists because the route is v3, which CircleCI
Server simply does not route. `circleci_audit_log_config` is v2 — reachable from
Server's router in principle — but the API gates the feature behind a
CircleCI Cloud billing plan tier (the API, requiring a "Scale" plan),
which is a SaaS-billing concept Server installations do not have. CircleCI's own
changelog describes audit log streaming as Scale-plan-only. The provider could
not inspect CircleCI Server's gateway routes served to confirm the route is absent
there too, so this is gated the same way as the v3-only resources, out of
caution rather than confirmed routing. If that turns out to be wrong for some
Server installation, the fix is to drop the gate, not to loosen it further.

### `requireStandaloneCapable` is deliberately necessary-but-not-sufficient

Groups and GitHub App routes need a `circleci` type (standalone) organization.
Server is always `github` type, so `deployment = "server"` is definitively
incompatible and is rejected. A Cloud `github`/`bitbucket` organization also cannot
use them, but the provider holds an organization UUID rather than a slug and cannot
tell the type without an extra lookup — so that half is documented per resource
instead of enforced.

### Values the API never returns are not exposed as strings

`circleci_webhooks` reports `has_signing_secret` (bool) rather than a
`signing_secret` string, because the API masks every secret. A `Sensitive` string
holding `"****"` looks exactly like a credential a configuration could pass to a
receiver and never is one. Same reasoning excludes `token` from
`circleci_runner_tokens`.

### Mocks are derived from production service source, not from the OpenAPI spec

The highest-leverage decision in the project. An audit against the API
found **six bugs where the client and the mock were wrong in the same way**, so
every test passed:

- checkout key tags were `public-key`/`created-at`; production sends
  `public_key`/`created_at`, so `public_key` was permanently empty
- `circleci_runners` decoded a bare array where the API sends `{"items": …}` — and
  the SDK's own fake served a bare array too
- the OIDC API rewrites `ttl` (`90m` → `1h30m0s`), guaranteeing an inconsistent
  result after apply
- `GetPolicyDocument` decoded a bundle map where the single-document route returns a
  flat object, so every policy that existed read as absent
- a missing group answers **403**, not 404, so drift detection never worked
- `filter[ref]` needs a qualified `namespace/orb@version`, not a bare version

**Rule for new work:** confirm every field name, status code and envelope against
the API and its own test fixtures. Write the fixture in the
production shape so a struct-tag regression fails even if a mock is changed to
match it.

**Seventh bug, found by applying the rule to a resource that predated it.**
`circleci-sdk-go` tags the webhook fields `json:"verify-tls"` and
`json:"signing-secret"`; the service reads `verify_tls` and `signing_secret`. Since
the API ignores unrecognized keys, **every webhook `circleci_webhook` created had no
signing secret at all**, whatever was configured, and TLS verification fell back to
the server default. Shipped in v0.4.0 (issue #25).

This one was security-relevant rather than cosmetic: the signing secret is the only
thing that lets a receiver tell a genuine delivery from a forged POST. It is also the
clearest argument for the rule — the bug is invisible to any test whose fake was
written from the client's own structs, and instantly visible to one written from the
service's schema.

`WebhookInput` is deliberately a separate type from `Webhook` rather than the same
struct reused for reads and writes. `Webhook.SigningSecret` only ever holds the
`"****"` mask, and one struct doing both jobs is exactly how a masked value gets
written back as a literal secret.

### Characterization tests state the bug in the assertion

Where a bug is found in a resource that cannot be fixed in the same change — because
the fix needs the `circleci-sdk-go` migration — the test asserts **current**
behaviour with a comment saying so and what to change when it is fixed. Four such
tests exist today, covering issue #26: `circleci_project` sending every settings
toggle as `false` on create, and `circleci_pipeline`'s missing `RequiresReplace`,
absent drift handling, and ungateable deployment check.

The alternative — a skipped or absent test — loses the finding entirely. A test that
fails loudly when someone fixes the bug is a feature: the failure message says "this
appears to be fixed; update the test", which is how the webhook bug got closed out
cleanly.

The trap to avoid is a characterization test that reads like an endorsement. Each one
has to say, in the assertion message, that the value being asserted is wrong.

### A pass-through proxy's own tests are not the schema either

`circleci_audit_log_config` is served by the API, reached through
the API's the API — a config-driven reverse proxy that only
rewrites the URL path (`/api/v2/audit-log/configs/*` → `/file-configs/*`) and
otherwise forwards the request byte-for-byte. the API also carries
its own tests for that route
(`the CircleCI API`), and they use a completely
fabricated body shape: `{"name": "test-config", "s3": {"bucket": ..., "region":
...}}` and a list keyed `"configs"`, both invented purely to exercise the proxy
mechanics (path rewrite, method, status passthrough) and nothing like the real
`target_type`/`config`/`items` shape the API actually sends and
expects. Trusting those tests would have reintroduced exactly the class of bug
the rule above exists to prevent, one layer further from the client than usual.
**Corollary:** when a route is fronted by a generic proxy, its tests confirm the
proxy works, not the wire shape — go to the backend service's own handler and
fixtures regardless of how convincing the gateway-level test looks.

### Not every v2 route family is scoped by organization the same way

`circleci_audit_log_config`'s routes split across two different scoping
conventions on the same resource: list and create are nested under
`/organizations/{org_id}/audit-log/configs`, but get, update and delete address
a config by its bare `id` — `GET`/`DELETE /api/v2/audit-log/configs/{id}`, and
update is a `PUT /api/v2/audit-log/configs` with the id in the body, not even a
per-id path. This is a real API shape, not an inconsistency worth "fixing" in
the client: `GetAuditLogConfig`/`UpdateAuditLogConfig`/`DeleteAuditLogConfig`
take only an id, and the resource's `ImportState` needs only that id — unlike
`circleci_otel_exporter`, which needs `organization_id/exporter_id` because its
*read* is a filtered list rather than a real single-item route.

Two more divergences worth knowing before touching this resource: a missing or
unauthorized config both answer **404** with `"Resource does not exist or
unauthorized"` (anti-enumeration by 404, the opposite of the 403-for-missing
behaviour noted above for groups); and creating a config verifies connectivity
to the destination even when `is_disabled = true`, while *updating* one to
`is_disabled = true` skips that check — so a config can be create-blocked by an
unreachable bucket but paused in place once that bucket becomes unreachable
later.

The family is seven routes in total, all rewritten by the same the API
proxy config: the five above, plus `GET .../organizations/{org_id}/audit-log/access`
(entitlement only — see `circleci_audit_log_access`) and
`POST /api/v2/audit-log/configs/connection/check` (a stateless validate-only
call, deliberately not wired up — see "Deliberate omissions").

### Semantic equality where the API reformats a value

`durationValue` implements `StringSemanticEquals` so `90m`, `1h30m` and `5400s`
compare equal to the `1h30m0s` the API stores. Normalizing in the provider alone is
insufficient: after `terraform import` there is no configuration to normalize
towards.

### A fake-backed test uses `resource.UnitTest`, never `resource.Test`

`resource.Test` **skips unless `TF_ACC=1`**. That is right for a test that talks to a
real installation and wrong for one that drives an `httptest` fake, because the fake
needs no credentials — so gating it behind `TF_ACC` means `go test ./...` reports a
green run from tests that never executed.

The whole fake-backed suite was in that state: **81 test cases across 16 files, written
and correct, that only ever ran in CI** (where `task ci:test` sets `TF_ACC: 1`). Coverage
of `internal/provider` measured 12% locally and 60% in CI, and the difference was
entirely tests skipping rather than anything being untested. Converting them moved the
local number to 57% without a line of new test code.

`resource.UnitTest` is the same function with `IsUnitTest` set, which bypasses the
`TF_ACC` check.

`TestCredentialFreeTestsUseUnitTest` enforces this by AST-walking the package: a test
that calls `resource.Test` while referencing no credential-gated helper fails. The set
of credential-gated helpers is *computed*, not hardcoded — a helper qualifies if it
calls `t.Skip`/`Skipf`/`SkipNow`, or calls another helper that does — so a new fixture
helper in `acctest_test.go` is picked up automatically instead of silently widening the
hole.

## Operational hazards

### Only run `task generate-doc` after registering the types

`tfplugindocs` **removes `docs/` and rebuilds it from the live provider schema**. Run
it while a new resource exists on disk but is not yet listed in
`CircleCiProvider.Resources`/`DataSources` and it deletes the whole `docs/resources`
and `docs/data-sources` trees, then fails partway through regenerating them, because
it cannot find a schema for the template it is rendering.

Recovery is `git restore docs/` plus removing any stray partial file, but the
practical rule is: **register first, generate second.** When several people or agents
are adding types at once, only whoever owns `provider.go` should run it.

## Schema naming

### `organization_id` everywhere, for now

v3 bans `organization_id` in favour of `org_id`, but every existing resource uses
`organization_id`. Mixing them would be worse than either. All v3 renames
(`organization_id` → `org_id`, `pipeline` → `run`) are batched into a planned **1.0**
with a state migration and a `moved{}` guide.

Exception: `circleci_url_orb_allow_list_entry` uses `organization`, because that
route accepts a UUID *or* a `vcs/org` slug and calling it `organization_id` would be
misleading.

## Deliberate omissions

`API-COVERAGE.md` is the full route-by-route inventory, taken from the API'
own registration tables rather than from the published OpenAPI spec — the spec both
omits routes that exist and describes routes that are never wired up. This table is
the *reasoning*; that file is the *checklist*.

| Not built | Why |
|---|---|
| `circleci_schedule` (legacy scheduled pipelines) | Superseded by a trigger with `event_source_provider = "schedule"`; works only on GitHub OAuth and Bitbucket organizations. A migration guide exists instead. |
| Orb promotion as a resource | Promotion creates a *new* version rather than mutating one, so it has no idempotent Terraform shape. The client method exists and is tested. |
| 7 of 10 insights endpoints | They answer "what happened in this run" with unbounded row counts that would churn state on every refresh. Two are deprecated in the v2 API routes served. |
| VCS connection setup, account creation, API token creation, SSO/SAML, audit log retrieval | No API. Account and VCS steps are browser consent flows; `POST /user/token` is session-only auth, which is a deliberate privilege boundary. |
| ~~User invitations~~ | **Corrected — this was wrong.** `GET`/`POST /api/v2/organizations/{org_id}/users` and `GET`/`PATCH`/`DELETE .../users/{user_id}` are fully specified in `openapi_definitions/v2_endpoints/user_groups/`. The capability was recorded as absent because the routes are **not registered in the API's router at all** — the API serves them directly via gateway, on the `a host reserved for internal use` origin. A search of the service that fronts most of v2 therefore found nothing. **Not yet implemented** — it is the largest remaining capability gap, tracked in `NEEDS-FROM-MAINTAINER.md` because shipping it needs sign-off on using an `a host reserved for internal use` route. |
| Cloud resource classes | A config-level flag, not an API object. |
| Docker layer caching | ~~No API object.~~ **Corrected:** `DELETE /api/v3/projects/{id}/dlc` does exist — it purges the cache. But it is a one-shot side effect with nothing to read back, so it cannot be modelled as a resource; Terraform has no primitive for "run this once". Enabling DLC remains a config-level flag. |
| `POST /api/v2/audit-log/configs/connection/check` as its own primitive | It is a stateless validate-only call with nothing to read back or manage, the same shape problem as DLC above. It is also redundant with what `create`/`update` already do: `CreateAuditLogConfig` verifies connectivity unconditionally, and `UpdateAuditLogConfig` does whenever `is_disabled = false` — so calling it as a pre-flight step before create/update would duplicate a check the API is about to perform anyway, for the same failure mode and message. |
| Raw project SSH keys | v1.1 only and absent from the gateway inventory. Checkout keys are the supported mechanism. |
| Workflow/job cancel, rerun, approve | Runtime actions, not desired state. |

## Known compromises

### The provider still has one v1.1 dependency

Following a project after creating it has no v2 route, and an unfollowed project
never runs. It is at least host-correct now, so it works on CircleCI Server.
v1.1 is the only API version with an active deprecation initiative.

### Two data sources sit on an internal API

`circleci_github_app_repository` and `_repositories` use routes CircleCI marks
*"Internal / CLI-only … intentionally NOT customer-facing"*, excluded from the
published spec. Approved by the maintainer; documented as unpublished and subject
to change. They are the only way to resolve `owner/repo` to the numeric
`external_id` that `circleci_pipeline` and `circleci_trigger` require, and the
alternative is practitioners pasting magic numbers.

### ~~`internal/provider` coverage is around 50%~~ — superseded

The original entry said coverage was "around 50%" and that provisioning test
organizations was the way to raise it. Both halves were wrong, in a way worth keeping
on the record.

The number was never one number. It measured 12% locally and 60% under CI, and the
whole difference was **tests skipping rather than code being untested** — see "A
fake-backed test uses `resource.UnitTest`" above. Unskipping the fake-backed suite and
adding fakes for the resources that had only credential-gated tests took it to about
77% with the same measurement everywhere.

So credentials were never the blocker for *coverage*. What they are still needed for
is the class of bug mocks cannot find, which is a different thing and remains the top
item in `NEEDS-FROM-MAINTAINER.md`. Note that the seven wire-shape bugs found so far
were all caught by reading production the real wire format, not by running against a live
API — real organizations are for the behaviours nobody thought to mock, not for
re-finding these.

Targets unchanged: 85% for `internal/provider`, 95% for `internal/circleci`.
`internal/httpcl` is vendored and deliberately not padded.
