---
page_title: "circleci_project Resource - circleci"
subcategory: ""
description: |-
  Manages a CircleCI project.
---

# circleci_project (Resource)

Manages a CircleCI project and its advanced settings.

Use this for a project Terraform creates. For a project that **already exists** in CircleCI, use [`circleci_project_settings`](project_settings) instead: this resource owns the project's whole settings record, and it can neither be pointed at an existing CircleCI project nor share one with another resource — two resources managing the same project overwrite each other's changes on every apply.

## What an apply actually does

`POST /api/v2/organization/{organization_id}/project` is **two different operations**, and which one runs depends on the organization, not on the configuration. Both behaviours were measured against the live API.

| Organization | What an apply does |
| --- | --- |
| **Standalone** (CircleCI-native; slug `circleci/…`) | **Creates** a new project. Nothing needs to exist beforehand and no repository is involved: the new project comes back with `vcs_info_provider = "CircleCI"` and a `vcs_info_url` of `//circleci.com/<org-uuid>/<project-uuid>`. Confirmed on a standalone organization with no VCS connection at all, as well as on GitHub App and GitLab organizations. |
| **Classic**, VCS-backed (slug `gh/…`, `bb/…`) | **Adopts a repository that already exists.** It never creates one. The project comes back with the real VCS provider (`GitHub`) and repository URL, and CircleCI is then followed through a v1.1 route so the project can run. |

~> **Precondition on a classic organization.** A repository named `name` must **already exist** in that organization and be visible to the token's VCS account. Otherwise the apply fails, and the API's answer says nothing about repositories:

```
Error: Error creating CircleCI project

Could not create CircleCI project, unexpected error: GitHub response: Not Found (HTTP 404)
```

The provider adds the explanation, but the underlying condition can only be fixed on the VCS: create the repository first, then apply. A randomly generated project name can never work on a classic organization.

~> **The other classic-organization failure: adopting a repository that is already a CircleCI project.** A repository can be adopted at most once, so naming one that is already a project — someone else's `circleci_project`, one adopted by hand in the CircleCI web app, or this resource's own state having been lost while the project it created still exists — fails too, and again the API's answer says nothing about why or what to do:

```
Error: Error creating CircleCI project

Could not create CircleCI project, unexpected error: Cannot create project
since a project with the same name already exists in this organization
(HTTP 409)
```

The provider adds a hint here as well, pointing at `terraform import` (see below) rather than at deleting the existing project — deleting it first would also delete its build history, for no reason, when importing it into this configuration keeps it intact.

Destroying works the same way on both: `DELETE /api/v2/project/{project-slug}`, using the slug CircleCI reported. On a standalone organization that slug's segments are opaque identifiers rather than names (`circleci/<org-fragment>/<project-fragment>`), and a slug assembled from the organization and project names is rejected with `400 Invalid project slug` — so use the `slug` attribute, not a name-based guess, when importing. On a classic organization, destroying really does unfollow the repository rather than merely dropping it from state: measured over the network, adopting the same repository again immediately afterward succeeds, where a still-followed repository would answer the 409 above.

## Availability

| | |
| --- | --- |
| **CircleCI Cloud** | Yes |
| **CircleCI Server** | Yes. Projects are a first-class CircleCI Server feature with the same v2 surface. **Reasoned rather than measured**: no CircleCI Server installation has been available to test against, so this is derived from which routes a Server installation exposes. Creating a project on a classic (non-standalone) organization additionally follows a v1.1 route to make the project runnable — the oldest API surface this provider still depends on — and that dependency is untested against Server too. See the CircleCI Server note on the provider index page. |
| **API** | `POST /api/v2/organization/{organization_id}/project`, `GET` and `DELETE /api/v2/project/{project-slug}` |
| **Organization type** | Any, but the operation differs — see "What an apply actually does" above. On a standalone organization this creates a project; on a classic one it adopts an existing repository. |
| **Token** | A personal API token with permission to create projects in the organization. On a classic organization the token's VCS account must also be able to see the repository being adopted. |

## Example Usage

