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
| `CIRCLECI_TEST_<KEY>_RUNNER_NAMESPACE` | Namespace for runner resource classes |

There is deliberately no `CONTEXT_*` or `WEBHOOK_*` row: the acceptance tests
for `circleci_context`, `circleci_context_environment_variable` and
`circleci_webhook` all create their own scratch context or webhook per run
(the org and project fixtures above are all they need) rather than reading a
shared, pre-existing one, so there is no such fixture for a variable to name.

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

### Deploy/release fixture data

`circleci_deploy_environment(s)`, `circleci_deploy_component(s)` and
`circleci_deploy_settings` read `/api/v2/deploy/*`, a family with no create
route at all (see `internal/circleci/deploy_environment.go`'s header comment):
deploy environments and components come into existence only when a real
pipeline job emits a `circleci run release log` (or `release plan` +
`release update`) marker. Two of the organizations above carry deliberately
seeded, **permanent** fixture data for this, on a dedicated branch that is not
cleaned up (there is no delete route to clean it up with, even if that were
wanted):

| Organization | Project | Branch |
|---|---|---|
| `CIRCLECI_TEST_GH_OAUTH_ORG_ID` (gh-oauth-cci-1) | `CIRCLECI_TEST_GH_OAUTH_PROJECT_ID` (project-1) | `deploy-fixture-seed` |
| `CIRCLECI_TEST_GH_APP_ORG_ID` (gh-app-cci-1) | `CIRCLECI_TEST_GH_APP_PROJECT_ID` (test-repo) | `deploy-fixture-seed` |

Each branch's `.circleci/config.yml` carries one job per marker (a job's
*second* `release log` call was [NET] observed not to persist — see the
comments there) and is filtered to only run on that branch, so it never
affects the `main`-branch pipelines the rest of this suite's fixtures rely on.
Both organizations end up with environments `tf-fixture-staging` and
`tf-fixture-production`, components `tf-fixture-widget` (multiple versions,
both environments), `tf-fixture-widget-plus` (a deliberate substring-match
sibling of `tf-fixture-widget`) and `tf-fixture-planned` (seeded via the
`plan`/`update` pair rather than `log`). gh-app-cci-1 additionally carries a
component named `test-repo` and an environment named `default`, left behind
by the very first marker either organization ever received — CircleCI
defaults a marker's component to the project name and its environment to
`default` when neither flag is given, but ([NET], and easy to miss) only on
that very first marker; see the config there for the full comment.

No other project or organization in this suite was touched: two organizations
now have real, populated deploy data, and the rest still answer the same
empty `{"items":[],"next_page_token":""}` they always did (see
`deploy_data_sources_test.go`'s `testDeployEmptyOrganizationID` comment, and
the real-API test file below, for what that does — and does not — reveal
about "deploys never enabled" versus "enabled but unused").

The real-API counterpart to `deploy_data_sources_test.go` reads this fixture
data directly; see its header comment for the full inventory and what
remains unconfirmed even with it (labels, `archived_at`, and the all-zero
UUID sentinel all still have no producible path).

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
policy bundles. Each job therefore declares a `serial-group`, one per
organization — four groups for four jobs, since no two of these jobs write to
the same organization (see "Parallel-safe acceptance fixtures" below for how
that was confirmed, including for the one pair that used to share a group).
Four separate groups serialize nothing that does not need it; the config
comment explains what would put two jobs back in one group: a test writing
something account-wide rather than org-scoped, or a test that once again
points one job's fixtures at another job's organization.

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
the organization, the writable project (`PROJECT_ID` and `PROJECT_SLUG`), and
the runner namespace each of these four organizations already had claimed
(`RUNNER_NAMESPACE` — see the config's own comment on why that one could be
added at all: unlike every other fixture here, a namespace cannot be created
per test run, because CircleCI has no way to delete one). `STATIC_PROJECT_*`,
`PIPELINE_ID`, `TRIGGER_*`, `CONTEXT_*`, `WEBHOOK_*` and the
static `GH_APP_REPO_*` pair are deliberately absent rather than guessed: an
absent variable skips with its own name in the message, while a wrong one fails
against a nonsense identifier in a way that reads exactly like a regression.
`CONTEXT_*` and `WEBHOOK_*` in particular are not merely absent for lack of a
value — the acceptance tests for `circleci_context`,
`circleci_context_environment_variable` and `circleci_webhook` create their
own scratch context/webhook per run instead of reading a shared one, so they
were never going to need these two rows filled in. So
the artifact of a green run doubles as the to-do list for the next one.

