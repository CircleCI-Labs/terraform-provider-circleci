---
page_title: "circleci_github_app_repository Data Source - circleci"
subcategory: ""
description: |-
  Resolves a repository reachable through the CircleCI GitHub App to its numeric GitHub ID.
---

# circleci_github_app_repository (Data Source)

Resolves a repository reachable through the CircleCI GitHub App to its numeric GitHub ID.

`circleci_pipeline_definition` and `circleci_trigger` both require that ID — as `config_source_repo_external_id`, `checkout_source_repo_external_id` and `event_source_repo_external_id`. Until now it was not obtainable from Terraform at all, so it had to be copied out of the GitHub UI and pasted into the configuration as a magic number. Use `external_id` from this data source instead.

## Availability

| | |
| --- | --- |
| **CircleCI Cloud** | Yes |
| **CircleCI Server** | No — the GitHub App integration only exists for `circleci` type (standalone) organizations, and a CircleCI Server installation is always a `github` type organization. Using this data source with `deployment = "server"` reports an explicit error rather than the HTTP 404 the request would otherwise produce. |
| **API** | `GET /api/v2/github-app/organization/{org_id}/repositories` |
| **Organization type** | `circleci` (standalone). A GitHub App installation is what makes a standalone organization able to see repositories, so `github` and `bitbucket` organizations have nothing to report here. |
| **Token** | A personal or organization API token with read access to the organization. |

## The API this reads is not published

~> **This data source reads an unpublished CircleCI API, deliberately.** The route is declared in CircleCI's own API definitions only so that request validation accepts it, under a comment marking it *"Internal / CLI-only. Defined here so the validation middleware accepts them, but intentionally NOT customer-facing: they are excluded from the public docs bundles."* It does not appear in the published OpenAPI spec or in the CircleCI API reference, and it therefore carries **no compatibility guarantee** — CircleCI may change its response shape, its path, or withdraw it entirely, without notice and without a deprecation window.

It is used here because the alternative is worse. There is no published way to resolve a repository name to the numeric external ID that pipelines and triggers require, so the choice is between reading this route and asking every practitioner to hard-code an opaque integer. If CircleCI publishes an equivalent route, this data source will move to it.

The field names in the schema below were read from CircleCI's production source rather than inferred, because the API's spelling is not the obvious one: the fully-qualified name arrives as `repo_full_name`, not `full_name`, and the short name as `repo_name`.

## Why there is no matching resource

A GitHub App installation, and the set of repositories it can reach, live on GitHub's side of the integration. CircleCI's API can report them but cannot create, change or remove them — its own install endpoint only returns a URL for a human to open on GitHub. There is nothing for Terraform to converge on, so this is a data source and only a data source.

## Repositories that are missing

An installation configured for **selected repositories** only reports the repositories it was granted. A repository the organization owns, but has not granted the app, is invisible here and this data source will not find it. Use `circleci_github_app_repositories` to see what the installation is actually scoped to, then widen the installation on GitHub if something is missing.

## Example Usage

```terraform
# Resolve a repository name to the numeric GitHub ID that
# circleci_pipeline_definition and circleci_trigger require. Without this, the ID
# has to be copied out of the GitHub UI by hand and pasted into the configuration
# as a magic number.
data "circleci_github_app_repository" "api" {
  organization_id = var.organization_id
  full_name       = "acme/api"
}

resource "circleci_pipeline_definition" "api" {
  project_id  = var.project_id
  name        = "build"
  description = "Build and test the API"

  config_source_provider         = "github_app"
  config_source_file_path        = ".circleci/config.yml"
  config_source_repo_external_id = data.circleci_github_app_repository.api.external_id

  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = data.circleci_github_app_repository.api.external_id
}

resource "circleci_trigger" "api_push" {
  project_id             = var.project_id
  pipeline_definition_id = circleci_pipeline_definition.api.id
  name                   = "push"
  description            = "Run on every push"

  event_source_provider         = "github_app"
  event_source_repo_external_id = data.circleci_github_app_repository.api.external_id
  event_preset                  = "all-pushes"
}

# The default branch is reported too, so a configuration does not have to repeat
# what GitHub already knows.
output "api_default_branch" {
  value = data.circleci_github_app_repository.api.default_branch
}
```

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `full_name` (String) Fully-qualified repository name, in `owner/repo` form. The comparison is case-insensitive, matching how GitHub treats owner and repository names.

### Optional

- `org_id` (String) The unique identifier (UUID) of the organization to read GitHub App repositories from.

This is the same field as the deprecated `organization_id`; set exactly one of the two.
- `organization_id` (String, Deprecated) The unique identifier (UUID) of the organization to read GitHub App repositories from.

~> **Deprecated in favour of `org_id`**, which matches CircleCI's own naming. Both work and mean the same thing; set exactly one.

### Read-Only

- `default_branch` (String) The repository's default branch on GitHub.
- `external_id` (String) The repository's numeric GitHub ID as a string. This is the value to pass to `circleci_pipeline` and `circleci_trigger`, whose external ID arguments are strings.
- `id` (Number) The repository's numeric GitHub ID as a number, for arithmetic or comparison. `external_id` carries the same value in the string form the pipeline and trigger resources accept.
- `name` (String) Repository name without its owner.
- `owner` (String) GitHub account that owns the repository.
- `private` (Boolean) Whether the repository is private on GitHub.
