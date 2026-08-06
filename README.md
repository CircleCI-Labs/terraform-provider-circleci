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
      version = "~> 0.5"
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
      version = "~> 0.5"
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
> matrix. `TESTING.md` enumerates the seven integration types and what each one
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
| `circleci_context` | yes | yes | yes | yes | yes | yes | yes [^ctxowner] |
| `circleci_context_environment_variable` | yes | yes | yes | yes | yes | yes | yes |
| `circleci_context_restriction` (`project`, `expression`) | yes | yes | yes | yes | yes | yes | yes |
| `circleci_context_restriction` (`group`) | ? [^grouptype] | ? [^grouptype] | ? [^grouptype] | ? [^grouptype] | **no** [^bbrestrict] | ? [^grouptype] | ? [^grouptype] |
| `circleci_project` | yes | yes | yes | yes | yes | yes | yes [^v11follow] |
| `circleci_project_settings` / `circleci_project` settings | yes | yes | yes [^glslug] | yes [^glslug] | yes | yes [^glslug] | yes |
| ├ `build_fork_prs`, `forks_receive_secret_env_vars` | **no** | yes | ? [^forkprs] | **no** | yes | **no** | yes |
| ├ `oss` | ? | yes | ? | ? | yes | ? | ? |
| ├ `set_github_status` | yes | yes | yes [^vcsstatus] | yes [^vcsstatus] | yes [^vcsstatus] | yes | yes |
| `circleci_project_environment_variable` | yes | yes | yes | yes | yes | yes | yes |
| `circleci_checkout_key` | **no** [^ckey] | yes | **no** [^ckey] | **no** [^ckey] | yes | **no** [^ckey] | yes |
| `circleci_pipeline_definition` (read) | yes | yes | yes | yes | yes | yes | **no** [^nopdserver] |
| `circleci_pipeline_definition` (create/update/delete) | yes | **no** [^synthpd] | **no** [^synthpd] | **no** [^synthpd] | **no** [^synthpd] | yes | **no** [^nopdserver] |
| `circleci_trigger` | yes | yes [^oauthtrig] | **no** | **no** | **no** [^bbtrig] | yes | **no** [^nopdserver] |
| `circleci_webhook` | yes | yes | yes | yes | yes | yes | yes |
| `circleci_runner_resource_class`, `circleci_runner_token` | yes | yes | yes | yes | yes | yes | yes [^runnerhost] |
| `circleci_group` | yes [^standalone] | **no** [^standalone] | yes | yes | **no** [^standalone] | yes [^standalone] | **no** [^standalone] |
| `circleci_project_group` | yes [^standalone] | **no** | yes | yes | **no** | yes | **no** [^pgroute] |
| `circleci_organization_settings` | yes | yes | yes | yes | yes | yes | **no** [^nov3] |
| `circleci_orb_namespace`, `circleci_orb`, `circleci_orb_version` | yes | yes | yes | yes | yes | yes | **no** [^nov3] |
| `circleci_config_policy_bundle`, `circleci_config_policy_settings` | yes | yes | yes | yes | yes | yes | yes [^policyserver] |
| `circleci_oidc_custom_claims` | yes | yes | yes | yes | yes | yes | yes [^oidcserver] |
| `circleci_url_orb_allow_list_entry` | yes | yes | yes [^glorbauth] | yes [^glorbauth] | yes | yes | ? [^weakroute] |
| `circleci_otel_exporter` | yes | yes | yes | yes | yes | yes | ? [^weakroute] |
| `circleci_github_app_installation`, `circleci_github_app_repository`, `circleci_github_app_repositories` (read-only) | yes | **no** [^ghaonly] | **no** [^ghaonly] | **no** [^ghaonly] | **no** [^ghaonly] | ? [^ghesapp] | **no** [^ghaonly] |

### Implemented, and the same on every VCS integration

These types are scoped to an organization, a user, or a pipeline *run*, so no
part of their behaviour depends on which VCS the organization is connected to.
They are listed separately rather than as more rows of seven `yes` cells, so the
matrix above stays a statement about VCS differences.

Read-only entries are data sources; ephemeral entries never enter state at all.

