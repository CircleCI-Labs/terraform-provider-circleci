<!-- Copyright (c) CircleCI -->
<!-- SPDX-License-Identifier: MPL-2.0 -->

# Acceptance testing

This provider's acceptance tests create and destroy **real** CircleCI objects.
This document lists the accounts they need, why those accounts must be
disposable, and how to wire the credentials into CI.

Unit and mock-backed tests need none of this — `task test` passes on a fresh
checkout with no credentials, and every acceptance test skips with a message
naming the variable it is missing.

## Why the organizations must be dedicated and disposable

Not a precaution. Several resources have an org-wide or irreversible blast
radius, and the tests exercise all of them:

| Resource | What a test run does to the organization |
|---|---|
| `circleci_organization` | **Creates and deletes organizations.** |
| `circleci_organization_settings` | Flips org-wide toggles: AI features, runner terms-of-service acceptance, private orbs, image and resource-class brownouts, whether running is disabled. |
| `circleci_config_policy_bundle` | `POST .../policy-bundle` **replaces the entire bundle**. Against an org with real governance rules, a test run deletes them. Destroy uploads an *empty* bundle. |
| `circleci_oidc_custom_claims` | Destroy resets the org's OIDC claims, changing what cloud providers will accept. |
| `circleci_orb_version` | **Publishing an orb version is irreversible** — there is no delete endpoint. Test runs permanently add versions to a namespace. |
| `circleci_orb_namespace` | Deleting a namespace destroys every orb under it. Organizations are normally limited to one namespace. |
| `circleci_group`, `circleci_project_group` | Creates and deletes groups and re-writes their full project role grants — i.e. permissions. |
| `circleci_project`, `circleci_checkout_key` | Creates and deletes projects and their deploy/user keys. |
| `circleci_runner_resource_class` | Deletes resource classes, with `force` cascading to their tokens. |

Two of these — the policy bundle and the OIDC claims — are *security controls*.
A test run against a production organization would silently disable governance.

So: **one organization per integration type, owned by us, containing nothing
anyone depends on.** Not a spare team's org, not a personal org with real
projects in it.

## Accounts required

CircleCI behaves differently per VCS integration, and the differences are real
(checkout keys are unavailable on GitLab and GitHub App projects; legacy
schedules only exist on GitHub OAuth and Bitbucket; groups require a standalone
org). To test what we ship, we need one of each.

| # | Integration | CircleCI org slug shape | Why it is needed |
|---|---|---|---|
| 1 | **GitHub App** | `circleci/<uuid>` | The modern default. Pipeline definitions and triggers use `github_app`. Groups/RBAC require a standalone org. |
| 2 | **GitHub OAuth** | `github/<org>` (`gh/<org>`) | The legacy path most existing customers are on. The only place legacy scheduled pipelines and `github_oauth` triggers exist. |
| 3 | **GitHub OAuth + GitHub App** | `github/<org>` (`gh/<org>`) | A hybrid: OAuth-connected, and *also* carrying a GitHub App installation. Confirmed distinct from #2 — `GET /api/v2/github-app/organization/{id}/installation` answers 200 here and 404 on an OAuth-only org — so it is the only fixture that can point the GitHub App routes at an organization whose projects are OAuth projects. |
| 4 | **GitLab Cloud** | `circleci/<uuid>` | Standalone. Checkout keys are documented as unavailable — we need to prove our resources fail cleanly rather than confusingly. |
| 5 | **GitLab self-managed** | `circleci/<uuid>` | Separate connection type from GitLab Cloud; worth confirming it is not silently different. |
| 6 | **Bitbucket Cloud** | `bitbucket/<org>` (`bb/<org>`) | Different feature set again; `set_github_status` is meaningless here. |
| 7 | **GitHub Enterprise Server** | `circleci/<uuid>` | The `github_server` provider value in `circleci_pipeline_definition` / `circleci_trigger` exists solely for this and is currently untested. |
| 8 | **CircleCI Server** | n/a — separate installation | The `deployment = "server"` path. Orthogonal to the above: Server does not route `/api/v3`, and `circleci_pipeline_definition` / `circleci_trigger` do not exist there at all. |

Numbers 1–7 are organizations on CircleCI Cloud. Number 8 is a whole
installation, and is the one that needs the most lead time.

