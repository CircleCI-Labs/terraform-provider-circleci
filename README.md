# CircleCI Terraform Provider

> This repository is part of CircleCI Labs — solutions developed by CircleCI's field
> engineering team based on real customer needs.
>
> ✅ **Created by Field Engineers @ CircleCI**
> ✅ **Used by real CircleCI customers**
> ❌ **NOT officially supported by CircleCI support**

> [!IMPORTANT]
> Read that third line before adopting this in production. A Terraform provider is not a
> script you run once — it takes ownership of live CircleCI objects and records them in
> your state file. Issues and pull requests here are handled on a best-effort basis by
> field engineering, not by CircleCI support.
>
> The work here is intended for upstream contribution to
> [`CircleCI-Public/terraform-provider-circleci`](https://github.com/CircleCI-Public/terraform-provider-circleci).
> This notice applies to the Labs fork and should be removed if these changes land there.

The CircleCI Terraform Provider enable customers to manage CircleCI projects with IaC patterns, matching the same patterns used to manage GitHub repos. For large-scale organizations, this enables automated project creation for new teams or projects.

## Installing

This fork is published to the Terraform Registry under the **`CircleCI-Labs`** namespace,
which is a different provider from the official `CircleCI-Public/circleci`. Use this
address to get the resources documented here:

```hcl
terraform {
  required_providers {
    circleci = {
      source  = "CircleCI-Labs/circleci"
      version = "~> 0.6"
    }
  }
}
```

> [!IMPORTANT]
> `CircleCI-Labs/circleci` and `CircleCI-Public/circleci` are two separate providers, not
> two versions of one. Terraform tracks each by its full address, so switching between them
> is a provider migration and not a version bump — state written by one is not read by the
> other. Pick one per configuration.

## Usage
The current documentation is found [here](https://registry.terraform.io/providers/CircleCI-Labs/circleci/latest/docs).
Define the provider:
```hcl
terraform {
  required_providers {
    circleci = {
      source = "CircleCI-Labs/circleci"
      version = "~> 0.6"
    }
  }
}

provider "circleci" {
  # host defaults to https://circleci.com and deployment to "cloud".
  #key = "*****" # prefer the CIRCLE_TOKEN environment variable
}

# For CircleCI Server, tell the provider which kind of installation this is.
# Server does not route the v3 API, so resources that require it are unavailable
# and report an explicit error. See the compatibility table below.
provider "circleci" {
  alias       = "server"
  host        = "https://circleci.example.com"
  deployment  = "server"
  runner_host = "https://circleci.example.com"
}
```
Start defining Data Sources and Resources. For example:
```hcl
resource "circleci_project" "test_project" {
  name   = "Project_Name"
  org_id = "********-****-****-****-****************"
}
```

Use the Official [CircleCI API documentation](https://circleci.com/docs/api/v2/index.html) to check which valid values might be needed for some resources.

## Compatibility

CircleCI does not offer the same features on every VCS integration, and this
provider does not yet implement everything CircleCI offers. Those are two
different gaps, so they are tracked separately:

- **VCS** — does the CircleCI *API* support this on that integration?
- **Provider** — has this provider *implemented* it?

A row where the VCS supports something and the provider does not is a roadmap
item. A row where the provider implements something the VCS cannot do is a bug —
please open an issue.

Columns: **GH App** = GitHub App · **GH OAuth** = classic GitHub OAuth ·
**GitLab** = gitlab.com · **GitLab SM** = GitLab self-managed ·
**BB** = Bitbucket Cloud · **GHES** = GitHub Enterprise Server ·
**Server** = CircleCI Server (self-hosted CircleCI, an orthogonal axis).

`?` means we could not confirm it from CircleCI's documentation. A `?` is
deliberate: it is safer than a wrong `yes`. It is the same verdict the per-type
pages spell **Unverified**, and the two must not disagree about a cell.

### Which document says what

Availability is described in three places, which answer different questions.
Where they overlap, the narrower one wins:

| Document | Authoritative for |
|---|---|
| Each type's `## Availability` table in the [registry docs](https://registry.terraform.io/providers/CircleCI-Labs/circleci/latest/docs) | **That one type on the Cloud/Server axis**, plus its routes, organization-type requirement and token requirement |
| **This matrix** | **The VCS-integration axis** — the columns below |
| The summary table on the provider index page | **Nothing.** It is a summary and must never disagree with either of the above |

If this matrix and a type's own page disagree about Cloud or Server, the type's
page is right and this table is the bug. If they disagree about a VCS
integration, this table is right, because the per-page table has no VCS column —
it carries the organization-type requirement instead.

### How well evidenced this is

> [!IMPORTANT]
> **Every column here is reasoned, not measured.** There is no per-integration CI
> matrix. `TESTING.md` enumerates the eight integration types and what each one
> is for; the tests whose result depends on `CIRCLECI_TEST_VCS_TYPE` now skip by
> name when it names an unsupported integration (see "What a run covers" in
> `TESTING.md`), but a single CI job still only ever configures one integration
> at a time, so it has never been *run* against most of them. The cells below
> come from CircleCI's own documentation and from which routes each deployment
> actually serves.
>
> Three columns deserve naming, because they are the weakest and it is not
> obvious from looking at them:
>
> * **GHES** — the `github_server` provider value in `circleci_pipeline_definition`
>   and `circleci_trigger` exists for GitHub Enterprise Server and nothing else,
>   and has never been exercised.
> * **BB** — exercised no more than GHES. Its documented differences (no `group`
>   context restrictions, no `schedule` value in the trigger event-source enum)
>   are read off documentation, not observed.
> * **Server** — no installation has been available at all. Note that "the route
>   is v2" is *not* sufficient evidence on its own: `circleci_pipeline_definition`
>   is v2 and still unavailable there.
>
> Nothing here is *known* wrong for any column. Please open an issue if your
> installation disagrees with a cell — that is how these get upgraded from
> reasoned to measured.

### Implemented by this provider

Each row covers a resource **and the data sources that read the same thing**:
the `circleci_context` row also covers the `circleci_context` and
`circleci_contexts` data sources, the `circleci_orb*` row also covers
`circleci_orbs`, `circleci_orb_categories` and the singular orb data sources,
and so on. They call the same routes on the same integrations. Where read and
write genuinely differ, the row is split — see `circleci_pipeline_definition`.

| Terraform type | GH App | GH OAuth | GitLab | GitLab SM | BB | GHES | Server |
|---|:--:|:--:|:--:|:--:|:--:|:--:|:--:|
| `circleci_context` | yes | yes | yes | yes | yes | yes | [yes](COMPATIBILITY.md#context-owners-on-circleci-server-are-identified-by-account-id-not-slug) |
| `circleci_context_environment_variable` | yes | yes | yes | yes | yes | yes | yes |
| `circleci_context_restriction` (`project`, `expression`) | yes | yes | yes | yes | yes | yes | yes |
| `circleci_context_restriction` (`group`) | [?](COMPATIBILITY.md#circleci-has-two-unrelated-concepts-called-a-group) | [?](COMPATIBILITY.md#circleci-has-two-unrelated-concepts-called-a-group) | [?](COMPATIBILITY.md#circleci-has-two-unrelated-concepts-called-a-group) | [?](COMPATIBILITY.md#circleci-has-two-unrelated-concepts-called-a-group) | [**no**](COMPATIBILITY.md#bitbucket-cannot-restrict-a-context-by-group) | [?](COMPATIBILITY.md#circleci-has-two-unrelated-concepts-called-a-group) | [?](COMPATIBILITY.md#circleci-has-two-unrelated-concepts-called-a-group) |
| `circleci_project` | yes | yes | yes | yes | yes | yes | [yes](COMPATIBILITY.md#classic-organizations-still-need-one-v11-api-call-to-follow-a-project) |
| `circleci_project_settings` / `circleci_project` settings | yes | yes | [yes](COMPATIBILITY.md#gitlab-github-app-and-ghes-projects-use-a-uuid-slug-not-a-name) | [yes](COMPATIBILITY.md#gitlab-github-app-and-ghes-projects-use-a-uuid-slug-not-a-name) | yes | [yes](COMPATIBILITY.md#gitlab-github-app-and-ghes-projects-use-a-uuid-slug-not-a-name) | yes |
| ├ `build_fork_prs`, `forks_receive_secret_env_vars` | **no** | yes | [?](COMPATIBILITY.md#circlecis-own-documentation-disagrees-with-itself-about-gitlab-and-fork-pull-requests) | **no** | yes | **no** | yes |
| ├ `oss` | ? | yes | ? | ? | yes | ? | ? |
| ├ `set_github_status` | yes | yes | [yes](COMPATIBILITY.md#set_github_status-controls-vcs-status-updates-everywhere-not-just-github-checks) | [yes](COMPATIBILITY.md#set_github_status-controls-vcs-status-updates-everywhere-not-just-github-checks) | [yes](COMPATIBILITY.md#set_github_status-controls-vcs-status-updates-everywhere-not-just-github-checks) | yes | yes |
| `circleci_project_environment_variable` | yes | yes | yes | yes | yes | yes | yes |
| `circleci_checkout_key` | [**no**](COMPATIBILITY.md#checkout-keys-need-a-vcs-that-checks-out-over-ssh) | yes | [**no**](COMPATIBILITY.md#checkout-keys-need-a-vcs-that-checks-out-over-ssh) | [**no**](COMPATIBILITY.md#checkout-keys-need-a-vcs-that-checks-out-over-ssh) | yes | [**no**](COMPATIBILITY.md#checkout-keys-need-a-vcs-that-checks-out-over-ssh) | yes |
| `circleci_pipeline_definition` (read) | yes | yes | yes | yes | yes | yes | [**no**](COMPATIBILITY.md#pipeline-definitions-and-triggers-are-v2-routes-server-still-does-not-forward) |
| `circleci_pipeline_definition` (create/update/delete) | yes | [**no**](COMPATIBILITY.md#github-oauth-gitlab-and-bitbucket-cloud-have-no-stored-pipeline-definition) | [**no**](COMPATIBILITY.md#github-oauth-gitlab-and-bitbucket-cloud-have-no-stored-pipeline-definition) | [**no**](COMPATIBILITY.md#github-oauth-gitlab-and-bitbucket-cloud-have-no-stored-pipeline-definition) | [**no**](COMPATIBILITY.md#github-oauth-gitlab-and-bitbucket-cloud-have-no-stored-pipeline-definition) | yes | [**no**](COMPATIBILITY.md#pipeline-definitions-and-triggers-are-v2-routes-server-still-does-not-forward) |
| `circleci_trigger` | yes | [yes](COMPATIBILITY.md#github-oauth-triggers-use-the-same-endpoint-under-a-narrower-contract) | **no** | **no** | [**no**](COMPATIBILITY.md#bitbucket-cloud-triggers-have-no-schedule-event-source) | yes | [**no**](COMPATIBILITY.md#pipeline-definitions-and-triggers-are-v2-routes-server-still-does-not-forward) |
| `circleci_webhook` | yes | yes | yes | yes | yes | yes | yes |
| `circleci_runner_resource_class`, `circleci_runner_token` | yes | yes | yes | yes | yes | yes | [yes](COMPATIBILITY.md#self-hosted-runners-on-server-need-runner_host-pointed-at-the-installation) |
| `circleci_group` | [yes](COMPATIBILITY.md#groups-and-github-app-discovery-need-a-standalone-organization) | [**no**](COMPATIBILITY.md#groups-and-github-app-discovery-need-a-standalone-organization) | yes | yes | [**no**](COMPATIBILITY.md#groups-and-github-app-discovery-need-a-standalone-organization) | [yes](COMPATIBILITY.md#groups-and-github-app-discovery-need-a-standalone-organization) | [**no**](COMPATIBILITY.md#groups-and-github-app-discovery-need-a-standalone-organization) |
| `circleci_project_group` | [yes](COMPATIBILITY.md#groups-and-github-app-discovery-need-a-standalone-organization) | **no** | yes | yes | **no** | yes | [**no**](COMPATIBILITY.md#project-group-grants-are-not-exposed-on-circleci-server) |
| `circleci_organization_settings` | yes | yes | yes | yes | yes | yes | [**no**](COMPATIBILITY.md#circleci-server-does-not-route-the-v3-api-at-all) |
| `circleci_orb_namespace`, `circleci_orb`, `circleci_orb_version` | yes | yes | yes | yes | yes | yes | [**no**](COMPATIBILITY.md#circleci-server-does-not-route-the-v3-api-at-all) |
| `circleci_config_policy_bundle`, `circleci_config_policy_settings` | yes | yes | yes | yes | yes | yes | [yes](COMPATIBILITY.md#config-policies-need-the-scale-plan-on-cloud-or-circleci-server-42-or-later) |
| `circleci_oidc_custom_claims` | yes | yes | yes | yes | yes | yes | [yes](COMPATIBILITY.md#oidc-custom-claims-need-circleci-server-44-or-later-and-are-unavailable-air-gapped) |
| `circleci_url_orb_allow_list_entry` | yes | yes | [yes](COMPATIBILITY.md#the-url-orb-allow-list-cannot-authenticate-against-gitlab) | [yes](COMPATIBILITY.md#the-url-orb-allow-list-cannot-authenticate-against-gitlab) | yes | yes | [?](COMPATIBILITY.md#some-v2-routes-are-owned-by-a-backend-a-server-installation-may-not-forward) |
| `circleci_otel_exporter` | yes | yes | yes | yes | yes | yes | [?](COMPATIBILITY.md#some-v2-routes-are-owned-by-a-backend-a-server-installation-may-not-forward) |
| `circleci_github_app_installation`, `circleci_github_app_repository`, `circleci_github_app_repositories` (read-only) | yes | [**no**](COMPATIBILITY.md#github-app-discovery-has-nothing-to-report-without-an-actual-github-app-connection) | [**no**](COMPATIBILITY.md#github-app-discovery-has-nothing-to-report-without-an-actual-github-app-connection) | [**no**](COMPATIBILITY.md#github-app-discovery-has-nothing-to-report-without-an-actual-github-app-connection) | [**no**](COMPATIBILITY.md#github-app-discovery-has-nothing-to-report-without-an-actual-github-app-connection) | [?](COMPATIBILITY.md#whether-github-enterprise-server-reports-as-a-github-app-installation-is-unchecked) | [**no**](COMPATIBILITY.md#github-app-discovery-has-nothing-to-report-without-an-actual-github-app-connection) |

### Implemented, and the same on every VCS integration

These types are scoped to an organization, a user, or a pipeline *run*, so no
part of their behaviour depends on which VCS the organization is connected to.
They are listed separately rather than as more rows of seven `yes` cells, so the
matrix above stays a statement about VCS differences.

Read-only entries are data sources; ephemeral entries never enter state at all.

| Terraform type | Cloud | Server | Note |
|---|:--:|:--:|---|
| `circleci_organization` | yes | [?](COMPATIBILITY.md#creating-an-organization-on-circleci-server-is-untested) | Create is find-or-create for VCS-backed organizations |
| `circleci_pipeline_run`, `circleci_pipeline_run_config`, `circleci_pipeline_run_values`, `circleci_pipeline_run_workflows` (read-only) | yes | [yes](COMPATIBILITY.md#the-v2-api-is-forwarded-on-circleci-server-by-default) | A *run*, not a definition. See the object model guide |
| `circleci_workflow`, `circleci_workflow_jobs`, `circleci_job` (read-only) | yes | [yes](COMPATIBILITY.md#the-v2-api-is-forwarded-on-circleci-server-by-default) | |
| `circleci_user`, `circleci_user_collaborations` (read-only) | yes | [yes](COMPATIBILITY.md#the-v2-api-is-forwarded-on-circleci-server-by-default) | Needs a personal token, not a project token |
| `circleci_runner_task_counts`, `circleci_runners` (read-only) | yes | [yes](COMPATIBILITY.md#self-hosted-runners-on-server-need-runner_host-pointed-at-the-installation) | |
| `circleci_usage_export`, `circleci_ephemeral_runner_token` (ephemeral) | yes | [yes](COMPATIBILITY.md#usage-export-and-ephemeral-runner-tokens-work-the-same-on-cloud-and-server) | |
| `circleci_insights_summary`, `circleci_insights_workflows`, `circleci_insights_flaky_tests` (read-only) | yes | [depends](COMPATIBILITY.md#insights-on-server-depends-on-whether-that-installation-runs-the-insights-service) | Approximate, recomputed daily, unusable for cost reporting |
| `circleci_notification_channel_config`, `circleci_notification_preferences`, `circleci_notification_integration_status` | yes | [**no**](COMPATIBILITY.md#circleci-server-does-not-route-the-v3-api-at-all) | Plus `circleci_notification_integrations` and `circleci_notification_links` (read-only) |
| `circleci_ios_signing_certificate`, `circleci_ios_signing_config` | yes | [**no**](COMPATIBILITY.md#circleci-server-does-not-route-the-v3-api-at-all) | Only usable by a macOS executor. Both take [write-only credentials](COMPATIBILITY.md#six-resources-accept-a-secret-as-write-only-and-never-store-it) |
| `circleci_deploy_component`, `circleci_deploy_components`, `circleci_deploy_environment`, `circleci_deploy_environments`, `circleci_deploy_settings` (read-only) | yes | [**no**](COMPATIBILITY.md#deploy-and-release-tracking-routes-are-absent-from-servers-gateway) | |
| `circleci_catalog_offerings` (read-only) | yes | [**no**](COMPATIBILITY.md#circleci-server-does-not-route-the-v3-api-at-all) | The resource classes an organization may use |
| `circleci_audit_log_config` | [yes](COMPATIBILITY.md#audit-log-streaming-is-gated-on-the-scale-plan-out-of-caution-not-confirmed-routing) | [**no**](COMPATIBILITY.md#audit-log-streaming-is-gated-on-the-scale-plan-out-of-caution-not-confirmed-routing) | Plus `circleci_audit_log_access` and `circleci_audit_log_configs` (read-only) |
| `circleci_budget` | [yes](COMPATIBILITY.md#spend-budgets-are-served-on-an-undocumented-fixed-address) | [**no**](COMPATIBILITY.md#spend-budgets-are-served-on-an-undocumented-fixed-address) | An undocumented private route. Plus `circleci_budgets` (read-only) |

A few caveats surprise people more than the rest: checkout keys are unavailable on
GitHub App, GitHub Enterprise Server, GitLab.com and GitLab self-managed projects, because
those integrations check out over HTTPS and never need one; and CircleCI Server does not
route the `/api/v3` API at all, so anything built on it — organization settings, orbs,
notifications, iOS signing and more — reports an explicit error there rather than working.
[`COMPATIBILITY.md`](./COMPATIBILITY.md) has the reasoning behind every other cell above,
organised by why the limitation exists rather than by which resource it affects.

Two deprecated type names are still registered so existing configurations keep
working: `circleci_pipeline` for both the resource and the data source now called
`circleci_pipeline_definition`. Their availability is whatever
`circleci_pipeline_definition`'s is, since they are the same implementation. See
the renaming guide.

The three provider functions — `orb_ref`, `project_slug`, `parse_project_slug` —
are pure string handling and make no API call, so neither the VCS integration nor
`deployment` affects them.

### Supported by CircleCI, not implemented here

[`API-COVERAGE.md`](./API-COVERAGE.md) is the full route-by-route inventory, built
from the routes CircleCI actually serves rather than from the published OpenAPI
spec — the spec both omits routes that exist and describes routes that are never
wired up. Use it to check a specific endpoint; the summary below is the
reasoning.

| Capability | API | Why not |
|---|---|---|
| Legacy scheduled pipelines | v2 `/project/{slug}/schedule` | Superseded. Use `circleci_trigger` with `event_source_provider = "schedule"`; see the migration guide. Also GitHub OAuth and Bitbucket only |
| Additional project SSH keys | v1.1 only | Not served on CircleCI Server. Checkout keys are the supported mechanism, and GitLab projects ship a pre-existing key that must not be deleted |
| 7 of 10 insights endpoints | v2 | Unbounded row counts that would churn state on every refresh. Two are deprecated. Three *are* implemented |
| Reporting and search | v3 `analysis/*`, `metric/*`, `runs/search`, `jobs/{id}/tests` | Same reasoning. Under discussion — see the tracking issue |
| Orb promotion | v3 `orb/versions/{id}/promote` | Creates a *new* version rather than mutating one, so it has no idempotent Terraform shape. The client method exists and is tested |
| Docker layer cache purge | v3 `DELETE /projects/{id}/dlc` | A one-shot side effect with nothing to read back. Terraform has no primitive for "run this once" |
| Run/workflow cancel, rerun, approve | v2, v3 | Runtime actions, not desired state |
| Orb and namespace import | v3 `*/import` | One-shot migration between installations |

### Cannot be managed by any tool

No API exists. Listed so nobody hunts for them.

VCS connection setup (GitHub App install, OAuth authorize, GitLab/Bitbucket tokens)
· CircleCI account creation · API token creation (`POST /user/token` is session-only
auth, a deliberate privilege boundary) · SSO/SAML configuration · audit log
*retrieval* (streaming *configuration* is managed — see `circleci_audit_log_config`)
· cloud resource classes (config-level, not API-managed).

> Earlier versions of this list included **user invitations**. That was wrong: routes
> to list organization members, invite them with a role, change a role and remove a
> member are all specified. They are **deliberately not implemented here**, because
> they are served on a host reserved for internal use rather than through CircleCI's
> public API. This is the largest remaining capability gap.

The account and VCS steps are browser consent flows by design, which is the structural
reason a CircleCI organization cannot be stood up end to end from Terraform alone.

## Acknowledgments
This repository was created following the Terraform plugin framework defined by Hashicorp [here](https://developer.hashicorp.com/terraform/plugin/framework).

# Development

This repository makes use of [Task](https://taskfile.dev/#/). It may be installed (on MacOS) with:
```
$ brew install go-task/tap/go-task
```

See the full list of available tasks by running `task -l`, or, see the [Taskfile.yml](./Taskfile.yml) script.

```sh
task lint
task fmt
task generate

# Inner loop: the API client packages only. ~510 tests in about 2 seconds.
task test:fast

# Everything. Takes minutes — see below.
task test

# One package
task test -- ./internal/circleci/...
```

### Why `task test` takes minutes

Most tests in `internal/provider` are not unit tests in the usual sense.
`terraform-plugin-testing` launches a **real `terraform` binary** for each test case
and drives `plan`/`apply`/`destroy` through it against an `httptest` fake of the
CircleCI API. Several hundred of those run with no credentials, which is the point —
they exercise the provider the way Terraform actually calls it, including import and
drift behaviour that in-process tests cannot reach.

They are written with `resource.UnitTest`, **not `resource.Test`**. `resource.Test`
skips unless `TF_ACC=1`, so a fake-backed test written that way silently does not run;
`TestCredentialFreeTestsUseUnitTest` fails the build if anyone reintroduces that.

So: `task test:fast` while iterating, `task test` before pushing.

## Acceptance tests

The acceptance tests in `internal/provider` run against a real CircleCI
installation. They only execute when `TF_ACC=1` is set, and most of the
fixtures they touch (organization, project, pipeline, trigger, runner
namespace) have to already exist in the account the token belongs to —
context and webhook are the exceptions, created and destroyed by the test
itself. Those identifiers are read from environment variables — **any test
whose variable is unset, or set to a placeholder (see below), skips instead
of failing**, so a clean checkout with no credentials still passes
`go test ./...`.

Authentication:

| Variable | Description |
| --- | --- |
| `CIRCLE_TOKEN` | CircleCI personal access token. Tests skip without it. |

### Fixture identifiers

`CIRCLECI_TEST_VCS_TYPE` picks which integration is active for this run: one
of `github_app`, `github_oauth`, `github_hybrid`, `gitlab`,
`gitlab_selfmanaged`, `bitbucket`, `github_server`. Every other fixture
variable is named
`CIRCLECI_TEST_<KEY>_<SUFFIX>`, where `KEY` comes from the table below and
`SUFFIX` is the identical set for every key. That uniformity is what lets most
helpers (`testOrgID`, `testProjectID`, ...) resolve the active integration's
fixtures dynamically, with no VCS branching of their own — the same call
reads a GitHub App fixture in one run and a Bitbucket fixture in another.

| `CIRCLECI_TEST_VCS_TYPE` value | Key |
| --- | --- |
| `github_app` | `GH_APP` |
| `github_oauth` | `GH_OAUTH` |
| `github_hybrid` | `GH_HYBRID` |
| `github_server` | `GH_SERVER` |
| `gitlab` | `GL_CLOUD` |
| `gitlab_selfmanaged` | `GL_SM` |
| `bitbucket` | `BB_CLOUD` |

`github_hybrid` is a GitHub OAuth organization that *also* has a GitHub App
installation, and it is a distinct configuration rather than a second
`github_oauth` fixture: `GET
/api/v2/github-app/organization/{id}/installation` answers 200 for such an
organization and 404 for an OAuth-only one. Its projects are still OAuth
projects (`gh/<org>` slugs), so project-level behaviour matches
`github_oauth`; what the key buys is the ability to point the GitHub App
routes at an organization whose projects are not GitHub App projects.

Suffixes, and what each identifies:

| Suffix | Description |
| --- | --- |
| `TOKEN` | Reserved for a future per-integration credential. Not read by any helper today — `CIRCLE_TOKEN` above is still the only token the tests use. |
| `ORG_ID` | UUID of the primary test organization. |
| `ORG_SLUG` | Slug of that organization, e.g. `circleci/<id>` or `gh/<org>`. |
| `ORG_NAME` | Display name of that organization. |
| `ALT_ORG_ID` | UUID of a second organization (project-move tests). |
| `ALT_ORG_SLUG` | Slug of that second organization. |
| `PROJECT_ID` | UUID of a writable project; pipelines, triggers, webhooks and context restrictions are created against it. |
| `PROJECT_SLUG` | Slug of a writable project, used for project environment variables. |
| `STATIC_PROJECT_ID` | UUID of a pre-existing project that is only read. |
| `STATIC_PROJECT_SLUG` | Slug of that project. |
| `STATIC_PROJECT_NAME` | Name of that project. |
| `PIPELINE_ID` | UUID of a pre-existing pipeline in the `PROJECT_ID` project. |
| `TRIGGER_ID` | UUID of a pre-existing trigger. |
| `TRIGGER_PROJECT_ID` | UUID of the project owning that trigger. |
| `SCHEDULED_TRIGGER_ID` | UUID of a pre-existing scheduled trigger in the `STATIC_PROJECT_ID` project. |
| `RUNNER_NAMESPACE` | Runner namespace of the primary organization; resource classes are named `<namespace>/<class>`. |
| `REPO_NAME` | Full name (`owner/repo`) of a repository reachable through the integration. |
| `REPO_EXTERNAL_ID` | External (VCS-side) ID of that repository. |

There is no `CONTEXT_*` or `WEBHOOK_*` suffix: the acceptance tests for
`circleci_context`, `circleci_context_environment_variable` and
`circleci_webhook` create their own scratch context or webhook per run from
`ORG_ID`/`PROJECT_ID` alone, rather than reading a shared, pre-existing one.

For example, with `CIRCLECI_TEST_VCS_TYPE=github_app` the primary
organization's UUID is read from `CIRCLECI_TEST_GH_APP_ORG_ID`; switch to
`bitbucket` and the same helper reads `CIRCLECI_TEST_BB_CLOUD_ORG_ID` instead —
no code change needed.

A few variables are **static** rather than tied to the active integration,
because the test that reads them specifically needs that one integration no
matter what else is configured:

| Variable | Description |
| --- | --- |
| `CIRCLECI_TEST_GH_OAUTH_ORG_ID` | UUID of a GitHub OAuth-backed organization. |
| `CIRCLECI_TEST_GH_OAUTH_ORG_SLUG` | Slug of that organization, e.g. `gh/<org>`. |
| `CIRCLECI_TEST_GH_OAUTH_ADOPTABLE_REPO_NAME` | Name of a repository that **already exists** in that GitHub OAuth organization, and whose CircleCI project the suite may **create and delete**. `circleci_project` cannot create a project on a classic, VCS-backed organization — it only adopts an existing repository, and any other name is answered `404 GitHub response: Not Found` — so `TestAccGithubProjectResource` and `TestAccGithubProjectOrgUpdateResource` skip without this. Point it at a repository nobody minds losing the build history of: those tests destroy the project they create. |
| `CIRCLECI_TEST_GH_APP_REPO_EXTERNAL_ID` | External ID of a repository reachable via the GitHub App integration. |
| `CIRCLECI_TEST_GH_APP_REPO_NAME` | Full name (`owner/repo`) of that repository. |
| `CIRCLECI_TEST_GH_SERVER_PROJECT_ID` | UUID of a GitHub Server backed project. |
| `CIRCLECI_TEST_GH_SERVER_PIPELINE_ID` | UUID of a pipeline in that project. |
| `CIRCLECI_TEST_GH_SERVER_REPO_EXTERNAL_ID` | External ID of the GitHub Server repository. |

### Placeholder values skip cleanly

A variable set to `REPLACE_ME`, `TODO`, or `CHANGEME` (compared
case-insensitively, after trimming whitespace) is treated exactly like an
unset variable: the test skips, and the skip message says the value was a
placeholder rather than simply missing. This lets a maintainer seed every
`CIRCLECI_TEST_*` name into a shared CI context with a dummy value — so the
full list is visible and fillable in one place — without those dummy values
making tests run and fail against a nonsense organization.

CI must define these for the acceptance tests to contribute coverage; without
them (or with only placeholders in place) the suite reports skips rather than
failures.