| Terraform type | Cloud | Server | Note |
|---|:--:|:--:|---|
| `circleci_organization` | yes | ? [^orgcreate] | Create is find-or-create for VCS-backed organizations |
| `circleci_pipeline_run`, `circleci_pipeline_run_config`, `circleci_pipeline_run_values`, `circleci_pipeline_run_workflows` (read-only) | yes | yes [^v2api] | A *run*, not a definition. See the object model guide |
| `circleci_workflow`, `circleci_workflow_jobs`, `circleci_job` (read-only) | yes | yes [^v2api] | |
| `circleci_user`, `circleci_user_collaborations` (read-only) | yes | yes [^v2api] | Needs a personal token, not a project token |
| `circleci_runner_task_counts`, `circleci_runners` (read-only) | yes | yes [^runnerhost] | |
| `circleci_usage_export`, `circleci_ephemeral_runner_token` (ephemeral) | yes | yes [^ephemeralserver] | |
| `circleci_insights_summary`, `circleci_insights_workflows`, `circleci_insights_flaky_tests` (read-only) | yes | depends [^insightsserver] | Approximate, recomputed daily, unusable for cost reporting |
| `circleci_notification_channel_config`, `circleci_notification_preferences`, `circleci_notification_integration_status` | yes | **no** [^nov3] | Plus `circleci_notification_integrations` and `circleci_notification_links` (read-only) |
| `circleci_ios_signing_certificate`, `circleci_ios_signing_config` | yes | **no** [^nov3] | Only usable by a macOS executor. Both take write-only credentials [^writeonly] |
| `circleci_deploy_component`, `circleci_deploy_components`, `circleci_deploy_environment`, `circleci_deploy_environments`, `circleci_deploy_settings` (read-only) | yes | **no** [^reltracker] | |
| `circleci_catalog_offerings` (read-only) | yes | **no** [^nov3] | The resource classes an organization may use |
| `circleci_audit_log_config` | yes [^scaleplan] | **no** [^scaleplan] | Plus `circleci_audit_log_access` and `circleci_audit_log_configs` (read-only) |
| `circleci_budget` | yes [^privateroute] | **no** [^privateroute] | An undocumented private route. Plus `circleci_budgets` (read-only) |

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