### Per organization, please also create

- A **throwaway repository** we can attach a project to, containing a trivial
  `.circleci/config.yml`. It will accumulate pipeline definitions and triggers.
- A second **static repository/project** that tests only read, never mutate.
  Some data-source tests assert against a stable object.
- An **API token** belonging to a user with **organization admin** rights.
  Admin is required for org settings, groups and project role grants.
  `circleci_checkout_key` with `type = "user-key"` additionally requires a
  *personal* token rather than a project token.

## Credentials layout in CircleCI

Every per-integration fixture variable is named `CIRCLECI_TEST_<KEY>_<SUFFIX>`
— see the key table and the suffix list in README.md's "Fixture identifiers"
section, which is the canonical reference for the naming scheme. Because the
integration is baked into the variable *name* rather than into which context
supplies it, all seven integrations' fixtures can be configured side by side
without a context each.

The split is by *sensitivity*, not by integration:

- **The token lives in a context**, and it is the only thing in it:

  ```
  terraform-provider-acc-token
  ```

  Restrict it to this project *and* to the `labs` branch
  (`circleci_context_restriction`, or the web UI) so neither an unrelated
  project nor an unreviewed branch can read it. That restriction is also the
  control that stops a pull request from a fork reaching the token, because
  CircleCI enforces it before the job starts.

- **Every fixture identifier lives in `.circleci/config.yml`**, in the
  `environment` block of the acceptance job for that organization. Org and
  project UUIDs and slugs are not secrets, and putting them in the config makes
  "which organization does CI write to?" a reviewable diff instead of an
  invisible edit in the context UI. See "In CI" below.

CircleCI Server is a separate axis (deployment, not VCS integration — see
"Accounts required" above) and keeps its own context, since its variables
(`CIRCLE_HOST`, `CIRCLE_DEPLOYMENT`, `CIRCLE_RUNNER_HOST`) are not part of this
per-integration scheme at all:

```
tfprovider-acc-circleci-server
```

### Seeding the fixture variables

A variable may be left out entirely, or set to a placeholder (`REPLACE_ME`,
`TODO`, or `CHANGEME`; see README.md's "Placeholder values skip cleanly").
A placeholder is treated exactly like an unset variable, so where it helps to
have the full list visible and fillable in one place — a context UI, say — a
placeholder keeps the name on screen without ever making a test run against a
nonsense organization. In `.circleci/config.yml` the same job is served by
simply omitting the line: the acceptance jobs there set only the identifiers
that exist, and each missing one produces a skip that names itself.
`CIRCLECI_TEST_VCS_TYPE` then picks, per CI job, which integration that job's
tests exercise.

### The variables

`CIRCLE_TOKEN` is the only one of these that belongs in the context. Every
other row is a fixture identifier and belongs in the job's `environment` block
in `.circleci/config.yml`.

| Variable | Notes |
|---|---|
| `CIRCLE_TOKEN` | Org-admin personal API token |
| `CIRCLECI_TEST_VCS_TYPE` | One of `github_app`, `github_oauth`, `github_hybrid`, `gitlab`, `gitlab_selfmanaged`, `bitbucket`, `github_server`. **Tests use this to select which integration's variables to read, and to skip combinations the integration does not support** — see below |
| `CIRCLECI_TEST_<KEY>_ORG_ID` / `_ORG_SLUG` / `_ORG_NAME` | The primary organization for that integration |
| `CIRCLECI_TEST_<KEY>_ALT_ORG_ID` / `_ALT_ORG_SLUG` | A second org, for the org-move test |
| `CIRCLECI_TEST_<KEY>_PROJECT_ID` / `_PROJECT_SLUG` | The writable throwaway project |
| `CIRCLECI_TEST_<KEY>_STATIC_PROJECT_ID` / `_STATIC_PROJECT_SLUG` / `_STATIC_PROJECT_NAME` | The read-only project |
| `CIRCLECI_TEST_<KEY>_PIPELINE_ID` | A pipeline definition on the writable project |
| `CIRCLECI_TEST_<KEY>_TRIGGER_ID` / `_TRIGGER_PROJECT_ID` | An existing trigger |
| `CIRCLECI_TEST_<KEY>_SCHEDULED_TRIGGER_ID` | A `schedule` event-source trigger |
| `CIRCLECI_TEST_<KEY>_CONTEXT_ID` / `_CONTEXT_NAME` / `_CONTEXT_ENV_VAR_NAME` | A pre-existing context to read |
| `CIRCLECI_TEST_<KEY>_WEBHOOK_ID` / `_WEBHOOK_NAME` / `_WEBHOOK_URL` | A pre-existing webhook |
| `CIRCLECI_TEST_<KEY>_RUNNER_NAMESPACE` | Namespace for runner resource classes |

`<KEY>` is `GH_APP`, `GH_OAUTH`, `GH_HYBRID`, `GH_SERVER`, `GL_CLOUD`, `GL_SM`
or `BB_CLOUD` — fill in the row for every integration you have an account for.
`GH_HYBRID` is account #3 above (OAuth-connected *and* carrying a GitHub App
installation); it needs its own row precisely because it is a different
organization from `GH_OAUTH`, not another name for it.

