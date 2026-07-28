<!-- Copyright (c) CircleCI -->
<!-- SPDX-License-Identifier: MPL-2.0 -->

# Open questions and things needed from the maintainer

Everything here has been driven as far as judgement allows. Each item says what was
assumed in the meantime, so nothing is blocked waiting for an answer.

Ordered by how much it unblocks, except for the first item, which is ordered by
urgency because it affects users of the currently released provider.

---

## 0. Two things to raise with internal teams

Both are **decisions, not work**. Everything else in this file has been driven as far
as judgement allows; these two cannot be.

### 0a. Organization member management sits on an internal-only host

`GET`/`POST /api/v2/organizations/{org_id}/users` and
`GET`/`PATCH`/`DELETE .../users/{user_id}` support listing members, inviting them with
a role, changing a role and removing a member. Fully specified in
the API's `openapi_definitions/v2_endpoints/user_groups/`.

**Not implemented, pending your internal conversation.** The blocker is that both spec
files carry a `servers:` override:

```yaml
servers:
  - url: a host reserved for internal use
    description: reserved for internal use
```

They are also absent from the API's router entirely — the API serves
them directly through gateway. That is the same unpublished-but-reachable category as the
GitHub App repository routes, which you approved explicitly, so this needs the same
call rather than an assumption.

Worth knowing before that conversation:

- This is the **largest remaining capability gap** in the provider. It is also the one
  most likely to be asked for by the enterprise customers who wanted RBAC groups,
  since groups without member management is half a feature.
- Invite is a **bulk** endpoint returning **202** with a per-email `errors` array, so a
  single-member resource has to inspect `errors` — otherwise a failed invitation
  reports success.
- `status` is `pending` until the invitee accepts, which is a real persistent state a
  resource has to tolerate rather than treat as drift.
- Destroy revokes a real person's access to the organization.

Questions for the internal team: is depending on `a host reserved for internal use` acceptable for
a published provider; is there a plan to route these through the API; and is
the shape stable enough to build against.

### 0b. The 1.0 renames need a decision on timing

v3 API conventions require `organization_id` → `org_id`, and `pipeline` → `run`.
Every currently shipped resource uses `organization_id`, and mixing the two would be
worse than either, so **all renames are batched into a planned 1.0** with a state
migration and a `moved {}` guide — one upgrade absorbing all of them rather than
several releases each breaking something.

What needs deciding:

- **Is 1.0 real, and when?** The batching only makes sense if it actually happens.
  Right now every rename is deferred against a release that has no owner or date.
- **Does the 0.5.0 work in this branch ship first**, or wait and go out as 1.0 with the
  renames included? Shipping 0.5.0 first gets the bug fixes to users sooner — including
  the webhook one — at the cost of a second breaking upgrade later.
- **`circleci_trigger`'s attribute names diverge from its own data source** (`repo` vs
  `repository`, `web_hook` vs `webhook`). That is a bug, but fixing it is breaking, so
  it is queued for the same batch.
