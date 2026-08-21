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

### `circleci-sdk-go` is removed, not wrapped

Originally the plan was to keep `circleci-sdk-go` for the resources that already
shipped on it and use the owned client only for new work — add value first, retire the
dependency later. That was the right call at the time and the wrong one to keep:
**eight bugs were traced to the SDK**, one of them a shipped security bug
(`circleci_webhook` silently never sending `signing_secret`). The maintainer's decision
was to pull the band-aid off in one go.

The SDK's problems were not incidental. They were structural, and each produced a
distinct class of bug:

| SDK property | What it caused |
|---|---|
| Hyphenated JSON tags against a snake_case API that ignores unknown keys | Fields permanently empty. **Two instances**: `public-key`/`created-at` on checkout keys, `created-at` on project environment variables. A third, `signing-secret`/`verify-tls` on webhooks, turned out to be the opposite mistake — see "The webhook routes are asymmetric" below |
| Untyped errors — every failure is `fmt.Errorf("%s: %s", status, body)` | Drift detection by `strings.Contains(err.Error(), "404")`, which also matches a 5xx whose body mentions 404, **silently dropping live resources from state** |
| Version baked into the client's base URL | "v3 on Cloud, v2 on Server" inexpressible; also a hardcoded `https://circleci.com` that made project creation impossible on Server |
| `Configure` receiving a narrow service (`*pipeline.PipelineService`) rather than a client | `circleci_pipeline_definition` could not be deployment-gated **at all** — the service carried no deployment information |
| Missing fields (`AdvanceSettings` has no `build_prs_only`) | Settings simply unreachable from Terraform |

The last row is the one worth remembering: a wrapper cannot fix a field the wrapped type
does not have. That is why this was a removal rather than an adapter.

**Wire shapes were re-derived from the real API, not ported from the SDK.** Porting
would have carried the tag bugs across intact. `CircleCI-Public/circleci-cli`'s
`internal/apiclient` was the primary reference — MIT, actively maintained, and covering
the same entities — cribbed with attribution since it lives under `internal/` and cannot
be imported.

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

The one exception is a setting the API will not accept on write. `circleci_project_settings.oss`
is `Computed`-only, not `Optional`: the settings `PATCH` answers
`400 Unexpected field 'advanced.oss'.` and rejects the whole request when the field is
present, so the attribute can only ever report. Adopting a value that can never be
written back cannot lead to writing it, which is what makes the exception safe — and the
same test now asserts `oss` is *not* `Optional`, for the opposite reason.

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
Server's router in principle — but the API gates the feature behind a CircleCI
Cloud billing plan tier, requiring a "Scale" plan, which is a SaaS-billing
concept Server installations do not have. CircleCI's own changelog describes
audit log streaming as Scale-plan-only. There was no way to confirm whether a
Server installation routes the path at all, so this is gated the same way as the
v3-only resources, out of caution rather than confirmed routing. If that turns out to be wrong for some
Server installation, the fix is to drop the gate, not to loosen it further.

### Contexts answer 403 for absence too, and that is decided before the handler runs

Every `/api/v2/context/{id}/...` route — get, delete, both restriction routes, both
env-var mutating routes, both list routes — resolves the context id in a preliminary step
that maps **every** failure to **HTTP 403**: genuinely deleted, belongs to another
organization, or the token lacks permission. That step runs *before* the route's own
logic, so a route that would otherwise have answered 404 never gets the chance.

This was not previously modelled anywhere here. Worse, `context_fake_test.go`
asserted 404 for missing reads and **400** for missing deletes, restrictions and env
vars — so the fake disagreed with production on every one of those paths, and context
drift detection could never have worked. Ninth bug of the same family, and the reason
the rule above says to read the handler *and its middleware*.

Handling mirrors `circleci_group` rather than folding 403 into `IsNotFound`:

- **Read** treats 403 as a hard diagnostic naming all three possible causes, leaving
  state untouched. Silently removing the resource would let a token that merely lost
  permission cause a recreate of a live context — with real environment variables in it.
- **Delete** treats 403 *and* 404 as already-gone, because the desired end state is
  reached either way.

A genuine 404 — the narrow race where the middleware resolves and the handler then fails
— does still drop state for a clean recreate.

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

### Mocks are derived from what the API actually returns, not from the OpenAPI spec

