---
page_title: "circleci_pipeline_definition Resource - circleci"
subcategory: ""
description: |-
  Manages a CircleCI pipeline definition.
---

# circleci_pipeline_definition (Resource)

Manages a CircleCI pipeline definition. A pipeline definition specifies where to find the pipeline configuration and where to check out code from.

-> **A definition is not a run.** This resource manages the declared pairing of a
config source and a checkout source. Each execution of it is a pipeline *run*, read
with [`circleci_pipeline_run`](../data-sources/pipeline_run) — a different object
with a different identifier. This resource was named `circleci_pipeline` until it
was renamed for exactly that reason; the old name still works, and
[Renaming pipeline resources and data sources](../guides/renaming-pipeline-types)
covers the move.

## Availability

| | |
| --- | --- |
| **CircleCI Cloud** | Yes |
| **CircleCI Server** | No — and **not** for the v3 reason that covers orbs and organization settings. `pipeline-definitions` is a **v2** path, but a Server installation's gateway does not forward it to the backend that owns it. v3 arriving on Server would therefore not make this work. `deployment = "server"` is refused at plan time. Define workflows in `.circleci/config.yml` on Server instead. |
| **API** | `GET` and `POST /api/v2/projects/{project_id}/pipeline-definitions`, `GET`, `PATCH` and `DELETE .../pipeline-definitions/{id}` |
| **Organization type** | Any for **read**. **Create, update and delete require a GitHub App or GitHub Enterprise Server config source**: on a pure GitHub OAuth, GitLab or Bitbucket Cloud organization CircleCI synthesises an *implicit* definition from the project id rather than storing one, so there is nothing to write — and, per the warning below, nothing importable either. A GitHub OAuth organization that *also* has a GitHub App installation is the exception: its projects still take `gh/<org>` slugs, but an explicit `github_app` definition creates, updates and deletes there exactly as it does on a GitHub App organization, alongside the implicit one CircleCI keeps for OAuth. See the compatibility table in the README. |
| **Token** | A personal API token belonging to an organization admin. |

!> **Do not import an OAuth-based project's implicit pipeline definition.** `GET`
answers 200 for it, so `terraform import` appears to succeed — but it can never be
updated (`PATCH` answers 400) or destroyed (`DELETE` answers 500 and the definition
survives). This provider detects it and refuses the import with an explanatory
error; if you hit that error, there is no explicit definition to manage here, and
none is needed — CircleCI runs the project's `.circleci/config.yml` without one.

## Example Usage

```terraform
# A definition is keyed by project UUID, which is not something CircleCI shows you
# — resolve it from the slug rather than pasting a literal.
data "circleci_project" "example" {
  slug = "github/acme/api"
}

# GitHub's own numeric repository id, which is what the github_app config and
# checkout sources are keyed by, not the repository name. The
# circleci_github_app_repository data source reports it; so does
# `gh api repos/acme/api --jq .id`.
data "circleci_github_app_repository" "example" {
  org_id    = data.circleci_project.example.org_id
  full_name = "acme/api"
}

resource "circleci_pipeline_definition" "example" {
  project_id  = data.circleci_project.example.id
  name        = "my-pipeline"
  description = "Main CI/CD pipeline"

  config_source_provider         = "github_app"
  config_source_file_path        = ".circleci/config.yml"
  config_source_repo_external_id = data.circleci_github_app_repository.example.external_id

  # A definition can compile its configuration from one repository while checking
  # out another, so the checkout source is set separately even when it is the same.
  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = data.circleci_github_app_repository.example.external_id
}

# Migrating from the old `circleci_pipeline` type name? Add a moved block. The
# definition is re-addressed in state rather than destroyed and recreated, so
# its ID is preserved and any triggers attached to it stay attached. Delete the
# block once you have applied it.
moved {
  from = circleci_pipeline.example
  to   = circleci_pipeline_definition.example
}
```

### GitHub Enterprise Server configuration

