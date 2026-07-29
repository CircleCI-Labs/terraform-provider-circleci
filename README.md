# CircleCI Terraform Provider
The CircleCI Terraform Provider enable customers to manage CircleCI projects with IaC patterns, matching the same patterns used to manage GitHub repos. For large-scale organizations, this enables automated project creation for new teams or projects.

## Usage
The current documentation is found [here](https://registry.terraform.io/providers/CircleCI-Public/circleci/latest/docs).
Define the provider:
```hcl
terraform {
  required_providers {
    circleci = {
      source = "CircleCI-Public/circleci"
      version = "~> 0.4"
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
  name 				= "Project_Name"
  organization_id 	= "********-****-****-****-****************"
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
deliberate: it is safer than a wrong `yes`.

### Implemented by this provider

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
| `circleci_pipeline` (read) | yes | yes | yes | yes | yes | yes | **no** [^nopdserver] |
| `circleci_pipeline` (create/update/delete) | yes | **no** [^synthpd] | **no** [^synthpd] | **no** [^synthpd] | **no** [^synthpd] | yes | **no** [^nopdserver] |
| `circleci_trigger` | yes | yes [^oauthtrig] | **no** | **no** | **no** [^bbtrig] | yes | **no** [^nopdserver] |
| `circleci_webhook` | yes | yes | yes | yes | yes | yes | yes |
| `circleci_runner_resource_class`, `circleci_runner_token` | yes | yes | yes | yes | yes | yes | yes [^runnerhost] |
| `circleci_group`, `circleci_group_membership` | yes [^standalone] | **no** [^standalone] | yes | yes | **no** [^standalone] | yes [^standalone] | **no** [^standalone] |
| `circleci_project_group` | yes [^standalone] | **no** | yes | yes | **no** | yes | **no** [^pgroute] |
| `circleci_organization_settings` | yes | yes | yes | yes | yes | yes | **no** [^nov3] |
| `circleci_orb_namespace`, `circleci_orb`, `circleci_orb_version` | yes | yes | yes | yes | yes | yes | **no** [^nov3] |
| `circleci_config_policy_bundle`, `circleci_config_policy_settings` | yes | yes | yes | yes | yes | yes | yes [^policyserver] |
| `circleci_oidc_custom_claims` | yes | yes | yes | yes | yes | yes | yes [^oidcserver] |
| `circleci_url_orb_allow_list_entry` | yes | yes | yes [^glorbauth] | yes [^glorbauth] | yes | yes | ? |
| `circleci_otel_exporter` | yes | yes | yes | yes | yes | yes | ? |

### Supported by CircleCI, not implemented here

[`API-COVERAGE.md`](./API-COVERAGE.md) is the full route-by-route inventory, built
from the routes CircleCI serves rather than the published
OpenAPI spec — the spec both omits routes that exist and describes routes that are
never wired up. Use it to check a specific endpoint; the summary below is the
reasoning.

| Capability | API | Why not |
|---|---|---|
| Legacy scheduled pipelines | v2 `/project/{slug}/schedule` | Superseded. Use `circleci_trigger` with `event_source_provider = "schedule"`; see the migration guide. Also GitHub OAuth and Bitbucket only |
| Additional project SSH keys | v1.1 only | Absent from the Server routes served. Checkout keys are the supported mechanism, and GitLab projects ship a pre-existing key that must not be deleted |
| 7 of 10 insights endpoints | v2 | Unbounded row counts that would churn state on every refresh. Two are deprecated in the v2 API routes served. Three *are* implemented |
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
*retrieval* · cloud resource classes (config-level, not API-managed).

> Earlier versions of this list included **user invitations**. That was wrong: routes
> to list organization members, invite them with a role, change a role and remove a
> member are all specified. They are **deliberately not implemented here**, because
> they are served on a host reserved for internal use rather than through CircleCI's
> public API. This is the largest remaining capability gap — see
> `NEEDS-FROM-MAINTAINER.md`.

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
[^synthpd]: For GitHub OAuth, GitLab and Bitbucket Cloud there is no stored pipeline-definition entity: the v2 API generates a synthetic UUID from the project ID. They can be read but never created or updated.
[^oauthtrig]: Same endpoint, different contract. `event_preset` is required and limited to `all-pushes` or `only-build-prs`; `disabled` is unsupported.
[^bbtrig]: Bitbucket Cloud supports schedule triggers as a feature but has no value in the trigger API's `event_source.provider` enum, so they cannot be created through it.
[^runnerhost]: Set `runner_host` to your Server installation: it serves the runner API itself rather than delegating to `runner.circleci.com`.
[^standalone]: Requires a `circleci` type (standalone) organization. `github` and `bitbucket` organizations cannot use groups even on CircleCI Cloud, and a CircleCI Server installation is always a `github` type organization.
[^pgroute]: The project-group route sits under a different path prefix that a CircleCI Server installation does not expose.
[^policyserver]: CircleCI Server 4.2 and later. Requires the Scale plan on Cloud.
[^oidcserver]: CircleCI Server 4.4 and later. Not available in air-gapped installations.
[^glorbauth]: Entries can be managed, but the `auth` value cannot be GitLab: "GitLab authentication is not currently supported. Only use public GitLab repositories with the `None` auth type."
[^nov3]: A CircleCI Server installation does not route `/api/v3` to the public API service. Set `deployment = "server"` and these resources report an explicit error rather than a confusing HTTP 404.
[^nopdserver]: Unavailable on Server for a *different* reason to `[^nov3]`, worth distinguishing because it changes what would fix it. `pipeline-definitions` and `triggers` are **v2** routes, not v3 — but a Server installation's gateway routes served does not forward them to the public API service at all. v3 arriving on Server would therefore not make these work; the routes served entry is what is missing. Set `deployment = "server"` and these resources fail at plan time with an explicit error.

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

# Inner loop: the API client packages only. ~390 tests in about 2 seconds.
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