The highest-leverage decision in the project. An audit of what the API really
accepts and returns found **six bugs where the client and the mock were wrong in
the same way**, so every test passed (three more were found later, below, bringing
the total to nine):

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
what the API actually sends and accepts — not against the spec, and not against
what the client already assumes. Write the fixture in the real wire shape so a
struct-tag regression fails even if a mock is changed to match it.

**Eighth bug, and the second of exactly the same kind.** `circleci-sdk-go`'s
`envproject.EnvVariable` tagged the creation timestamp `json:"created-at"` against the
API's `created_at`, so `circleci_project_environment_variable.created_at` was always
empty. That is the second hyphenated-tag bug from the same dependency, after the checkout
key (`public-key`/`created-at`). Both re-verified against the live API since: those
responses really are snake_case, and neither field is ever sent, so neither has a request
side to get wrong.

Two instances of one mistake in one library is still the strongest single argument for the
removal: it is not a bug to be fixed but a habit encoded in a codebase nobody maintains.
The third instance everyone assumed was the same — the webhook — was not, and assuming it
was is what cost two releases.

**Seventh bug — and the one this rule was applied to backwards.** The webhook routes
are **asymmetric**: the request side reads `verify-tls` and `signing-secret`
(hyphenated) while responses report `verify_tls` and `signing_secret` (snake_case).
The published OpenAPI document says so, and the live API agrees — a create sending the
hyphenated keys stores the secret and honours the flag; the same create sending
snake_case answers `201` with both values discarded.

So `circleci-sdk-go`'s hyphenated tags were right for the request and wrong for the
response, and "make them snake_case like everything else in v2" fixed the read and broke
the write. **Every webhook `circleci_webhook` created had no signing secret at all** —
first in v0.4.0 because the SDK's tags were also used to decode, then again in v0.5.0 and
v0.6.0 because the request keys were changed to the spelling the request side ignores.
Security-relevant rather than cosmetic both times: the signing secret is the only thing
that lets a receiver tell a genuine delivery from a forged POST.

Two lessons, and the second is the expensive one:

1. The bug is invisible to any test whose fake was written from the client's own structs,
   and instantly visible to one written from the service's schema. That is the rule above.
2. "The API is snake_case everywhere" is a generalisation, and a fake written from a
   generalisation is a fake written from the client again. The webhook fake read snake_case
   request keys, so it agreed with the broken client and the whole suite stayed green
   through two releases. Fakes must be pinned to the route, and the wire shape must be
   confirmed by a real request when the two directions disagree.

`WebhookInput` is deliberately a separate type from `Webhook` rather than the same struct
reused for reads and writes, and the asymmetric keys are now the strongest reason: plain
struct tags cannot spell one field two ways. It is also why the difference is not hidden
in a `MarshalJSON` — tags are what people read and grep, and a method body is where a
convention like this goes to die. `Webhook.SigningSecret` only ever holds the `"****"`
mask, and one struct doing both jobs is also exactly how a masked value gets written back
as a literal secret.

**Ninth bug — a fake more generous than production, and the first of that shape.** Every
bug above was a *wrong* value: a misspelled key, a bare array, a rewritten `ttl`. This one
was a value production does not send at all. `POST
/projects/{project}/pipeline-definitions/{definition}/triggers` answers `200` and omits
`created_at`; `GET`, `PATCH` and the list route all include it. Nothing in the OpenAPI
document says so, and the trigger fake echoed its whole stored record back from `POST`,
`created_at` and all — so when `circleci_trigger`'s `Create` dropped its read-back on the
strength of a comment asserting "the create response is a full `Trigger`, the same shape
`GetTrigger` returns", every mocked test still passed while `created_at` was permanently
empty in state for real users.

The generalisation this time was not about spelling but about symmetry: *a create returns
the thing it created.* Usually true, promised nowhere, and — as with "the API is
snake_case everywhere" — a fake written from it is a fake written from the client again.
Two lessons on top of the rule above:

1. **A fake must be able to be less generous than the client wants.** A fake that always
   answers with everything the struct can hold cannot fail a provider that reads a field
   the route never sends. The trigger fake now derives its `POST` body from its stored
   record by *removing* `created_at` (`createResponse` in
   `internal/provider/trigger_resource_fake_test.go`), so the omission is a property of
   the fake rather than an accident of which fixture a test happened to use.