## Parallel-safe acceptance fixtures

The four acceptance jobs used to need three serial groups rather than four
(see the config comment), because two of them wrote into the same
organization. The acceptance-test harness in `internal/provider` (naming,
cleanup and verification helpers, all prefixed `test`) is a small,
general-purpose harness that removes the need for that kind of coordination
going forward: any test can create its own private fixture object, name it so
it cannot collide with a concurrent run of anything else, and prove it is
really gone afterward, instead of reading and writing the shared
`testProjectID`/`testContextID`/... fixtures every other test also depends
on.

**Naming.** `testUniqueName(t, kind)` returns `tf-acc-<kind>-<test-slug>-
<MMDD-HHMM-ci>-<random>` — for example
`tf-acc-ctx-harnesscon-0822-1517-local-owaoc2`. Two sources of uniqueness, not
one: a coarse timestamp plus the CI job identifier (`CIRCLE_WORKFLOW_ID`,
falling back to `CIRCLE_BUILD_NUM`, then `"local"`) states roughly when and
(in CI) which job made an object, readable by a human with no decoding; a
random suffix is what actually guarantees no collision, including between two
runs that started in the same minute with no CI id at all. Neither alone
would do the job — a bare timestamp collides, a bare random string tells
nobody anything when they find it later.

**Cleanup.** `testRegisterCleanup(t, description, del)` wraps `t.Cleanup` so a
delete that itself fails is a loud test failure (`t.Error`, naming the object
and pointing at the leak detector below) rather than a silently leaked
object — which is exactly how two real leaks were found in this project
before this file existed. `t.Cleanup` funcs run after `t.Fatal`/`t.FailNow`
and after a recovered panic in a subtest; they do **not** run if the process
itself dies (a genuine unrecovered panic, a SIGKILL from CircleCI's
no-output timeout, an interrupted `go test`) — stated as a limit, not
papered over, because nothing running inside that same process can fix it.
Proved live: a temporary test that created a context, registered its
cleanup, and then called `t.Fatal` immediately afterward still had the
context deleted — confirmed independently afterward with a fresh
`GET /api/v2/context?owner-id=...` listing that no longer contained it —
even though the test itself is reported failed.

**Verification.** A single status code lies about absence, and differently
per service (see "Why the organizations must be dedicated and disposable"
above and the measurements in `internal/circleci/context.go`,
`group.go`, `budget.go`): a deleted context or group answers 403, not 404,
and a budget's own delete answers 500 regardless of whether it worked.
`testAssertAbsent` and its typed wrappers (`testAssertContextGone`,
`testAssertGroupGone`, `testAssertResourceClassGone`) settle it by
re-listing the collection and checking for absence instead — the one signal
every one of these services answers unambiguously. `testAssertProjectGone`
is the one exception: a deleted project's slug is documented to answer a
real 404, and there is no list-projects route to check against instead (see
its doc comment), so `GetProject` really is the right check there.

A related gotcha lives on the delete side, not the check side: a cleanup
that calls `DeleteContext` (or a group delete) directly must treat a 403
(`circleci.IsUnauthorized`), not only a 404 (`circleci.IsNotFound`), as
"already gone" — both routes answer 403 for an object that no longer
exists. Getting this backwards does not silently pass; it fails loudly with
the *right* verdict for the *wrong* reason (a cleanup that runs after the
test's own explicit delete already succeeded reports the object as
undeletable). This was caught exactly this way, live, the first time the
demonstration test below ran against a real organization — see
`TestAccHarnessContextLifecycle`'s own comment for what that looked like.

**No shared mutable fixture.** The harness's own demonstration tests in
`internal/provider`, alongside the harness itself, show the whole thing end
to end, live, against three resource types:
`TestAccHarnessContextLifecycle` creates its own context in the active
organization (any organization, any class — context creation does not
require a standalone org); `TestAccHarnessProjectLifecycle` creates its own
project via `testCreateStandaloneProject`; `TestAccHarnessGroupLifecycle`
creates its own group. The latter two are gated to a standalone org, the
same as `TestAccCircleCiProjectResource`, since groups and a genuine project
create both require that class (see "Accounts required" above). None of the
three reads `testContextID`/`testProjectID`/a shared group fixture.

