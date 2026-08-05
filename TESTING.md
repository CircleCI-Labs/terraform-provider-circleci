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
| 3 | **GitLab Cloud** | `circleci/<uuid>` | Standalone. Checkout keys are documented as unavailable — we need to prove our resources fail cleanly rather than confusingly. |
| 4 | **GitLab self-managed** | `circleci/<uuid>` | Separate connection type from GitLab Cloud; worth confirming it is not silently different. |
| 5 | **Bitbucket Cloud** | `bitbucket/<org>` (`bb/<org>`) | Different feature set again; `set_github_status` is meaningless here. |
| 6 | **GitHub Enterprise Server** | `circleci/<uuid>` | The `github_server` provider value in `circleci_pipeline_definition` / `circleci_trigger` exists solely for this and is currently untested. |
| 7 | **CircleCI Server** | n/a — separate installation | The `deployment = "server"` path. Orthogonal to the above: Server does not route `/api/v3`, and `circleci_pipeline_definition` / `circleci_trigger` do not exist there at all. |

Numbers 1–6 are organizations on CircleCI Cloud. Number 7 is a whole
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

One context per integration type, so the same variable names resolve differently
per context and the test suite needs no per-VCS branching:

```
tfprovider-acc-github-app
tfprovider-acc-github-oauth
tfprovider-acc-gitlab-cloud
tfprovider-acc-gitlab-selfmanaged
tfprovider-acc-bitbucket-cloud
tfprovider-acc-github-server
tfprovider-acc-circleci-server
```

Restrict each context to this project only (`circleci_context_restriction`, or
the web UI) so an unrelated project cannot read the tokens.

### Variables in every context

| Variable | Notes |
|---|---|
| `CIRCLE_TOKEN` | Org-admin personal API token |
| `CIRCLECI_TEST_VCS_TYPE` | One of `github_app`, `github_oauth`, `gitlab`, `gitlab_selfmanaged`, `bitbucket`, `github_server`. **Tests use this to skip combinations the integration does not support** — see below |
| `CIRCLECI_TEST_ORG_ID` | Organization UUID |
| `CIRCLECI_TEST_ORG_SLUG` | e.g. `circleci/<uuid>` or `gh/<org>` |
| `CIRCLECI_TEST_ORG_NAME` | Display name |
| `CIRCLECI_TEST_ALT_ORG_ID` / `_SLUG` | A second org, for the org-move test |
| `CIRCLECI_TEST_PROJECT_ID` / `_SLUG` | The writable throwaway project |
| `CIRCLECI_TEST_STATIC_PROJECT_ID` / `_SLUG` / `_NAME` | The read-only project |
| `CIRCLECI_TEST_PIPELINE_ID` | A pipeline definition on the writable project |
| `CIRCLECI_TEST_TRIGGER_ID` / `_PROJECT_ID` | An existing trigger |
| `CIRCLECI_TEST_SCHEDULED_TRIGGER_ID` | A `schedule` event-source trigger |
| `CIRCLECI_TEST_CONTEXT_ID` / `_NAME` / `_ENV_VAR_NAME` | A pre-existing context to read |
| `CIRCLECI_TEST_WEBHOOK_ID` / `_NAME` / `_URL` | A pre-existing webhook |
| `CIRCLECI_TEST_RUNNER_NAMESPACE` | Namespace for runner resource classes |

### Only in the GitHub App context

| Variable | Notes |
|---|---|
| `CIRCLECI_TEST_GITHUB_APP_REPO_EXTERNAL_ID` | Numeric GitHub repo id |
| `CIRCLECI_TEST_GITHUB_APP_REPO_NAME` | `owner/repo` |

### Only in the GitHub OAuth context

`CIRCLECI_TEST_GITHUB_ORG_ID`, `CIRCLECI_TEST_GITHUB_ORG_SLUG`.

### Only in the GitHub Enterprise Server context

`CIRCLECI_TEST_GITHUB_SERVER_PROJECT_ID`, `CIRCLECI_TEST_GITHUB_SERVER_PIPELINE_ID`,
`CIRCLECI_TEST_GITHUB_SERVER_REPO_EXTERNAL_ID`.

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
export CIRCLECI_TEST_ORG_ID=...   # and the rest
TF_ACC=1 task test
```

Any variable left unset skips the tests that need it, naming it in the skip
message. There is no way to make a test silently pass without its fixture.

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
| `TestAccTriggerResourceWebhook` | `github_app`, `github_oauth` or `github_server` — `circleci_trigger` does not exist at all on GitLab, GitLab self-managed or Bitbucket Cloud |
| `TestAccScheduledTriggerDataSource` | `github_app`, `github_oauth` or `github_server` — a scheduled trigger is a `circleci_trigger` |

Every other acceptance test either behaves the same everywhere, or already
depends on a fixture variable TESTING.md documents as set in one context only
(`CIRCLECI_TEST_GITHUB_APP_REPO_EXTERNAL_ID`, `CIRCLECI_TEST_GITHUB_SERVER_*`,
...) — those skip on an unrelated integration for free, with no VCS check
needed, because the variable itself is simply unset there.

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
that into actual per-integration evidence needs seven CI jobs, each pointed at
a different context (see "Credentials layout" above); this gating is what
makes such a matrix meaningful instead of merely green. Until that matrix
exists, GitHub Enterprise Server, Bitbucket Cloud and CircleCI Server in
particular remain reasoned rather than measured, exactly as README.md's
compatibility matrix says.