2. **Route-by-route, not resource-by-resource.** "The trigger API returns a trigger" is
   true of three of its four routes. The shape has to be confirmed per method, which is
   what `CreateTrigger`'s doc comment now records: all four responses, side by side, with
   the timestamps they were measured from.

### Characterization tests state the bug in the assertion

Where a bug is found in a resource that cannot be fixed in the same change — because
the fix needs the `circleci-sdk-go` migration — the test asserts **current**
behaviour with a comment saying so and what to change when it is fixed. Four such
tests exist today: `circleci_project` sending every settings
toggle as `false` on create, and `circleci_pipeline_definition`'s missing `RequiresReplace`,
absent drift handling, and ungateable deployment check.

The alternative — a skipped or absent test — loses the finding entirely. A test that
fails loudly when someone fixes the bug is a feature: the failure message says "this
appears to be fixed; update the test", which is how the webhook bug got closed out
cleanly.

The trap to avoid is a characterization test that reads like an endorsement. Each one
has to say, in the assertion message, that the value being asserted is wrong.

### A pass-through route's wire shape belongs to whatever is behind it

`circleci_audit_log_config`'s routes are a thin proxy: the path is rewritten and
the request is otherwise forwarded byte-for-byte to a backend, which is what
actually defines the request and response bodies. The shape the provider sends is
`target_type`/`config`/`items`, established from what the API accepts and returns
rather than from anything the proxy layer suggests.

**The general rule:** where a route is a pass-through, the wire shape is the
backend's, and a plausible-looking body that the proxy happily forwards is not
evidence the backend accepts it. This is the same trap as the rule above, one
layer further from the client.

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

The family is seven routes in total, all rewritten by the same proxy
configuration: the five above, plus `GET .../organizations/{org_id}/audit-log/access`
(entitlement only — see `circleci_audit_log_access`) and
`POST /api/v2/audit-log/configs/connection/check` (a stateless validate-only
call, deliberately not wired up — see "Deliberate omissions").

### A `Required` attribute the server ignores is still worth sending

`circleci_runner_resource_class.organization_id` is `Required`, and the provider sends
`org_id` in the create body — but the API **never reads it**. It derives the owning
organization from the resource class's namespace and the caller's admin permissions on
that namespace.

It is still sent, for two reasons: the schema requires it, so removing it from the body
while leaving it `Required` would be more confusing than the redundancy; and the server
ignoring a field today is not a promise it will keep. The client documents why.

Two other things the same handler read revealed, both invisible from the spec:

- `GET /api/v3/runner/resource` accepts `namespace` *or* `org-id`, and when both are
  present **`org-id` wins and `namespace` is silently ignored** — not an error. A data
  source passing both would appear to filter by namespace and would not.
- A missing resource class and an unauthorized caller both answer **404**
  (`middleware.AccessDeniedMsg`). Same anti-enumeration conflation as groups answering
  403, and the same conclusion: absence and forbidden are not distinguishable here, so
  Delete may treat 404 as success while Read must not silently drop state on a
  permission change.

### Semantic equality where the API reformats a value

`durationValue` implements `StringSemanticEquals` so `90m`, `1h30m` and `5400s`
compare equal to the `1h30m0s` the API stores. Normalizing in the provider alone is
insufficient: after `terraform import` there is no configuration to normalize
towards.

### An unordered collection is a `SetAttribute`, and no state upgrade is needed to become one

CircleCI stores several collections without an order and answers with them in an order
of its own choosing. Verified live:

```
PATCH pr_only_branch_overrides ["zebra","alpha","main","beta"]
→ GET  pr_only_branch_overrides ["zebra","main","alpha","beta"]
```

stable across subsequent reads, but never the order it was given. A `ListAttribute`
over such a collection produces a plan that never converges: Terraform compares the
configured order against the returned order and plans a change on every run, and where
the attribute is also `Computed` the apply fails with "Provider produced inconsistent
result after apply". `circleci_webhook.events` and `pr_only_branch_overrides` on both
project resources were in exactly that state.

**Changing an existing attribute from a list to a set needs no `SchemaVersion` bump and
no `UpgradeState`.** A list and a set of the same element type share one JSON encoding —
both are a JSON array — and the framework answers `UpgradeResourceState` by re-reading
the stored raw JSON against the *current* schema type whenever the stored version matches
the current one. So state written by an older provider decodes as a set untouched. This
is checked rather than assumed: `TestListToSetNeedsNoStateUpgrade` feeds prior
list-shaped state through the provider's real `UpgradeResourceState` RPC for all three
resources, asserts the value comes back typed as a set, and asserts the schema version is
still 0 so that a future bump has to come with a deliberate decision.

