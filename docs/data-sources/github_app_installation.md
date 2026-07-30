---
page_title: "circleci_github_app_installation Data Source - circleci"
subcategory: ""
description: |-
  Reports the CircleCI GitHub App installation for an organization: whether it is installed at all, which GitHub account it is attached to, and how much of that account it can reach.
---

# circleci_github_app_installation (Data Source)

Reports the CircleCI GitHub App installation for an organization: whether it is installed at all, which GitHub account it is attached to, and how much of that account the installation can reach.

This is the precondition for [`circleci_github_app_repository`](github_app_repository) and [`circleci_github_app_repositories`](github_app_repositories). Both of those answer with an empty result whether the app is not installed at all, or is installed but was not granted the repository asked for — and this data source is the only way to tell those two situations apart, because it reports an explicit error when there is no installation rather than an empty list.

## Availability

| | |
| --- | --- |
| **CircleCI Cloud** | Yes |
| **CircleCI Server** | No — the GitHub App integration only exists for `circleci` type (standalone) organizations, and a CircleCI Server installation is always a `github` type organization, so Server can never have an installation to report. Using this data source with `deployment = "server"` reports an explicit error rather than the HTTP 404 the request would otherwise produce. |
| **API** | `GET /api/v2/github-app/organization/{org_id}/installation` |
| **Organization type** | `circleci` (standalone). A GitHub App installation is what makes a standalone organization able to see repositories, so `github` and `bitbucket` organizations have nothing to report here. |
| **Token** | A personal or organization API token with read access to the organization. |

## The API this reads is not published

~> **This data source reads an unpublished CircleCI API, deliberately.** The route is declared in CircleCI's own API definitions only so that request validation accepts it, under a comment marking it *"Internal / CLI-only … intentionally NOT customer-facing: they are excluded from the public docs bundles."* It does not appear in the published OpenAPI spec or in the CircleCI API reference, and it therefore carries **no compatibility guarantee** — CircleCI may change its response shape, its path, or withdraw it entirely, without notice and without a deprecation window.

It sits in the same block of internal routes as the GitHub App *repositories* route that [`circleci_github_app_repository`](github_app_repository) reads, so the same caveat and the same rationale apply: there is no published way to resolve a repository name to the numeric external ID that `circleci_pipeline_definition` and `circleci_trigger` require, and the alternative is practitioners pasting magic numbers into their configurations. Use of these routes was approved by the maintainer on that basis, on the condition that the documentation says so — which is what this section is.

The field names in the schema below were read from CircleCI's production source and confirmed against its handler tests, rather than inferred from the shape of neighbouring routes.

## A missing installation is an error, not an empty result

If the organization has no GitHub App installation, the route answers HTTP 404 and this data source reports an error naming the organization, with instructions to install the app from the organization's VCS integration settings in the CircleCI web application.

That is deliberately different from the repositories data sources, which answer HTTP 200 with an empty `items` array both when nothing is installed and when an installation exists that was granted no repositories. Reading this data source alongside them resolves the ambiguity: if this read succeeds, the app *is* installed, so an empty repository list means "installed, but granted nothing".

## Why there is no matching resource

-> **There is deliberately no `circleci_github_app_installation` resource.** A GitHub App installation is created through a browser consent flow on GitHub's side of the integration, not through this API — CircleCI's own install route only hands back a redirect URL for a human to open, which is not something Terraform can converge on. Install the app in the CircleCI web application, then read it here.

## What `repository_selection` tells you

`all` means the installation can reach every repository the GitHub account owns; `selected` means it is limited to a chosen subset. Either way, `circleci_github_app_repositories` reports only the repositories actually reachable — so on a `selected` installation, a repository the organization owns but has not granted the app is invisible to Terraform, and the fix is to widen the installation on GitHub.

## Example Usage

```terraform
# Whether the CircleCI GitHub App is installed for an organization, and how much
# of the GitHub account it can reach. Reading this fails with an explicit error
# when the app is not installed at all, which is what makes it a usable
# precondition check for the two repository data sources below.
data "circleci_github_app_installation" "acme" {
  # The organization's UUID, as shown on Organization Settings in the CircleCI
  # web application. The GitHub App integration only exists for `circleci` type
  # (standalone) organizations.
  org_id = "00000000-0000-0000-0000-000000000000"
}

# The repositories data source answers with an empty list both when nothing is
# installed and when an installation was granted no repositories, so on its own
# an empty result is ambiguous. Pairing it with the installation above resolves
# that: if the installation read succeeded, the app *is* installed.
data "circleci_github_app_repositories" "acme" {
  org_id = data.circleci_github_app_installation.acme.org_id
}

# `repository_selection` is "all" when the installation can reach every
# repository the GitHub account owns, or "selected" when it was scoped to a
# subset. A repository missing from a "selected" installation is invisible to
# Terraform, and the fix is on GitHub — not in this configuration — so surface it
# as a check rather than letting a later lookup fail with "not found".
check "github_app_grants_repositories" {
  assert {
    condition     = length(data.circleci_github_app_repositories.acme.repositories) > 0
    error_message = "The CircleCI GitHub App is installed on ${data.circleci_github_app_installation.acme.login} (repository_selection = ${data.circleci_github_app_installation.acme.repository_selection}) but has been granted no repositories. Widen the installation on GitHub."
  }
}

# Resolving a repository name to the numeric GitHub ID that
# circleci_pipeline_definition and circleci_trigger require is the reason the
# GitHub App data sources exist at all; the installation is what makes that
# lookup possible.
data "circleci_github_app_repository" "api" {
  org_id    = data.circleci_github_app_installation.acme.org_id
  full_name = "acme/api"
}

output "api_repository_external_id" {
  value = data.circleci_github_app_repository.api.external_id
}

# `target_type` distinguishes an app installed on a GitHub organization from one
# installed on a single user account: a user installation can only ever reach
# that user's own repositories. `id` is the installation's own numeric id, not
# any repository id.
output "github_app_installation" {
  value = {
    installation_id      = data.circleci_github_app_installation.acme.id
    github_account       = data.circleci_github_app_installation.acme.login
    account_kind         = data.circleci_github_app_installation.acme.target_type
    repository_selection = data.circleci_github_app_installation.acme.repository_selection
  }
}
```

<!-- schema generated by tfplugindocs -->
## Schema

### Optional

- `org_id` (String) The unique identifier (UUID) of the organization to read the GitHub App installation from.

This is the same field as the deprecated `organization_id`; set exactly one of the two.
- `organization_id` (String, Deprecated) The unique identifier (UUID) of the organization to read the GitHub App installation from.

~> **Deprecated in favour of `org_id`**, which matches CircleCI's own naming. Both work and mean the same thing; set exactly one.

### Read-Only

- `id` (Number) The GitHub App installation's own numeric id. This identifies the installation itself, not any repository it can reach.
- `login` (String) The GitHub account login (organization or user) the installation belongs to.
- `repository_selection` (String) `all` when the installation can reach every repository the GitHub account owns, or `selected` when it is limited to a chosen subset. Either way, `circleci_github_app_repositories` reports only the repositories actually reachable.
- `target_type` (String) The kind of GitHub account the app is installed on: `Organization` or `User`.