[^ctxowner]: On CircleCI Server a context owner must be given as `owner.type: "account"` with an id; owner slugs are not supported, and context names must be unique across all organizations in the account.
[^grouptype]: CircleCI has two unrelated concepts called "group". *VCS security groups* are documented as available for `github` type organizations only and require the GitHub OAuth integration. *CircleCI RBAC groups* require a `circleci` type organization. Those requirements are mutually exclusive, and the API does not document which one `restriction_type = "group"` expects. Verify against your organization.
[^bbrestrict]: "Bitbucket repositories do not provide an API that allows CircleCI contexts to be restricted."
[^v11follow]: Project creation issues a `POST /api/v1.1/project/.../follow`, because no v2 route follows a project and an unfollowed project never runs. `circleci-sdk-go` sent this to a hardcoded `https://circleci.com` regardless of the configured host, so it could not work against CircleCI Server; the provider now uses its own client and honours `host`. Only classic (`github`/`bitbucket`) organizations need it — a standalone `circleci/<uuid>` organization follows the project as part of creating it. This is the provider's one remaining v1.1 dependency, and v1.1 is the only API version with an active deprecation initiative.
[^glslug]: GitLab, GitHub App and GHES projects use the `circleci/<orgUUID>/<projectUUID>` slug form. The settings endpoint's `provider` path segment has no `gitlab` value.
[^forkprs]: CircleCI's own documentation contradicts itself here — the VCS overview says GitLab.com supports it, while the pipelines and OSS pages say it is unsupported for GitLab and GitHub App pipelines. Unresolved.
[^vcsstatus]: Misleadingly named. This is really "VCS status updates" and controls Bitbucket and GitLab commit statuses too. It is *not* GitHub Checks, which is GitHub-only.
[^ckey]: "Not available to projects that use GitLab or GitHub App." GitHub App and GHES check out over HTTPS and need no keys. Note a read/write asymmetry: only `POST` carries the restriction, so a `GET` on GitLab succeeds and returns the auto-provisioned key — a resource will read fine and fail only on create. `user-key` additionally requires a *user* API token.
[^synthpd]: For GitHub OAuth, GitLab and Bitbucket Cloud there is no stored pipeline-definition entity: CircleCI generates a synthetic UUID from the project ID. They can be read but never created or updated.
[^oauthtrig]: Same endpoint, different contract. `event_preset` is required and limited to `all-pushes` or `only-build-prs`; `disabled` is unsupported.
[^bbtrig]: Bitbucket Cloud supports schedule triggers as a feature but has no value in the trigger API's `event_source.provider` enum, so they cannot be created through it.
[^runnerhost]: Set `runner_host` to your Server installation: it serves the runner API itself rather than delegating to `runner.circleci.com`.
[^standalone]: Requires a `circleci` type (standalone) organization. `github` and `bitbucket` organizations cannot use groups even on CircleCI Cloud, and a CircleCI Server installation is always a `github` type organization.
[^pgroute]: The project-group route sits under a different path prefix that a CircleCI Server installation does not expose.
[^policyserver]: CircleCI Server 4.2 and later. Requires the Scale plan on Cloud.
[^oidcserver]: CircleCI Server 4.4 and later. Not available in air-gapped installations.
[^glorbauth]: Entries can be managed, but the `auth` value cannot be GitLab: "GitLab authentication is not currently supported. Only use public GitLab repositories with the `None` auth type."
[^nov3]: A CircleCI Server installation does not route `/api/v3` at all. Set `deployment = "server"` and these resources report an explicit error rather than a confusing HTTP 404.
[^nopdserver]: Unavailable on Server for a *different* reason to `[^nov3]`, worth distinguishing because it changes what would fix it. `pipeline-definitions` and `triggers` are **v2** routes, not v3 — but a Server installation's gateway does not forward them at all. v3 arriving on Server would therefore not make these work; the missing piece is the route, not the API version. Set `deployment = "server"` and these resources fail at plan time with an explicit error.
[^weakroute]: Not served by the same part of the API as most v2 routes, and a Server installation forwards only a subset of those paths. The provider does not gate these, so it will attempt the request against a Server installation; whether it succeeds is unknown. Being v2 is not evidence on its own — see `[^nopdserver]`.
[^ghaonly]: These read the organization's GitHub App installation, so an organization that has none has nothing to report: any `github`/`bitbucket` (OAuth) organization, and any standalone organization connected to GitLab rather than GitHub. The provider additionally requires a `circleci` type organization, which is why Server is a definite **no**.
[^ghesapp]: A GitHub Enterprise Server connection is also a GitHub App, but whether this route reports it has not been checked.
[^orgcreate]: *Reading* an organization is a plain v2 route and should work on Server. *Creating* one is a different question: a Server installation's organizations come from its VCS and are always `github` type, so a create there likely has nothing to do. Untested either way. Note that destroy deliberately mirrors create — an adopted organization is released from state, only a genuinely created one is deleted.
[^v2api]: Served by the long-standing v2 API, which a Server installation's gateway forwards `/api` to by default. That is evidence from the route configuration rather than merely "it is v2" — but still reasoned, not measured.
[^ephemeralserver]: Usage export is served on both Cloud and Server. Ephemeral runner tokens follow `runner_host`, which on Server is the installation itself.
[^insightsserver]: The routes are v2, but Insights is a separate service a given Server installation may not run. The provider does not block `deployment = "server"`; without Insights the request fails as not-found.
[^reltracker]: Deploy and release tracking is not available on CircleCI Server, and its routes are absent from a Server installation's gateway entirely. Set `deployment = "server"` and these data sources report an explicit error.
[^scaleplan]: Requires the Scale plan, a CircleCI Cloud billing concept a Server installation has no equivalent of. This is the provider's one Cloud-only gate that is **not** about the v3 API: the routes are v2 and might well be routed on Server, but nobody has been able to check, so `deployment = "server"` is refused out of caution rather than from a confirmed absent route. If that is wrong for some installation, the fix is to drop the gate.
[^writeonly]: Six resources accept their secret as a write-only argument that is never persisted to state: `circleci_context_environment_variable` and `circleci_project_environment_variable` (`value_wo`), `circleci_webhook` (`signing_secret_wo`), `circleci_otel_exporter` (`headers_wo`), `circleci_ios_signing_certificate` (`certificate_blob_wo`, `certificate_password_wo`) and `circleci_ios_signing_config` (`provisioning_profiles_wo`). See the "Managing secrets" guide.
[^privateroute]: Served on a CircleCI origin that carries no published specification, at a fixed address independent of `host` and `deployment`. A Server installation's gateway never routes to it, so `deployment = "server"` reports an explicit error rather than a confusing failure.

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
installation. They only execute when `TF_ACC=1` is set, and every fixture they
touch (organization, project, pipeline, context, trigger, webhook, runner
namespace) has to already exist in the account the token belongs to. Those
identifiers are read from environment variables — **any test whose variables are
unset skips instead of failing**, so a clean checkout with no credentials still
passes `go test ./...`.