The change *is* breaking for a configuration that indexes the attribute (`events[0]`),
which is why it appears in the changelog's BREAKING CHANGES.

### A fake must reorder what the API reorders

The permanent diff above survived a passing suite of 80-plus fake-backed tests, for one
reason: every fake echoed the collection back in the order it was submitted. That is the
one behaviour the real API does not have, so no test could fail for the only bug the
attribute had ever had.

`reorderedLikeTheAPI` (in `webhook_resource_fake_test.go`) reverses any JSON array a
fake is about to store, and the webhook and both project-settings fakes route
`events` and `pr_only_branch_overrides` through it. Reversal is the cheapest order that
differs for any collection of two or more, so reverting either attribute to a
`ListAttribute` now fails immediately with "Provider produced inconsistent result after
apply" rather than passing. It reverses a *copy*, because the recorded request bodies
are what tests assert the *sent* order on.

Two consequences worth stating, because both are easy to undo by accident:

- A test that means to exercise ordering must configure **two or more** elements. A
  single-element collection reverses to itself and passes whatever the attribute's type
  is; `TestAccProjectSettingsResource` used one branch and was silent for that reason.
- `TestFakeAPIsDoNotEchoCollectionOrder` guards the guard, failing if a fake goes back
  to echoing.

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

### A test provider must never shadow the real registration list

`discovery_data_sources_test.go` used to define its own `discoveryProvider` that
embedded the real provider but overrode `DataSources()` with a hardcoded list of eight,
reasoning that those tests "should not depend on the real registration".

That reasoning was backwards, and it cost two data sources their entire test coverage.
`circleci_pipeline_run_values` and `circleci_github_app_installation` were correctly
implemented, correctly registered, and had tests that reused this factory — so both
failed with **"the provider does not support data source"**, which reads like a broken
data source rather than a stale list in a test helper. Neither was actually exercised.

`TestEveryConstructorIsRegistered` already guarantees the real list is complete, so
depending on it is strictly safer than duplicating it. The factory is now an alias:

```go
var discoveryProviderFactories = testAccProtoV6ProviderFactories
```

**Rule:** a test may narrow the *provider configuration* (host, deployment, credentials)
as much as it likes, but it must not narrow the *type registry*. Any list of registered
types that exists in more than one place will drift, and the copy in the test wins
silently.

### Vulnerability scanning is reachability-based, and it runs in CI

`task vulncheck` runs `govulncheck`, which analyses whether this code can actually
*call* a vulnerable function rather than just comparing version numbers.

It is in CI because it found something a version-matching scanner did not. Dependabot
reported 43 alerts against the default branch's dependency set; the set reachable from
*this* branch was different, and included seven Go **standard library**
vulnerabilities — `crypto/tls`, `crypto/x509`, `net/http`, `net/textproto`,
`html/template` — reachable from the provider's own HTTP client and from
`providerserver`, plus a gRPC **authorization bypass** reachable from
`providerserver.Serve`. A dependency scanner keyed on `go.mod` will not flag a
standard library issue at all, because the standard library is not a dependency: it
comes from the toolchain.

**Rule:** the `toolchain` directive in `go.mod` and the Go image in
`.circleci/config.yml` move together. A patch-level Go bump is a security fix here,
not housekeeping — a Terraform provider is a long-lived binary that terminates TLS to
an API and is handed credentials.

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

### Both `organization_id` and `org_id`, while the old name is retired

CircleCI's v3 conventions call the field `org_id`; every release of this provider has
called it `organization_id`. The HCL name and the wire name are independent tags, so
this was never a v3 prerequisite — the provider already spoke v3 across 44 call sites
while exposing `organization_id`, and already sent `org_id` wherever v3 required it.
Aligning the *Terraform* vocabulary was a naming decision with no forcing function.

The decision taken was to **accept both names and deprecate the old one**, rather than
rename. Existing configurations keep working untouched and indefinitely, switching is a
no-op, and `organization_id` is removed at a later major once usage has drained. There
is no state upgrade at any point.

~~All v3 renames are batched into a planned 1.0 with a state migration and a `moved{}`
guide.~~ Both halves of that earlier note were wrong:

