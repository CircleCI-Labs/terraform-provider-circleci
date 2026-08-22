---
page_title: "circleci_github_app_repositories Data Source - circleci"
subcategory: ""
description: |-
  Lists every repository the CircleCI GitHub App can access for an organization, with the numeric GitHub ID of each.
---

# circleci_github_app_repositories (Data Source)

Lists every repository the CircleCI GitHub App can access for an organization, with the numeric GitHub ID of each.

Use it to drive `for_each` over the repositories CircleCI can see, to build a name-to-external-ID lookup table, or to find out what an installation is scoped to when `circleci_github_app_repository` cannot find a name.

Pagination is followed internally, so the result covers every repository rather than one page.

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

It is used here because there is no published way to resolve a repository name to the numeric external ID that `circleci_pipeline_definition` and `circleci_trigger` require. See `circleci_github_app_repository` for the single-repository lookup and the full rationale.

## No installation is an error, not an empty list

An earlier revision of this page claimed the route answers HTTP 200 with an empty `items` array both when the organization has no GitHub App installation at all and when it has one that has been granted no repositories — reasoning that looked plausible from the handler code alone. [Checked against the live API]: it is wrong for the first case. An organization with no installation at all answers HTTP 404 `"Organization not found."`, and this data source reports that as an explicit error naming the missing installation, not as an empty `repositories` list. `circleci_github_app_installation` is one way to confirm this ahead of time, but is no longer the *only* way to tell the two situations apart — the error here already does.

An empty, *non-erroring* `repositories` list means the installation exists but was granted no repositories. An installation configured for selected repositories reports only what it was granted, so a repository the organization owns but has not granted the app is absent from this list either way, and the only fix is to widen the installation on GitHub.

## Example Usage

```terraform
variable "org_id" {
  type        = string
  description = <<-EOT
    UUID of the CircleCI organization whose GitHub App installation is listed. This
    is CircleCI's own organization id, not a GitHub id — the circleci_organization
    data source reports it for a slug, and it is also in the URL of the
    organization's settings page in the CircleCI web application.
  EOT
}

# List every repository the CircleCI GitHub App can reach for an organization.
data "circleci_github_app_repositories" "all" {
  org_id = var.org_id
}

# Keyed by full name, this becomes a lookup table for external IDs.
locals {
  repo_external_ids = {
    for repo in data.circleci_github_app_repositories.all.repositories :
    repo.full_name => repo.external_id
  }
}

output "api_external_id" {
  value = local.repo_external_ids["acme/api"]
}

# Or drive resources directly off the listing. Private repositories only, here.
locals {
  private_repos = {
    for repo in data.circleci_github_app_repositories.all.repositories :
    repo.full_name => repo if repo.private
  }
}

output "private_repository_names" {
  value = keys(local.private_repos)
}

# Useful when circleci_github_app_repository cannot find a name: this shows what
# the installation is actually scoped to. A repository missing from here has to be
# granted to the app on GitHub.
output "visible_repository_names" {
  value = [for repo in data.circleci_github_app_repositories.all.repositories : repo.full_name]
}
```

<!-- schema generated by tfplugindocs -->
## Schema

### Optional

- `org_id` (String) The unique identifier (UUID) of the organization to read GitHub App repositories from.

This is the same field as the deprecated `organization_id`; set exactly one of the two.
- `organization_id` (String, Deprecated) The unique identifier (UUID) of the organization to read GitHub App repositories from.

~> **Deprecated in favour of `org_id`**, which matches CircleCI's own naming. Both work and mean the same thing; set exactly one.

### Read-Only

- `repositories` (Attributes List) The repositories the GitHub App can access, in the order the API returns them. (see [below for nested schema](#nestedatt--repositories))

<a id="nestedatt--repositories"></a>
### Nested Schema for `repositories`

Read-Only:

- `default_branch` (String) The repository's default branch on GitHub.
- `external_id` (String) The repository's numeric GitHub ID as a string, ready to pass to `circleci_pipeline` or `circleci_trigger`.
- `full_name` (String) Fully-qualified repository name, in `owner/repo` form.
- `id` (Number) The repository's numeric GitHub ID as a number.
- `name` (String) Repository name without its owner.
- `owner` (String) GitHub account that owns the repository.
- `private` (Boolean) Whether the repository is private on GitHub.
