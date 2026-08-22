---
page_title: "Getting started"
subcategory: "Guides"
description: |-
  From an empty directory to a CircleCI project that builds on every push, with a context supplying its secrets.
---

# Getting started

This guide goes from an empty directory to a CircleCI project that builds on
every push, with a context supplying its secrets. Each step says *why* it is
needed and which VCS integrations it works on, because that differs more than
you would expect.

Read it in order. The objects have hard dependencies — a trigger cannot exist
without a pipeline definition, which cannot exist without a project — and the
most common way to get stuck is to build them in the wrong order. If you want
the conceptual map first, see
[How CircleCI's objects relate](./object-model).

## What you will end up with

| Step | Terraform type | Why it is needed |
|---|---|---|
| 1 | provider `circleci` | Where to talk to, and as whom |
| 2 | `circleci_organization` (data source) | Everything else is keyed by an organization UUID |
| 3 | `circleci_project` | The thing CircleCI builds |
| 4 | `circleci_github_app_repository` (data source) | Resolves `owner/repo` to the numeric repository id steps 5 and 6 require |
| 5 | `circleci_pipeline_definition` | Where to check out from, where the config lives |
| 6 | `circleci_trigger` | Without one, nothing ever runs |
| 7 | `circleci_context` + `circleci_context_environment_variable` | Secrets, shared across projects |
| 8 | `circleci_context_restriction` | Stops every project in the organization from using those secrets |

## Which integrations each step works on

CircleCI does not offer the same features on every VCS integration, so a
configuration that works for a GitHub App organization may not work for a
Bitbucket one. This is the summary; the
[compatibility matrix in the README](https://github.com/CircleCI-Public/terraform-provider-circleci#compatibility)
is the authoritative, footnoted version.

Columns: **GH App** = GitHub App · **GH OAuth** = classic GitHub OAuth ·
**GitLab** = gitlab.com and self-managed · **BB** = Bitbucket Cloud ·
**GHES** = GitHub Enterprise Server · **Server** = CircleCI Server.

| Step | GH App | GH OAuth | GitLab | BB | GHES | Server |
|---|:--:|:--:|:--:|:--:|:--:|:--:|
| 1 provider configuration | yes | yes | yes | yes | yes | yes |
| 2 `circleci_organization` | yes | yes | yes | yes | yes | yes |
| 3 `circleci_project` | yes | yes | yes | yes | yes | yes |
| 4 `circleci_github_app_repository` | yes | n/a | n/a | n/a | **no** | **no** |
| 5 `circleci_pipeline_definition` (create) | yes | [**no**](https://github.com/CircleCI-Labs/terraform-provider-circleci/blob/HEAD/COMPATIBILITY.md#the-getting-started-guide-skips-the-pipeline-definition-step-where-there-is-nothing-to-create) | [**no**](https://github.com/CircleCI-Labs/terraform-provider-circleci/blob/HEAD/COMPATIBILITY.md#the-getting-started-guide-skips-the-pipeline-definition-step-where-there-is-nothing-to-create) | [**no**](https://github.com/CircleCI-Labs/terraform-provider-circleci/blob/HEAD/COMPATIBILITY.md#the-getting-started-guide-skips-the-pipeline-definition-step-where-there-is-nothing-to-create) | yes | **no** |
| 6 `circleci_trigger` | yes | [yes](https://github.com/CircleCI-Labs/terraform-provider-circleci/blob/HEAD/COMPATIBILITY.md#the-getting-started-guides-github-oauth-trigger-step-uses-a-narrower-contract) | **no** | **no** | yes | **no** |
| 7 context and its environment variables | yes | yes | yes | yes | yes | yes |
| 8 `circleci_context_restriction` (`project`) | yes | yes | yes | yes | yes | yes |

Reading that as a route through the guide:

- **GitHub App** — every step, as written.
- **GitHub Enterprise Server** — every step, with `github_server` in place of
  `github_app`, and the repository's numeric id supplied directly rather than
  resolved in step 4.
- **GitHub OAuth** — steps 1–3, then step 6 against the definition CircleCI
  already derived for the project (see that step), then 7–8. Skip 4 and 5.
- **GitLab and Bitbucket Cloud** — steps 1–3 and 7–8. Pipeline definitions and
  triggers are not available to manage.
- **CircleCI Server** — steps 1–3 and 7–8, plus `runner_host` if you use
  self-hosted runners. Steps 4–6 do not exist there.

## Step 1 — configure the provider

```terraform
terraform {
  required_providers {
    circleci = {
      source  = "CircleCI-Labs/circleci"
      version = "~> 0.6"
    }
  }
}

provider "circleci" {
  # key is deliberately not set here — see below.
}
```

### Which token, and where to get it

The provider authenticates with a **CircleCI personal API token**. Create one in
the CircleCI web application under *User Settings → Personal API Tokens*; see
[Managing API tokens](https://circleci.com/docs/managing-api-tokens/).

Supply it through the environment rather than in configuration, so it is never
committed:

```shell
export CIRCLE_TOKEN="..."
```

Two things to know before you pick a token:

- **It must be a personal token, not a project token.** Project tokens cannot
  reach the organization-scoped routes that contexts, projects and pipeline
  definitions live on.
- **Its owner's permissions are the provider's permissions.** Organization
  settings, groups and project role grants need an organization admin. Creating
  a `user-key` checkout key additionally requires that the token's owner has
  authorized their VCS account with CircleCI.

Token creation itself is not automatable: `POST /user/token` is authenticated by
session rather than by token, which is a deliberate privilege boundary. Neither
is connecting a VCS — installing the GitHub App, authorizing OAuth, adding a
GitLab or Bitbucket token — because those are browser consent flows. Do both by
hand once, then everything below is Terraform's.

### CircleCI Server

On CircleCI Server, tell the provider so. It selects the API version per
resource, and Server does not route v3:

```terraform
provider "circleci" {
  host        = "https://circleci.example.com"
  deployment  = "server"
  runner_host = "https://circleci.example.com"
}
```

`host` is a bare origin. Do not append `/api/v2` — the version is chosen per
request. (A trailing `/api/v2` is stripped for compatibility, with a warning.)

## Step 2 — find the organization

Every resource in this provider is keyed by an organization **UUID**. That UUID
is not something you can guess, so the usual starting point is a lookup by slug:

```terraform
data "circleci_organization" "acme" {
  slug = "circleci/00000000-0000-0000-0000-000000000000"
}
```

Set exactly one of `slug` and `id`.

The slug's shape tells you which integration you are on, and it matters for
several later steps:

| Organization type | Slug | Integrations |
|---|---|---|
| `github` | `gh/acme` | GitHub OAuth |
| `bitbucket` | `bb/acme` | Bitbucket Cloud |
| `circleci` (standalone) | `circleci/<orgUUID>` | GitHub App, GitLab, GHES |

-> A **standalone** organization is one whose slug is `circleci/<uuid>`. The
GitHub App integration only exists for standalone organizations, so if you are
on the GitHub App your organization is standalone and its slug is a UUID, not a
name. A CircleCI Server installation is always a `github` type organization.

If you would rather not look the organization up on every plan, put the UUID in
a variable — it never changes. The data source is the convenient form, not the
required one.

## Step 3 — create or find the project

A CircleCI project is CircleCI's view of one repository.

```terraform
resource "circleci_project" "api" {
  name   = "api"
  org_id = data.circleci_organization.acme.id
}
```

`name` is the repository name. CircleCI resolves it against the organization's
connected VCS, so **the repository must already exist and be visible to the
connection** — Terraform cannot create the repository, and cannot widen the
connection's access. If creation fails with a not-found error, that is almost
always what happened.

Creating the project also *follows* it. An unfollowed project never runs, and
following is the provider's one remaining v1.1 API call; it is only needed for
classic `github` and `bitbucket` organizations, because a standalone
organization follows the project as part of creating it.

-> **`org_id`, or `organization_id`?** Every organization-scoped type in this
provider accepts both. `org_id` is the name going forward; `organization_id` is
deprecated but still works, and switching between them does **not** replace the
resource. Set exactly one — setting both is a plan-time error. Older examples in
these docs still use `organization_id`; they are equivalent.

### If the project already exists

Use the data source instead. It is keyed by slug, not by name and organization:

```terraform
data "circleci_project" "api" {
  slug = "circleci/00000000-0000-0000-0000-000000000000/11111111-1111-1111-1111-111111111111"
}
```

The slug has three segments and its shape depends on the integration —
`gh/acme/api` for GitHub OAuth, `bb/acme/api` for Bitbucket, but
`circleci/<orgUUID>/<projectUUID>` for GitLab, GitHub App and GHES. There is no
`gitlab` or `github_app` segment; they all collapse to `circleci`. The
`project_slug` provider function builds the slug and validates that shape, so a
name passed where a UUID belongs fails at plan time instead of 404ing later
(provider-defined functions need Terraform 1.8 or later):

```terraform
data "circleci_project" "api" {
  slug = provider::circleci::project_slug("gh", "acme", "api")
}
```

To bring an existing project under management rather than only reading it,
`terraform import` it — the resource documentation gives the import ID format.

## Step 4 — resolve the repository's numeric id

The next two steps need the repository's **numeric VCS id**, not its name. In
the pipeline definition and trigger schemas it is called an `external_id`, and
it is a string containing a number, such as `"952038793"`.

That value tends to get copied out of the GitHub UI and pasted in as a magic
number. Resolve it instead:

```terraform
data "circleci_github_app_repository" "api" {
  org_id    = data.circleci_organization.acme.id
  full_name = "acme/api"
}
```

`external_id` is the string form to pass on; `id` is the same number as a
number, if you need to compare it.

~> This data source reads a route CircleCI marks internal and excludes from its
published OpenAPI spec, so it carries no compatibility guarantee. It is used
because there is no published way to resolve a repository name to its numeric
id. See the [data source's own page](../data-sources/github_app_repository) for
the full caveat.

Two limits worth knowing:

- It only covers **GitHub App** installations. There is no equivalent for
  `github_server`, so on GHES supply the numeric id yourself.
- An installation scoped to *selected repositories* only reports the ones it was
  granted. A repository the organization owns but has not granted is invisible
  here. `circleci_github_app_repositories` lists what the installation can
  actually see.

## Step 5 — define a pipeline

A **pipeline definition** is the declared pairing of *where to check code out
from* (the checkout source) and *where to find configuration* (the config
source, including the path to the config file). It is not a build; nothing has
run yet.

```terraform
resource "circleci_pipeline_definition" "build" {
  project_id  = circleci_project.api.id
  name        = "build"
  description = "Build and test the API"

  config_source_provider         = "github_app"
  config_source_file_path        = ".circleci/config.yml"
  config_source_repo_external_id = data.circleci_github_app_repository.api.external_id

  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = data.circleci_github_app_repository.api.external_id
}
```

The two sources are separate arguments because they are allowed to differ: a
config repository holding shared pipeline configuration, and an application
repository being built, is a supported and reasonably common shape. Set both to
the same repository when you do not need that.

`config_source_provider` and `checkout_source_provider` accept `github_app` or
`github_server` — those are the only integrations with a real, stored pipeline
definition. Use `github_server` on GHES.

~> **Three of these arguments force replacement**: `project_id`, `name` and
`config_source_repo_external_id`. CircleCI's update endpoint treats the config
source's provider and repository as immutable and accepts only the file path
within it, so an in-place update of those would be silently dropped — state would
record the new value while the API kept the old one. The remaining arguments
update in place. Replacement destroys the definition and, with it, any triggers
attached to it; Terraform recreates triggers it manages, but plan carefully
before renaming a definition.

## Step 6 — add a trigger so it actually runs

A pipeline definition with no trigger never runs. This is the single most common
"I applied it and nothing happened".

```terraform
resource "circleci_trigger" "on_push" {
  project_id                    = circleci_project.api.id
  pipeline_definition_id        = circleci_pipeline_definition.build.id
  event_source_provider         = "github_app"
  event_source_repo_external_id = data.circleci_github_app_repository.api.external_id
  event_preset                  = "all-pushes"
}
```

~> **`pipeline_definition_id` takes a pipeline *definition* id, not a pipeline
*run* id.** Both are UUIDs, so nothing rejects the wrong one at plan time; you
find out when the trigger never fires. Reference
`circleci_pipeline_definition.<name>.id` and the mistake is impossible. This
argument was called `pipeline_id` in earlier releases — that name still works
and still means a definition id, but it is deprecated precisely because it read
like a run id. See
[Renaming pipeline resources and data sources](./renaming-pipeline-types).

The required arguments differ by `event_source_provider`, because one endpoint
covers several contracts:

| `event_source_provider` | Also required | Not allowed |
|---|---|---|
| `github_app`, `github_server` | `event_source_repo_external_id` | — |
| `github_oauth` | `event_preset`, limited to `all-pushes` or `only-build-prs` | `disabled` |
| `webhook` | `event_name`, `event_source_web_hook_sender`, `checkout_ref`, `config_ref` | `event_preset` |
| `schedule` | `event_name`, `event_source_schedule_cron_expression`, `checkout_ref`, `config_ref` | `event_preset` |

`checkout_ref` and `config_ref` are for `webhook` and `schedule`, where there is
no VCS event to take a ref from. On `github_app` and `github_server` omit them
unless the event source repository differs from the definition's checkout or
config source repository.

For a nightly build, use `event_source_provider = "schedule"` rather than
looking for a `circleci_schedule` resource — there deliberately is not one. See
[Migrating scheduled pipelines](./migrating-scheduled-pipelines).

### On GitHub OAuth, where the definition id comes from

You skipped step 5, so there is no `circleci_pipeline_definition` resource to
reference — but the trigger still needs a definition id. CircleCI derives one
from the project, so read it:

```terraform
data "circleci_pipeline_definitions" "api" {
  project_id = circleci_project.api.id
}

resource "circleci_trigger" "on_push" {
  project_id             = circleci_project.api.id
  pipeline_definition_id = data.circleci_pipeline_definitions.api.pipeline_definitions[0].id

  event_source_provider = "github_oauth"
  event_preset          = "all-pushes"
}
```

`event_preset` is required here, and only `all-pushes` and `only-build-prs` are
accepted. Do not set `disabled` — GitHub OAuth does not support it.

## Step 7 — a context, with an environment variable

A **context** is an organization-scoped bag of environment variables that jobs
opt into with `context:` in `.circleci/config.yml`. It is the right place for
anything shared between projects; `circleci_project_environment_variable` is the
right place for something that belongs to exactly one project.

```terraform
variable "npm_token" {
  type      = string
  sensitive = true
}

resource "circleci_context" "build" {
  name   = "build"
  org_id = data.circleci_organization.acme.id
}

resource "circleci_context_environment_variable" "npm_token" {
  context_id = circleci_context.build.id
  name       = "NPM_TOKEN"
  value      = var.npm_token
}
```

CircleCI never returns an environment variable's value, so Terraform cannot
verify what is stored. `value` is recorded in state in cleartext, which is the
usual objection to managing secrets this way. On Terraform 1.11 or later, use
the write-only form instead — it is sent to CircleCI and then discarded, and
never written to state or to a plan file:

```terraform
resource "circleci_context_environment_variable" "npm_token" {
  context_id       = circleci_context.build.id
  name             = "NPM_TOKEN"
  value_wo         = var.npm_token
  value_wo_version = 1
}
```

Set exactly one of `value` and `value_wo`. Because nothing derived from
`value_wo` is stored, Terraform cannot tell that it changed: increment
`value_wo_version` every time you rotate the secret, or the new value is never
sent.

## Step 8 — restrict the context

A context with no `project` restriction can be used from **every project in
the organization**, including one added tomorrow by someone else, by any
member with access to it. (The context does not start out with an empty
restrictions list: CircleCI adds a permissive default `group` restriction,
"All members", as part of creating it — see the object model guide.)
Restricting it is how a shared secret stops being an organization-wide
secret.

```terraform
resource "circleci_context_restriction" "build_api_only" {
  context_id = circleci_context.build.id
  type       = "project"
  value      = circleci_project.api.id
}
```

`type` is one of `project`, `expression` or `group`. For `project`, `value` is
the project's UUID. Add one restriction resource per project allowed to use the
context.

~> `group` is ambiguous and not fully documented by the API. CircleCI has two
unrelated concepts called "group": VCS security groups, documented as available
for `github` type organizations only, and CircleCI RBAC groups (see
`circleci_group`), which require a standalone organization. Those requirements
are mutually exclusive and the API does not say which one this expects. Verify
against your own organization before relying on it. `group` restrictions are
unavailable on Bitbucket Cloud, which has no API for them; `project` and
`expression` work everywhere.

-> If your organization has `is_context_group_restriction_required` set in
`circleci_organization_settings`, every context must carry at least one group
restriction. A context starts with one (the "All members" default), so this
mainly forecloses removing it entirely — which, per CircleCI's documentation,
otherwise locks the context down to organization administrators only. Get a
deliberate group restriction in place before removing the default.

## Putting it together

```terraform
terraform {
  # 1.11 or later, for the write-only value_wo attribute used below. Drop this
  # and use `value` instead if you are on an earlier version.
  required_version = ">= 1.11"

  required_providers {
    circleci = {
      source  = "CircleCI-Labs/circleci"
      version = "~> 0.6"
    }
  }
}

provider "circleci" {
  # key comes from the CIRCLE_TOKEN environment variable
}

variable "organization_slug" {
  type        = string
  description = "CircleCI organization slug, e.g. circleci/<uuid>, gh/acme or bb/acme."
}

variable "repository" {
  type        = string
  description = "Repository in owner/repo form."
}

variable "npm_token" {
  type      = string
  sensitive = true
}

data "circleci_organization" "this" {
  slug = var.organization_slug
}

data "circleci_github_app_repository" "this" {
  org_id    = data.circleci_organization.this.id
  full_name = var.repository
}

resource "circleci_project" "this" {
  name   = data.circleci_github_app_repository.this.name
  org_id = data.circleci_organization.this.id
}

resource "circleci_pipeline_definition" "build" {
  project_id  = circleci_project.this.id
  name        = "build"
  description = "Build and test on every push"

  config_source_provider         = "github_app"
  config_source_file_path        = ".circleci/config.yml"
  config_source_repo_external_id = data.circleci_github_app_repository.this.external_id

  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = data.circleci_github_app_repository.this.external_id
}

resource "circleci_trigger" "on_push" {
  project_id                    = circleci_project.this.id
  pipeline_definition_id        = circleci_pipeline_definition.build.id
  event_source_provider         = "github_app"
  event_source_repo_external_id = data.circleci_github_app_repository.this.external_id
  event_preset                  = "all-pushes"
}

resource "circleci_context" "build" {
  name   = "build"
  org_id = data.circleci_organization.this.id
}

resource "circleci_context_environment_variable" "npm_token" {
  context_id       = circleci_context.build.id
  name             = "NPM_TOKEN"
  value_wo         = var.npm_token
  value_wo_version = 1
}

resource "circleci_context_restriction" "build_this_project" {
  context_id = circleci_context.build.id
  type       = "project"
  value      = circleci_project.this.id
}
```

Then, in the repository:

```yaml
# .circleci/config.yml
version: 2.1

jobs:
  test:
    docker:
      - image: cimg/node:20.11
    steps:
      - checkout
      - run: npm ci && npm test

workflows:
  build:
    jobs:
      - test:
          context:
            - build
```

Terraform derives its apply order from the references above, which is the
easiest way to get the ordering right: use `circleci_project.this.id` rather
than pasting a UUID, and the dependency graph builds itself.

## Ordering traps

Collected, because every one of these has caught somebody.

1. **A trigger needs a pipeline definition id, not a run id.** `pipeline_definition_id`
   on `circleci_trigger` is a definition. Elsewhere in this provider — on
   `circleci_pipeline_run_config`, `circleci_pipeline_run_values`,
   `circleci_pipeline_run_workflows` — an id named for a run *is* a run. Both are
   UUIDs and neither is validated against the other. See
   [How CircleCI's objects relate](./object-model).
2. **`external_id` is a numeric VCS repository id, carried as a string.** Not a
   repository name, not a CircleCI UUID. `circleci_github_app_repository`
   resolves it from `owner/repo`.
3. **A pipeline definition alone does nothing.** Add a trigger.
4. **The repository must exist first.** `circleci_project` names a repository
   CircleCI's VCS connection must already be able to see. Terraform can neither
   create the repository nor widen the connection.
5. **Renaming a pipeline definition replaces it.** So does changing its
   `project_id` or `config_source_repo_external_id`. Its triggers go with it.
6. **A context with no `project` restriction is available to every project in
   the organization**, including projects created after it. An *empty*
   restrictions list is not that state — it means every `group` grant has
   been removed, which per CircleCI's documentation locks the context down to
   organization administrators only.
7. **Cloud and Server differ in what exists at all.** `circleci_pipeline_definition`
   and `circleci_trigger` are unavailable on CircleCI Server — the
   `pipeline-definitions` and `triggers` routes are not forwarded by a Server
   installation, so steps 5 and 6 do not apply there and Server projects define
   workflows in `.circleci/config.yml` instead. Organization settings, orb
   namespaces and orbs need v3, which Server does not route. Set
   `deployment = "server"` and these types fail at plan time with an explicit
   error rather than a confusing HTTP 404.

## Where to go next

- [How CircleCI's objects relate](./object-model) — the conceptual map, and what
  Terraform manages versus only reads.
- [Self-hosted runners](./self-hosted-runners) — namespace, resource class,
  token, agent.
- [Migrating scheduled pipelines](./migrating-scheduled-pipelines) — nightly
  builds and cron.
- [Migrating from a community CircleCI provider](./migrating-from-community-providers)
  — if you already have `mrolla/circleci` or `kelvintaywl/circleci` state.