### Static, single-integration variables

These are not keyed by the active integration — the test that reads one
specifically needs that integration, regardless of `CIRCLECI_TEST_VCS_TYPE`:

| Variable | Notes |
|---|---|
| `CIRCLECI_TEST_GH_OAUTH_ORG_ID` / `_ORG_SLUG` | A GitHub OAuth org, for tests that assert on GitHub-OAuth-only behaviour |
| `CIRCLECI_TEST_GH_APP_REPO_EXTERNAL_ID` | Numeric GitHub repo id, GitHub App integration |
| `CIRCLECI_TEST_GH_APP_REPO_NAME` | `owner/repo`, GitHub App integration |
| `CIRCLECI_TEST_GH_SERVER_PROJECT_ID` | UUID of a GitHub Server backed project |
| `CIRCLECI_TEST_GH_SERVER_PIPELINE_ID` | UUID of a pipeline in that project |
| `CIRCLECI_TEST_GH_SERVER_REPO_EXTERNAL_ID` | External ID of the GitHub Server repository |

### Only in the CircleCI Server context

| Variable | Notes |
|---|---|
| `CIRCLE_HOST` | The installation origin, e.g. `https://circleci.example.com`. A bare origin — no `/api/v2` |
| `CIRCLE_DEPLOYMENT` | `server` |
| `CIRCLE_RUNNER_HOST` | Usually the same origin: Server serves the runner API itself |

## Running them

```sh
export CIRCLE_TOKEN=...
export CIRCLECI_TEST_VCS_TYPE=github_app
export CIRCLECI_TEST_GH_APP_ORG_ID=...   # and the rest of CIRCLECI_TEST_GH_APP_*
TF_ACC=1 task test
```

Any variable left unset, or left at a placeholder value, skips the tests that
need it, naming both which variable and which of the two it was in the skip
message. There is no way to make a test silently pass without its fixture.

### Reproducing a CI job exactly

The four organizations CI runs against are disposable and their identifiers are
not secrets, so the fixture values are in `.circleci/config.yml` in plain text,
in each acceptance job's `environment` block. To reproduce one locally, copy
that block:

```sh
export CIRCLE_TOKEN=...                  # your own token, not CI's
export CIRCLECI_TEST_VCS_TYPE=github_app
export CIRCLECI_TEST_GH_APP_ORG_ID=e75c804e-7f5c-4506-9dad-03fc86af39d1
export CIRCLECI_TEST_GH_APP_ORG_SLUG=circleci/Va2k7FVcHE7EyFDbRioifr
export CIRCLECI_TEST_GH_APP_ORG_NAME=gh-app-cci-1
export CIRCLECI_TEST_GH_APP_PROJECT_ID=18ae5fa4-d11c-4fe1-a1a7-fbcae4d037de

TF_ACC=1 task test -- ./internal/provider/...
```

The other three jobs differ only in which key they set: `GH_OAUTH`
(gh-oauth-cci-1, plus `_ALT_ORG_ID`/`_ALT_ORG_SLUG` for the org-move test),
`GH_HYBRID` (gh-oauth-cci-2) and `GL_CLOUD` (gitlab-test). Take the values from
the config rather than from here, so there is one copy to keep correct.

To run a single test, add `-run`:

```sh
TF_ACC=1 task test -- -run TestAccTriggerResourceWebhook ./internal/provider/...
```