- **`circleci_webhook.signing_secret`** is misleading — the API only ever returns a
  mask, so the attribute can never round-trip. Deprecating it is breaking
  ([#21](../../issues/21)).
- **`circleci_project`'s settings toggles are `Optional+Computed`**, which is the cause
  of issue #26's first bug. Making them `Optional`-only is the correct fix and needs a
  state migration, so it belongs in the same batch.

There is a full list in `DESIGN.md` under "Schema naming"; this is the summary for
taking to a team.

---

## 0c. Resolved: the webhook signing secret bug needs no disclosure

Recorded here because the reasoning matters more than the conclusion.

`circleci_webhook` at v0.4.0 and earlier never sent `signing_secret` or `verify_tls`
(hyphenated struct tags in `circleci-sdk-go` against snake_case API fields, and the API
ignores unknown keys). So every webhook the provider created had no signing secret,
however carefully one was configured, and apply reported success.

**Maintainer decision: no security advisory and no direct customer notification.**
Document it and carry it in the changelog. Done — it is the `SECURITY` entry at the top
of `CHANGELOG.md`, which states what was wrong, what the exposure was, and that
re-applying is the remediation.

Fixed by migrating the resource onto the provider's own client. Four further bugs of the
same family are catalogued in issue #26; none are security-relevant, and all are being
addressed by the full `circleci-sdk-go` removal.

---

## 1. Test organizations and credentials — the only hard blocker

Needed: the seven organizations in `TESTING.md`, each with an org-admin token, and a
CircleCI context per integration type holding the `CIRCLECI_TEST_*` variables.

**Why it matters more than it sounds.** `internal/provider` coverage sits near 50%
almost entirely because ~37 acceptance tests skip without credentials — and they
cover the *pre-existing* resources. Provisioning these lifts coverage with no new
test code, and it is the only way to find the class of bug mocks cannot: an API
that behaves differently from its own source.

**They must be disposable.** The suite creates and deletes organizations, replaces
whole config-policy bundles, resets OIDC claims and publishes orb versions
irreversibly. Two of those are security controls. See the blast-radius table in
`TESTING.md`.

**Assumed meanwhile:** every acceptance test skips with a message naming its missing
variable, so a credential-less checkout is green.

**Note on GHES:** the GitHub Enterprise Server integration is in Preview with access
granted on request, so column 6 may need a CircleCI-side request rather than
self-serve signup.

---

## 2. Is `deployment = "server"` right, or should it be version-based?

`deployment` is a two-valued enum today. If v3 lands on Server partially — which is
how it will arrive, since the ingress works in a lab but the read path needs
external datastores CircleCI Server does not ship — a boolean will not describe reality.

**Question:** would you rather have `api_version` per entity, or capability
detection, or keep the enum and add `server_version`?

**Assumed meanwhile:** the enum, because it is honest about today and cheap to
extend. Adding a third value is not a breaking change.

---

## 3. Confirm the undocumented endpoints are acceptable to ship

Two groups are built on routes absent from the published spec:

| Endpoint group | Used by | Status |
|---|---|---|
| `/api/v2/github-app/organization/{id}/repositories` | `circleci_github_app_repository`, `_repositories` | **Approved** by the maintainer |
| `/api/v2/organizations/{org}/groups/{id}/users`, `.../projects/{pid}/groups` | `circleci_group_membership`, `circleci_project_group` | Not explicitly confirmed |

The group routes are in the repo's own OpenAPI definitions but carry a `servers:`
override pointing at an internal host. They are the only way to manage group
membership and project role grants, which is a named customer request.

**Assumed meanwhile:** shipped, with each doc page stating the API is unpublished
and may change.

---

## 4. Documentation contradictions only a real installation can settle

Tracked as issue #7. Each is a `?` in the README compatibility matrix, deliberately,
because a wrong `yes` in a compatibility chart is worse than an admitted gap.

- **`build_fork_prs` on GitLab.com** — the VCS overview says supported; the pipelines
  and OSS pages say unsupported for GitLab and GitHub App pipelines.
- **Legacy scheduled pipelines on CircleCI Server** — the API scopes them to
  `github`/`bitbucket` org types and Server *is* a `github` org, but the VCS overview
  marks schedule triggers unsupported on the Server column.
- **Rollback on GHES** — the VCS overview says supported; the GHES page lists it
  under "not yet available".
- **`restriction_type = "group"`** — CircleCI has two unrelated concepts called
  "group" with mutually exclusive requirements, and the API does not say which one
  this field takes.
- **`oss`, `pr_only_branch_overrides`** per VCS — never documented outside GitHub.
- **Organization settings, URL orb allow list, OTel exporters, usage export on
  Server** — no statement exists either way.

Also: CircleCI's only Cloud-vs-Server parity document is a Support Center article
that returns HTTP 403 to automated fetching, so it cannot be cited programmatically.
Someone should read it and reconcile.

---

## 5. Naming, and whether 1.0 is real

All v3 renames are batched into a planned 1.0: `organization_id` → `org_id`, and the
v3 concept renames (v2 *pipeline* → v3 **run**, v2 *pipeline-definition* → v3
**pipeline**).

**Question:** is a 1.0 with a state migration and a `moved{}` guide actually going to
happen? If not, the rename should be abandoned rather than left pending — new
resources currently ship with names we already know v3 will disagree with.

Registry GA has been gated on "planned API changes" for 13 months, which is the same
question wearing a different hat.

**Assumed meanwhile:** `organization_id` everywhere, deferred to 1.0.

---

## 6. Ownership

The repository has no `CODEOWNERS`, the original author has left, and releases are
cut ad hoc. That is not a technical blocker but it decides whether this work is
maintainable after it lands.

Also worth deciding: whether `circleci-sdk-go` is retired. The provider is nearly
off it — only contexts, context env vars, organizations, pipelines, triggers,
webhooks and runner still use it — and it has no owner, no releases and no tags.

---

## 7. Smaller judgement calls, flagged rather than asked

Proceeding on these; say so if any is wrong.

- **`circleci_schedule` not implemented.** Superseded by `circleci_trigger` with
  `event_source_provider = "schedule"`. A migration guide exists. Reversible if
  GitHub-OAuth customers need it.
- **7 of 10 insights endpoints skipped** as runtime reporting rather than desired
  state.
- **Orb promotion not a resource** — it creates a new version, so it has no
  idempotent shape.
- **`circleci_webhook`'s masked `signing_secret`** is misleading and deprecating it
  is breaking, so it is queued for 1.0 (issue #21).
- **Runner uses the older `runner.circleci.com` surface**, not
  `circleci.com/api/v3/runner/resource-classes`, because the newer one is being
  actively reshaped and is not what Server serves.
- **Docker layer cache purge not implemented.** `DELETE /api/v3/projects/{id}/dlc`
  exists, but it is a one-shot side effect with nothing to read back, so it has no
  resource shape. (An earlier version of `DESIGN.md` claimed no API existed at all;
  that was wrong and is corrected.)

## 8. Decisions I would rather you made than me

These are the two places where I formed a view but the call is genuinely yours,
because both are about what the provider is *for* rather than how it works.

### Reporting and search endpoints — issue #24

Eight read-only routes are unimplemented: `POST /api/v3/analysis/{tests,jobs}`,
`POST /api/v3/metric/{counts,distributions}`, `GET /api/v3/jobs/{id}/tests`,
`POST /api/v3/runs/search`, the two `runs/facet-values` routes, and
`POST /api/v2/pipeline/search`.

My view: **do not build them.** They have the same shape as the 7 insights endpoints
already omitted — unbounded result sets that churn state on every `terraform refresh`
and make plans noisy for something nobody is converging on. Building them would be
coverage without utility.

The two exceptions are search rather than reporting: `pipeline/search` and
`runs/search` would let existing data sources filter server-side instead of draining
and filtering client-side, which is a real improvement to something already shipped.

If you want the reporting endpoints anyway — for dashboards, say — that changes the
answer, and it is a reasonable thing to want. Hence asking.

### Notification links

`GET /api/v3/notification/links` and `DELETE /api/v3/notification/links` are routed,
but **there is no create route, and DELETE takes no `:id`** — it deletes by criteria.
That combination means links are probably created as a side effect of something else,
in which case a resource with no Create would be the wrong model and a data source is
the honest answer. Recorded in `DESIGN.md` with whatever the the real wire format showed.

## 9. What the coverage number does and does not tell you

Worth stating because the number moved a long way for a reason that is easy to
misread.

Coverage of `internal/provider` was reported as 12% locally and 60% in CI. The
difference was **entirely tests skipping**, not code being untested: `resource.Test`
skips unless `TF_ACC=1`, which `task ci:test` sets and a developer does not — and 81
fake-backed cases that need no credentials at all were sitting behind that gate.
Converting them to `resource.UnitTest` took the local figure to 57% without a line of
new test code, and `TestCredentialFreeTestsUseUnitTest` now prevents it recurring.

So the honest summary is: the provider is better tested than 12% suggested and less
well tested than 60% suggested, because a large share of what runs is mock-backed.
**Mocks have been wrong in the same way as the client six times in this project**
(enumerated in `DESIGN.md`). Every one of those was found by reading production
the real wire format, not by a passing test. That is the specific reason section 1 asks for
real organizations rather than more test-writing time.