```terraform
data "circleci_organization" "acme" {
  slug = "gh/acme"
}

# Use this resource for a project Terraform creates. For a project that already
# exists in CircleCI, use circleci_project_settings instead: this resource owns the
# project's whole settings record and cannot adopt a project it did not create.
#
# A setting this configuration does not mention is not sent at all, so CircleCI
# applies its own default — and those defaults are not uniformly false. The table
# further down this page lists them.
resource "circleci_project" "api" {
  org_id = data.circleci_organization.acme.id
  name   = "api" # the repository name

  auto_cancel_builds            = true
  disable_ssh                   = true
  set_github_status             = true
  setup_workflows               = false
  write_settings_requires_admin = false
}

# Build only branches with an open pull request, with named exceptions.
# `pr_only_branch_overrides` is a set: CircleCI does not preserve the order branches
# are sent in, so order here is not significant.
resource "circleci_project" "web" {
  org_id = data.circleci_organization.acme.id
  name   = "web"

  build_prs_only           = true
  pr_only_branch_overrides = ["main", "develop"]

  # `forks_receive_secret_env_vars` must be set explicitly whenever
  # `build_fork_prs` is true, and the provider errors at validate time otherwise.
  # There is no safe default to fall back on: CircleCI leaves it at true on a
  # private project, which would hand this project's environment variables, secrets
  # and build cache to a pull request opened from any fork.
  build_fork_prs                = true
  forks_receive_secret_env_vars = false
}

# `oss` is read-only. CircleCI reports it but the settings API rejects the field
# outright, so it is Computed here and has to be set in the web application.
output "circleci_api_project" {
  value = {
    id             = circleci_project.api.id
    slug           = circleci_project.api.slug
    default_branch = circleci_project.api.vcs_info_default_branch
    oss            = circleci_project.api.oss
  }
}
```

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `name` (String) The name of the project. Changing this value forces a new resource to be created.

On a **classic**, VCS-backed organization this must be the name of a repository that **already exists** in that organization: CircleCI adopts the repository, it does not create one. On a **standalone** organization it is simply the name of the new project, and no repository is involved.

### Optional

- `auto_cancel_builds` (Boolean) Whether to automatically cancel redundant builds.
- `build_fork_prs` (Boolean) Whether to build pull requests from forked repositories.
- `build_prs_only` (Boolean) Whether to build only branches that have an open pull request. Use `pr_only_branch_overrides` to list branches that should always build.

~> On GitLab this is not a project setting but a per-trigger filter, so it has no effect there.
- `disable_ssh` (Boolean) Whether to disable SSH access to builds.
- `forks_receive_secret_env_vars` (Boolean) Whether forked pull requests can access secret environment variables.
- `org_id` (String) The unique identifier (UUID) of the organization that owns this project.

This is the same field as the deprecated `organization_id`; set exactly one of the two.

Changing this value forces a new resource to be created.
- `organization_id` (String, Deprecated) The unique identifier (UUID) of the organization that owns this project.

~> **Deprecated in favour of `org_id`**, which matches CircleCI's own naming. Both work and mean the same thing; set exactly one. Switching from this attribute to `org_id` does not replace the resource.
- `pr_only_branch_overrides` (Set of String) Branches that override the PR-only build setting. Order is not significant: CircleCI does not preserve the order branches are sent in.

~> **Cannot be cleared.** Setting this to `[]` is rejected at plan time. CircleCI's API accepts an empty list with HTTP 200 but silently leaves the existing branches in place, so there is no way to clear the list through this route. Remove the attribute from the configuration instead: that stops managing it and leaves the existing branches as they are.
- `set_github_status` (Boolean) Whether to set GitHub commit status on builds.
- `setup_workflows` (Boolean) Whether setup workflows are enabled.
- `write_settings_requires_admin` (Boolean) Whether admin permissions are required to change project settings.

### Read-Only

- `id` (String) The unique identifier of the project.
- `organization_name` (String) The name of the owning organization.
- `organization_slug` (String) The slug of the owning organization.
- `oss` (Boolean) Whether the project is treated as free and open source, which grants additional credits and makes builds visible to everyone.