```terraform
# The second accepted config_source_provider / checkout_source_provider value:
# GitHub Enterprise Server, alongside the github_app example above. It behaves
# identically — same required attributes — but its repository ids belong to your
# own GitHub Enterprise Server installation rather than to github.com, so they
# are small integers allocated per installation, not github_app's ids, and the
# two cannot be mixed.
#
# There used to be a third config_source_provider here: "circleci", a
# CircleCI-hosted configuration with no repository at all. It is not an accepted
# value — every create attempt against a customer-plausible file path answers
# HTTP 400 "Invalid config file path." — so there is no working example to show.
data "circleci_project" "github_server_example" {
  slug = "github/acme-internal/payments"
}

locals {
  # The repository id on the GitHub Server installation. These are small
  # integers allocated per installation, not github.com ids.
  github_server_example_repo_external_id = "2259"
}

resource "circleci_pipeline_definition" "github_server_example" {
  project_id  = data.circleci_project.github_server_example.id
  name        = "build"
  description = "Build and test on every push"

  config_source_provider         = "github_server"
  config_source_file_path        = ".circleci/config.yml"
  config_source_repo_external_id = local.github_server_example_repo_external_id

  checkout_source_provider         = "github_server"
  checkout_source_repo_external_id = local.github_server_example_repo_external_id
}
```

## Drift detection

A definition deleted outside Terraform is detected and recreated on the next plan.
That is worth calling out because the API makes it awkward: `GET
.../pipeline-definitions/{id}` returns **400**, not 404, for a definition that no
longer exists — and it returns the *same* 400, with the same response body, for
definitions that do exist on GitLab-backed projects, where that route cannot serve
them at all. The status alone cannot tell the two apart.

The provider therefore confirms the answer against the pipeline-definitions list
route, which reports correctly on every integration, before concluding anything:

| What the provider finds | What it does |
| --- | --- |
| The list carries the definition | Refreshes from it. The resource stays managed — this is the GitLab case. |
| The list does not carry it | Removes it from state; the next plan recreates it. |
| The list cannot be read | Nothing. The resource stays in state and the error names both failures. A transient outage never removes a live resource from state. |

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `checkout_source_provider` (String) The VCS provider for the pipeline's checkout source: `github_app` or `github_server`. checkout_source has no repo-less option at all — the API requires a real repository unconditionally, even for a definition whose configuration is hosted by CircleCI itself — so, unlike config_source_provider, there is nothing here that was ever removed.
- `checkout_source_repo_external_id` (String) The external ID of the repository to check out code from: the VCS provider's own numeric repository id, not its name. Always required — checkout_source has no repo-less provider.
- `config_source_file_path` (String) The path to the pipeline configuration file, relative to the repository named by `config_source_repo_external_id`. Required for every accepted config_source_provider.
- `config_source_provider` (String) Where the pipeline's configuration is read from: `github_app` or `github_server`, both of which read configuration from a VCS repository named by `config_source_repo_external_id`.

~> **`circleci` is not an accepted value here.** It names a CircleCI-internal, repo-less configuration source that this provider has never been able to create: the create endpoint 400s on it for every file path a customer configuration would plausibly use. A configuration written before this restriction was made explicit gets a plan-time error explaining why, rather than a plain "must be one of".

~> **Changing this value forces a new resource to be created.** The update endpoint's `config_source` accepts only `file_path`; provider is immutable after creation.
- `description` (String) A description of the pipeline.
- `name` (String) The name of the pipeline. Changing this value forces a new resource to be created.
- `project_id` (String) The ID of the project this pipeline belongs to. Changing this value forces a new resource to be created.

### Optional

- `config_source_repo_external_id` (String) The external ID of the repository containing the pipeline configuration: the VCS provider's own numeric repository id, not its name. Required for both accepted config_source_provider values.

~> **Changing this value forces a new resource to be created.**

### Read-Only

- `checkout_source_repo_full_name` (String) The full name of the repository used for code checkout.
- `config_source_repo_full_name` (String) The full name of the repository containing the pipeline configuration.
- `created_at` (String) The timestamp when the pipeline was created. Empty for an **implicit** pipeline definition — one CircleCI creates automatically for an OAuth-backed project rather than through this resource's create route — which the API never assigns a creation timestamp to. This resource can only create explicit definitions (always timestamped), but an implicit one can still end up here through `terraform import`, since the singular pipeline-definition route serves it.
- `id` (String) The unique identifier of the pipeline.

## Import

Import is supported using `project_id/pipeline_definition_id`, for an explicit
definition — one created through this resource or the CircleCI UI on a GitHub App
or GitHub Enterprise Server project:

```shell
terraform import circleci_pipeline_definition.example "<project_id>/<pipeline_definition_id>"
```
