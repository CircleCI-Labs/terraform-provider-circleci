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
| **Organization type** | Any for **read**. **Create, update and delete require GitHub App or GitHub Enterprise Server**: on GitHub OAuth, GitLab and Bitbucket Cloud CircleCI synthesises a definition from the project id rather than storing one, so there is nothing to write. See the compatibility table in the README. |
| **Token** | A personal API token belonging to an organization admin. |

## Example Usage

`config_source_provider` has a second branch beyond the two VCS-backed ones: a
CircleCI-hosted configuration, with no repository at all. Each is shown separately
below.

### VCS-backed configuration

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

### CircleCI-hosted configuration

```terraform
# A definition whose configuration is hosted by CircleCI itself, rather than read
# from a VCS repository. config_source_repo_external_id must be omitted here — the
# API rejects a repo on this branch outright — but checkout_source still needs a
# real repository: checkout_source has no CircleCI-hosted option, so a definition
# always checks out code from somewhere even when its configuration does not come
# from a repository.
data "circleci_project" "circleci_hosted_example" {
  slug = "github/acme/api"
}

data "circleci_github_app_repository" "circleci_hosted_example" {
  org_id    = data.circleci_project.circleci_hosted_example.org_id
  full_name = "acme/api"
}

resource "circleci_pipeline_definition" "circleci_hosted_example" {
  project_id  = data.circleci_project.circleci_hosted_example.id
  name        = "my-circleci-hosted-pipeline"
  description = "Pipeline whose configuration is hosted by CircleCI"

  config_source_provider  = "circleci"
  config_source_file_path = ".circleci/some-pipeline.yml"

  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = data.circleci_github_app_repository.circleci_hosted_example.external_id
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

- `checkout_source_provider` (String) The VCS provider for the pipeline's checkout source: `github_app` or `github_server`. Unlike config_source_provider, this has no `circleci` (repo-less) option: the API requires a real repository unconditionally, even when the pipeline's configuration is hosted by CircleCI itself — a definition always checks out code from somewhere.
- `checkout_source_repo_external_id` (String) The external ID of the repository to check out code from: the VCS provider's own numeric repository id, not its name. Always required — checkout_source has no repo-less provider.
- `config_source_file_path` (String) The path to the pipeline configuration file. Required for every config_source_provider, including `circleci`, which still requires `file_path` even though it has no repository to be relative to.
- `config_source_provider` (String) Where the pipeline's configuration is read from: `github_app`, `github_server` or `circleci`. `github_app` and `github_server` read configuration from a VCS repository, named by `config_source_repo_external_id`. `circleci` is a CircleCI-hosted configuration: there is no repository, and `config_source_repo_external_id` must be omitted — the API rejects a repo on this branch outright rather than ignoring it.

~> **Changing this value forces a new resource to be created.** The update endpoint's `config_source` accepts only `file_path`; provider is immutable after creation.
- `description` (String) A description of the pipeline.
- `name` (String) The name of the pipeline. Changing this value forces a new resource to be created.
- `project_id` (String) The ID of the project this pipeline belongs to. Changing this value forces a new resource to be created.

### Optional

- `config_source_repo_external_id` (String) The external ID of the repository containing the pipeline configuration: the VCS provider's own numeric repository id, not its name. Required when config_source_provider is `github_app` or `github_server`; must be omitted when it is `circleci`, which has no repository.

~> **Changing this value forces a new resource to be created.**

### Read-Only

- `checkout_source_repo_full_name` (String) The full name of the repository used for code checkout.
- `config_source_repo_full_name` (String) The full name of the repository containing the pipeline configuration. Empty when config_source_provider is `circleci`, which has no repository.
- `created_at` (String) The timestamp when the pipeline was created.
- `id` (String) The unique identifier of the pipeline.

## Import

Import is supported using `project_id/pipeline_definition_id`:

```shell
terraform import circleci_pipeline_definition.example "<project_id>/<pipeline_definition_id>"
```