### Seeing the coverage summary locally

`task test` and `task ci:test` both run `gotestsum`, whose default `pkgname`
format prints one line per package and *discards* a passing package's own
stdout — which is where the coverage block described below is written. It is
not missing, it is swallowed. Ask for a format that keeps it:

```sh
GOTESTSUM_FORMAT=standard-quiet TF_ACC=1 task test -- ./internal/provider/...
```

Plain `go test ./internal/provider/...` shows it too. The CI jobs set that same
variable, for the same reason.

## In CI

`.circleci/config.yml` has four acceptance jobs, one per organization we have:
`acceptance-gh-app`, `acceptance-gh-oauth`, `acceptance-gh-hybrid` and
`acceptance-gl-cloud`. Each runs `./internal/provider/...` — where every
acceptance test lives — with `CIRCLECI_TEST_VCS_TYPE` set to its integration
and that integration's fixture variables set inline. `task ci:test` sets
`TF_ACC=1`, so nothing needs to switch the tests on; what the jobs supply is
the token and the fixtures.

**They do not run on a push, and they do not gate anything.** They talk to a
live installation, so they fail for reasons unrelated to the commit — an API
blip, a rate limit, an object a killed earlier run never destroyed — and a red
build that means "maybe the API was unwell" quickly means nothing at all. They
also *cannot* run on a pull request: the context is restricted to the `labs`
branch, so a PR build would fail on a missing credential rather than skip.

Instead they live in their own workflow, compiled into a pipeline only when the
`run-acceptance-tests` pipeline parameter is true. A push cannot set it; a
scheduled trigger or an authenticated API call can:

```sh
curl -X POST https://circleci.com/api/v2/project/<project-slug>/pipeline \
  -H "Circle-Token: $CIRCLE_TOKEN" -H 'Content-Type: application/json' \
  -d '{"branch":"labs","parameters":{"run-acceptance-tests":true}}'
```

Nightly is the intended cadence, with that on-demand trigger for after you have
touched a resource — the case where a failure is actually attributable to
something.

**Serial groups.** Two runs against the same organization corrupt each other:
these tests create and delete projects, flip org-wide settings and replace
policy bundles. Each job therefore declares a `serial-group`, and the grouping
follows the organizations rather than the jobs — one group per organization,
except that `acceptance-gh-oauth` and `acceptance-gh-hybrid` share one, because
the OAuth job's org-move test moves a project into the hybrid job's
organization. Four separate groups would serialize nothing that needs it and
one shared group would serialize everything, turning four ~40-minute jobs into
a ~160-minute nightly for no safety gained. The config comment explains what
would change that: a test writing something account-wide rather than
org-scoped.

**What each job stores.** `test-reports/` goes up as an artifact, including:

| File | What it is |
|---|---|
| `vcs-coverage.txt` | The coverage block below, plus how many `TestAcc*` tests ran and skipped, plus every skip with its reason |
| `acceptance.log` | The full test output |
| `tests.xml`, `coverage.out` | The usual JUnit and coverage files |

**Two things fail the job even when every test passes**, because a run that
talked to no organization at all passes every assertion it makes:

- No coverage block was printed. For a job that sets `CIRCLECI_TEST_VCS_TYPE`,
  that means no VCS-gated test ever reached the gate.
- Every `TestAcc*` test skipped. Green, and nothing created.

Neither fires on "Exercised by this run (0)", which is the *correct* result for
`acceptance-gl-cloud` and `acceptance-gh-hybrid`: all four VCS-gated tests
require a GitHub App, OAuth, Server or Bitbucket fixture (see the table below),
so none of them can run there. The ran/skipped counts are what to read in those
two jobs.

**Filling in the rest.** Each job sets only the identifiers that exist today —
the organization and the writable project. `PROJECT_SLUG`, `STATIC_PROJECT_*`,
`PIPELINE_ID`, `TRIGGER_*`, `CONTEXT_*`, `WEBHOOK_*`, `RUNNER_NAMESPACE` and the
static `GH_APP_REPO_*` pair are deliberately absent rather than guessed: an
absent variable skips with its own name in the message, while a wrong one fails
against a nonsense identifier in a way that reads exactly like a regression. So
the artifact of a green run doubles as the to-do list for the next one.