1. **`moved{}` is the wrong mechanism for an *attribute* rename.** It re-addresses
   resource *instances*; it does nothing for an attribute. That needs `SchemaVersion`
   plus `UpgradeState`, and the deprecation approach needs neither. `moved{}` applies
   only to renaming a resource *type*, with `ResourceWithMoveState` — which is exactly
   what `circleci_pipeline` → `circleci_pipeline_definition` turned out to need, so the
   mechanism is now in use, just not for the reason originally written down.
2. **`pipeline` → `run` was bigger than "nearly a non-issue".** The attribute names were
   the symptom; the *resource type name* was the cause. `circleci_pipeline` managed a
   definition while colloquial CircleCI usage — and the `/pipeline/:id` route — means a
   run. Renaming the attributes while leaving the type name in place would have made the
   provider internally inconsistent rather than clearer. See "Deprecate only what
   shipped" below.

#### Deprecate only what shipped

A rename inside an unreleased build is not a compatibility event, and treating it as one
has a real cost.

Every "pipeline" name in the provider was disambiguated at once: the resource type, two
data source type names, four more data source type names, and five attributes. The first
implementation gave all of them the full non-breaking treatment — old name kept as a
working alias, deprecation message, `ExactlyOneOf` validator, alias constructor,
registration entry, doc page.

Checking the last released tag (v0.4.0, `d7fffe4`, 21 registered types) showed only three
of those surfaces had ever reached a practitioner: the `circleci_pipeline` resource, the
`circleci_pipeline` data source, and `pipeline_id` on the `circleci_trigger` resource.
Everything else was new in the same unreleased version that renamed it.

So eight of eleven deprecations protected nobody, and two of them were actively
incoherent: `circleci_pipeline_run_values` would have shipped with `pipeline_id` already
marked deprecated, telling practitioners not to use an attribute they had never seen. The
cost is not theoretical either — each alias is a registered type name, a doc page, a
constructor, a test, and a future removal.

Worse, two of the eleven could not have worked at all. `pipelines` on
`circleci_pipeline_definitions` and the nested `pipeline_id` on
`circleci_deploy_component` are **`Computed`**, and the framework only raises
`DeprecationMessage` when a value is present in *configuration*
(`fwserver/attribute_validation.go` gates on `!configHasNullValue`). A computed
attribute is always null in config, so the warning can never fire. A "deprecated"
computed attribute is deprecated only in the documentation.

The rule: **check what shipped before writing a deprecation.** For anything unreleased,
rename it and note it in the changelog.

#### The obvious implementation destroys data

This is the part worth remembering. `organization_id` carries `RequiresReplace` on
twelve resources, because CircleCI has no route that moves an object between
organizations. Add `org_id` as a plain second `Optional` attribute and the sequence is:

1. a practitioner follows the deprecation notice and removes `organization_id`
2. Terraform plans it as `null` — a change
3. `RequiresReplace` fires
4. the resource is **destroyed and recreated**

For `circleci_project` that deletes the project and its build history. The
"non-breaking" migration would have been more destructive than the breaking rename it
was meant to avoid, and it would have happened precisely to the users who did as they
were told.

Reproducible: reverting to the naive shape fails the migration test with
`expected NoOp, got action(s): [delete create]`.

Three things prevent it, all necessary:

- **`Computed`** on both attributes, so the departing one retains its prior value
  instead of planning `null`. This is the load-bearing one.
- **`RequiresReplaceIfConfigured`** instead of `RequiresReplace`, so a null
  configuration value never forces replacement while a genuine organization change
  still does.
- **`reconcileOrgIDPlan` in `ModifyPlan`**, required on any resource built with
  `replaces: false`. Without replacement, changing the organization plans
  `organization_id = B` alongside `org_id = A`, which no `Update` can satisfy — the
  plan is the only place the retained value can be corrected, and the correct value has
  to come from *config*, since the plan cannot say which name was written.

**Everything lives in `internal/provider/org_id_deprecation.go`**, deliberately. Forty
call sites resolve to one file, so ending the deprecation is a small deliberate change —
delete `deprecatedOrgIDAttribute`, drop `orgIDConfigValidator`, make `orgIDAttribute`
`Required`, follow the compiler — rather than an audit of forty schemas hoping to catch
every straggler.