~> **Read-only.** This is reported by the API but cannot be set through it. The settings endpoint rejects the field outright — `400 Unexpected field 'advanced.oss'.` — and because it rejects the whole request, including it broke every project create and settings update. CircleCI derives it from whether the repository is public together with an organization-level flag, so set it in the CircleCI web application rather than here.
- `slug` (String) The project slug, as CircleCI reports it. On a **classic**, VCS-backed organization that is `vcs-type/org-name/repo-name`, for example `gh/acme/my-repo`. On a **standalone** organization it is `circleci/<org-fragment>/<project-fragment>`, where both segments are opaque identifiers — the second is neither the project name nor its UUID. Use this value verbatim when importing; a slug assembled from names is rejected there.
- `vcs_info_default_branch` (String) The default branch of the project repository.
- `vcs_info_provider` (String) The VCS provider (e.g., `github`, `bitbucket`).
- `vcs_info_url` (String) The VCS URL of the project repository.

## Import

Import is supported using the project slug:

```shell
# Classic, VCS-backed organization
terraform import circleci_project.example "gh/my-org/my-repo"

# Standalone (CircleCI-native) organization: both segments are opaque identifiers
terraform import circleci_project.example "circleci/TFtestOrgFragment01234/TFtestProjFragment012"
```

Use the value of the `slug` attribute, exactly as CircleCI reports it. On a classic organization that reads as `gh/my-org/my-repo`; on a standalone organization it is `circleci/<org-fragment>/<project-fragment>`, where both segments are opaque identifiers and the second is neither the project's name nor its UUID. A slug built from names is rejected there with `400 Invalid project slug`.

The import populates **every attribute the API can supply** — the identifying attributes and all nine settings (the eight boolean toggles and `pr_only_branch_overrides`) — so the imported resource is indistinguishable from one this provider created, and `terraform plan` immediately afterwards is empty. Earlier versions populated only `slug` and left those nine `null`, which made the first plan after an import propose a change for every setting you then wrote down, whether or not it already held that value.

Because the settings attributes are optional, a configuration generated from the import (for example with `terraform plan -generate-config-out`) still mentions none of them, and that plans clean too. Add a setting to the generated configuration once you actually want this resource to manage it.

## Settings you leave out

A setting this configuration does not mention is **not sent at all**, so CircleCI applies its own default. This is worth knowing, because the defaults are not uniformly `false`:

| Setting | Default when never set |
| --- | --- |
| `auto_cancel_builds` | `false` |
| `build_fork_prs` | `false` |
| `build_prs_only` | `false` |
| `disable_ssh` | `false`, unless an organization-level value says otherwise |
| `forks_receive_secret_env_vars` | **`true` on a private project**, `false` on a public one |
| `set_github_status` | **`true`** |
| `setup_workflows` | **`true`** for projects created after 2023-12-01 |
| `write_settings_requires_admin` | `false`, unless an organization-level value says otherwise |
| `pr_only_branch_overrides` | the repository's default branch, for example `["main"]` |
| `oss` | derived from the repository, and read-only — see below |

Earlier versions of this provider sent every setting on create, writing `false` for anything the configuration left out. That forced `set_github_status` off on every Terraform-created project and cleared `pr_only_branch_overrides`, so it was fixed: nothing unmentioned is written, and the value CircleCI chose is read back into state.

~> Because `forks_receive_secret_env_vars` defaults to **`true`** on a private project, a configuration that sets `build_fork_prs = true` must also set `forks_receive_secret_env_vars` explicitly. The provider reports an error at validate time otherwise: leaving it unset on a private project means pull requests from forks receive this project's environment variables, secrets and build cache, so anyone who can open one can read them.

## Notes on `oss`

`oss` is **read-only**. CircleCI reports it but the settings API does not accept it: a request carrying it answers `400 Unexpected field 'advanced.oss'.` and is rejected in full, so a single unwritable field would fail every write. CircleCI derives the value from whether the repository is public together with an organization-level flag — set it in the CircleCI web application, not here.

Use [`circleci_project_settings`](project_settings) to manage the settings of a project Terraform did not create, or to have several configurations manage disjoint settings on one project.