Authentication:

| Variable | Description |
| --- | --- |
| `CIRCLE_TOKEN` | CircleCI personal access token. Tests skip without it. |

Fixture identifiers:

| Variable | Description |
| --- | --- |
| `CIRCLECI_TEST_ORG_ID` | UUID of the primary CircleCI-VCS test organization. |
| `CIRCLECI_TEST_ORG_SLUG` | Slug of that organization, e.g. `circleci/<id>`. |
| `CIRCLECI_TEST_ORG_NAME` | Display name of that organization. |
| `CIRCLECI_TEST_ALT_ORG_ID` | UUID of a second CircleCI-VCS organization (project-move tests). |
| `CIRCLECI_TEST_ALT_ORG_SLUG` | Slug of that second organization. |
| `CIRCLECI_TEST_GITHUB_ORG_ID` | UUID of a GitHub-backed organization. |
| `CIRCLECI_TEST_GITHUB_ORG_SLUG` | Slug of that organization, e.g. `gh/<org>`. |
| `CIRCLECI_TEST_PROJECT_ID` | UUID of a writable project; pipelines, triggers, webhooks and context restrictions are created against it. |
| `CIRCLECI_TEST_PROJECT_SLUG` | Slug of a writable project used for project environment variables. |
| `CIRCLECI_TEST_STATIC_PROJECT_ID` | UUID of a pre-existing project that is only read. |
| `CIRCLECI_TEST_STATIC_PROJECT_SLUG` | Slug of that project. |
| `CIRCLECI_TEST_STATIC_PROJECT_NAME` | Name of that project. |
| `CIRCLECI_TEST_PIPELINE_ID` | UUID of a pre-existing pipeline in `CIRCLECI_TEST_PROJECT_ID`. |
| `CIRCLECI_TEST_GITHUB_APP_REPO_EXTERNAL_ID` | External ID of a repository reachable via the GitHub App integration. |
| `CIRCLECI_TEST_GITHUB_APP_REPO_NAME` | Full name (`owner/repo`) of that repository. |
| `CIRCLECI_TEST_GITHUB_SERVER_PROJECT_ID` | UUID of a GitHub Server backed project. |
| `CIRCLECI_TEST_GITHUB_SERVER_PIPELINE_ID` | UUID of a pipeline in that project. |
| `CIRCLECI_TEST_GITHUB_SERVER_REPO_EXTERNAL_ID` | External ID of the GitHub Server repository. |
| `CIRCLECI_TEST_CONTEXT_ID` | UUID of a pre-existing context in the primary organization. |
| `CIRCLECI_TEST_CONTEXT_NAME` | Name of that context. |
| `CIRCLECI_TEST_CONTEXT_ENV_VAR_NAME` | Name of an environment variable that already exists on that context. |
| `CIRCLECI_TEST_TRIGGER_ID` | UUID of a pre-existing `github_app` trigger. |
| `CIRCLECI_TEST_TRIGGER_PROJECT_ID` | UUID of the project owning that trigger. |
| `CIRCLECI_TEST_SCHEDULED_TRIGGER_ID` | UUID of a pre-existing scheduled trigger in `CIRCLECI_TEST_STATIC_PROJECT_ID`. |
| `CIRCLECI_TEST_WEBHOOK_ID` | UUID of a pre-existing webhook scoped to `CIRCLECI_TEST_PROJECT_ID`. |
| `CIRCLECI_TEST_WEBHOOK_NAME` | Name of that webhook. |
| `CIRCLECI_TEST_WEBHOOK_URL` | Receiver URL of that webhook. |
| `CIRCLECI_TEST_RUNNER_NAMESPACE` | Runner namespace of the primary organization; resource classes are named `<namespace>/<class>`. |

CI must define these for the acceptance tests to contribute coverage; without
them the suite reports skips rather than failures.