Two asymmetries are deliberate and documented in that file: data sources use plain
`Optional` (nothing to replace, nothing retained, so the unconfigured name stays null),
and the two data sources where the organization is a read-only *output* get `Computed`
mirrors instead, so dropping `organization_id` later removes nothing a configuration
cannot already read.

Exception to the naming: `circleci_url_orb_allow_list_entry` uses `organization`,
because that route accepts a UUID *or* a `vcs/org` slug and calling it `organization_id`
would be misleading.

## Deliberate omissions

`API-COVERAGE.md` is the full route-by-route inventory, built from the routes CircleCI
actually serves rather than from the published OpenAPI spec — the spec both omits
routes that exist and describes routes that are never wired up. This table is
the *reasoning*; that file is the *checklist*.

| Not built | Why |
|---|---|
| `circleci_schedule` (legacy scheduled pipelines) | Superseded by a trigger with `event_source_provider = "schedule"`; works only on GitHub OAuth and Bitbucket organizations. A migration guide exists instead. |
| Orb promotion as a resource | Promotion creates a *new* version rather than mutating one, so it has no idempotent Terraform shape. The client method exists and is tested. |
| 7 of 10 insights endpoints | They answer "what happened in this run" with unbounded row counts that would churn state on every refresh. Two are deprecated. |
| VCS connection setup, account creation, API token creation, SSO/SAML, audit log retrieval | No API. Account and VCS steps are browser consent flows; `POST /user/token` is session-only auth, which is a deliberate privilege boundary. |
| ~~User invitations~~ | **Corrected — this was wrong.** Routes to list organization members, invite them with a role, change a role and remove a member are fully specified. The capability was recorded as absent because those routes are not reached the same way as most of v2, so looking where the bulk of v2 lives found nothing. **Deliberately not implemented:** they answer only on a host reserved for internal use, not through the public API. This is the largest remaining capability gap. |
| Cloud resource classes | A config-level flag, not an API object. |
| Docker layer caching | ~~No API object.~~ **Corrected:** `DELETE /api/v3/projects/{id}/dlc` does exist — it purges the cache. But it is a one-shot side effect with nothing to read back, so it cannot be modelled as a resource; Terraform has no primitive for "run this once". Enabling DLC remains a config-level flag. |
| `POST /api/v2/audit-log/configs/connection/check` as its own primitive | It is a stateless validate-only call with nothing to read back or manage, the same shape problem as DLC above. It is also redundant with what `create`/`update` already do: `CreateAuditLogConfig` verifies connectivity unconditionally, and `UpdateAuditLogConfig` does whenever `is_disabled = false` — so calling it as a pre-flight step before create/update would duplicate a check the API is about to perform anyway, for the same failure mode and message. |
| Raw project SSH keys | v1.1 only, and not served on CircleCI Server. Checkout keys are the supported mechanism. |
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
`external_id` that `circleci_pipeline_definition` and `circleci_trigger` require, and the
alternative is practitioners pasting magic numbers.

### ~~`internal/provider` coverage is around 50%~~ — superseded

The original entry said coverage was "around 50%" and that provisioning test
organizations was the way to raise it. Both halves were wrong, in a way worth keeping
on the record.

The number was never one number. It measured 12% locally and 60% under CI, and the
whole difference was **tests skipping rather than code being untested** — see "A
fake-backed test uses `resource.UnitTest`" above. Unskipping the fake-backed suite and
adding fakes for the resources that had only credential-gated tests took it to **81.2%**
with the same measurement everywhere. `internal/circleci` is at **90.6%**, and `internal/httpcl` — vendored, deliberately not padded — at 65.1%.

So credentials were never the blocker for *coverage*. What they are still needed for
is the class of bug mocks cannot find, which is a different thing and remains the top
outstanding ask. Eight of the nine wire-shape bugs found so far were caught by checking
the real wire format rather than by running against a live API — real organizations are
for the behaviours nobody thought to mock, not for re-finding those.

The ninth is the exception, and it is the argument for credentials rather than against
them: the trigger create route's missing `created_at` was surfaced by a real acceptance
run (`ImportStateVerify` failing, and a permanent `created_at` diff after apply), *then*
localised with `curl`. Reading a wire format tells you what a route sends; only running
the resource tells you which of the fields it omits the provider was relying on.

Targets unchanged: 85% for `internal/provider`, 95% for `internal/circleci`.
`internal/httpcl` is vendored and deliberately not padded.