## What a run covers, and how to tell

Most resources behave identically on every VCS integration, so most
acceptance tests carry no VCS branching at all: the same `CIRCLECI_TEST_*`
variable names resolve to a GitHub App project in one run and a Bitbucket
project in another, and the test never needs to know which. That is by
design — see "Credentials layout" above.

A few resources genuinely do not (README.md's compatibility matrix), and a
test that exercises one of those calls `testRequireVCSType(t, ...)` before it
builds any Terraform configuration. It skips, naming both what the test needs
and what `CIRCLECI_TEST_VCS_TYPE` was actually set to, when the configured
fixture cannot support the feature — so pointing the whole suite at, say, a
GitLab organization produces named skips instead of real-API failures that
read like regressions. Currently gated this way:

| Test | Requires |
|---|---|
| `TestAccCircleCiProjectResource` | `github_oauth` or `bitbucket` — `build_fork_prs = true` is unconfirmed on GitLab and a documented **no** on GitHub App, GitHub Enterprise Server and GitLab self-managed |
| `TestAccCircleCiProjectOrgUpdateResource` | `github_oauth` or `bitbucket` — same `build_fork_prs = true` assertion, against the same primary organization fixture |
| `TestAccTriggerResourceWebhook` | `github_app`, `github_oauth` or `github_server` — `circleci_trigger` does not exist at all on GitLab, GitLab self-managed or Bitbucket Cloud |
| `TestAccScheduledTriggerDataSource` | `github_app`, `github_oauth` or `github_server` — a scheduled trigger is a `circleci_trigger` |

Every other acceptance test either behaves the same everywhere, or already
depends on a static, single-integration fixture variable documented above
(`CIRCLECI_TEST_GH_APP_REPO_EXTERNAL_ID`, `CIRCLECI_TEST_GH_SERVER_*`, ...) —
those skip on an unrelated integration for free, with no VCS check needed,
because the variable itself is simply unset there.

**`github_hybrid` is deliberately absent from all four rows above.** A hybrid
organization's *connection* is GitHub OAuth, so each of those four features
would in fact work there — but that is exactly the point: running them against
`github_hybrid` re-measures `github_oauth` at the cost of a whole extra CI job,
because the App installation a hybrid org carries changes nothing any of these
four tests touches. Where the key earns its keep is the `github-app/*` routes
(`circleci_github_app_installation`, `circleci_github_app_repository`), which
today have only fake-backed tests; the first live test of those is what should
name `github_hybrid`. Until then a `github_hybrid` run reports these four as
named skips, which is the honest answer rather than a silent one.

At the end of a run, `TestMain` prints which of the VCS-gated tests above ran
against the configured integration and which skipped because the fixture was
a different one:

```
=== VCS integration coverage (CIRCLECI_TEST_VCS_TYPE=github_app) ===
Exercised by this run (2):
  TestAccScheduledTriggerDataSource (github_app)
  TestAccTriggerResourceWebhook (github_app)
Skipped, configured fixture is a different integration (1):
  TestAccCircleCiProjectResource (needs github_oauth/bitbucket, got github_app)
```

(A test appears in "Exercised" once `CIRCLECI_TEST_VCS_TYPE` is a supported
value for it — the summary says nothing about whether its assertions passed;
`go test`'s own output is still the source of truth for that.) This block is
printed once per invocation and only when at least one VCS-gated test ran, so
a credential-less checkout stays silent.

**What this does not give you.** The gate stops a test from running somewhere
it cannot possibly pass; it does not make the test run anywhere it *can*. A
single CI job still only ever has one `CIRCLECI_TEST_VCS_TYPE` configured, so
a single green run still only measures one integration — the "Exercised by
this run" list above is that run's honest ceiling, not the suite's. Turning
that into per-integration evidence takes one job per integration, which is
what the four jobs in "In CI" above are; this gating is what makes such a
matrix meaningful instead of merely green.

Four of the eight rows in "Accounts required" have a job today. GitLab
self-managed, Bitbucket Cloud, GitHub Enterprise Server and CircleCI Server
have no organization provisioned, so they have no job either, and they remain
reasoned rather than measured exactly as README.md's compatibility matrix
says. Adding one is adding a job: copy an existing acceptance job, change the
key in its `environment` block, and give it its own `serial-group`.