The group test is also where the 403-not-404 measurement was confirmed for
groups specifically, live: deleting a group, then repeating the delete and
separately reading it back, both answered `403 {"message":"Permission
denied."}` — not 404 — against gh-app-cci-1. `internal/circleci/group.go`'s
own `Get` doc comment ("a missing group is reported as an error satisfying
IsNotFound") does not describe this: `IsNotFound` only recognizes 404 and the
`ErrNotFound` sentinel, neither of which this measurement produced. Left as
found — fixing that comment (and whatever call site relies on it) is a
separate change with its own test, outside this harness's scope — but it is
exactly the kind of gap `testAssertGroupGone`'s list-based check exists to be
correct regardless of.

**Proving isolation.** Two things were run concurrently against the real
API, not merely reasoned about:

1. Three concurrent processes ran `TestAccHarnessContextLifecycle` against
   the *same* organization (gh-app-cci-1) at the same instant. All three
   passed, each creating and deleting a context with a distinct name.
2. `acceptance-gh-oauth`'s and `acceptance-gh-hybrid`'s exact
   `.circleci/config.yml` fixtures were run concurrently — one process per
   job, each running `TestAccHarnessContextLifecycle` against its own
   primary organization. Both passed, writing to gh-oauth-cci-1 and
   gh-oauth-cci-2 respectively.

`.circleci/scripts/find-leaked-fixtures.sh` (below) found nothing left over
by any of this.

**The serial group these two jobs used to share.** It existed because
`TestAccCircleCiProjectOrgUpdateResource`, run under `acceptance-gh-oauth`,
moves a project into `CIRCLECI_TEST_GH_OAUTH_ALT_ORG_ID` — set to
gh-oauth-cci-2, `acceptance-gh-hybrid`'s own primary organization. That test
was re-gated onto organization class (`testRequireStandaloneOrg`, the BUG P4
fix) after the shared group was introduced, and `acceptance-gh-oauth`'s
primary organization (gh-oauth-cci-1) is classic, not standalone — so the
gate now skips the move before it would ever reach the alt organization.
Reproduced directly, no network call needed since the gate fires first:

```sh
CIRCLECI_TEST_VCS_TYPE=github_oauth CIRCLECI_TEST_GH_OAUTH_ORG_SLUG=gh/gh-oauth-cci-1 \
  TF_ACC=1 go test ./internal/provider/ -run TestAccCircleCiProjectOrgUpdateResource -v
# --- SKIP: ... "needs a standalone organization, got the classic org gh/gh-oauth-cci-1"
```

and no other test reads `CIRCLECI_TEST_GH_OAUTH_ALT_ORG_ID`/`_ALT_ORG_SLUG`
(confirmed by grepping every `_test.go` file in this package). So today these
two jobs write to disjoint organizations and `.circleci/config.yml` gives
each its own serial group. **This is contingent, not permanent**: it holds
because no runnable test currently points one job's fixtures at another
job's organization and no test mutates anything account-wide. Either
changing would put two jobs back in one group — see the config comment for
exactly which.

**What still needs a serial group, and why this doesn't remove them
entirely.** Two runs of the *same* job still contend: they use the same
primary organization, and a resource with no per-test unique name yet (org
settings, policy bundles, OIDC claims — see "Why the organizations must be
dedicated and disposable" above) is still one shared, mutable, org-wide
object regardless of how many concurrent runs there are. The harness makes a
*newly written* test safe to run alongside anything else; it does not
retroactively make every existing test's fixture private. Each job's serial
group is what still protects those.

**The leak detector.** `.circleci/scripts/find-leaked-fixtures.sh` lists every
context, group, runner resource class and project across the four
organizations whose name matches the harness's naming scheme
(`^tf-acc-.+-[0-9]{4}-[0-9]{4}-` — the run-stamp's two four-digit groups, not
merely the `tf-acc-` prefix: this repository already has *permanent* fixtures
named `tf-acc-fixture` and `tf-acc-adoptable`, predating this harness and
chosen by a human, which the bare prefix wrongly flagged the first time this
script ran). Run it with an admin `CIRCLE_TOKEN` for all four organizations:

```sh
CIRCLE_TOKEN=... .circleci/scripts/find-leaked-fixtures.sh
```

It exits nonzero when it finds anything, so it can run as a CI step after the
acceptance suite, distinct from and not swallowed by whether the tests
themselves passed. It does not check spend budgets (no name field to match
against) or organizations (no list-all-organizations route exists; a leaked
`circleci_organization` test is a fifth organization with no fixed id to
start from, and has to be found by hand in the org picker). As of this
writing it reports nothing across all four organizations.

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
