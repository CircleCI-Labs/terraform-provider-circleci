---
page_title: "circleci_project_settings Resource - circleci"
subcategory: ""
description: |-
  Manages the advanced settings of an existing CircleCI project.
---

# circleci_project_settings (Resource)

Manages the advanced settings of an existing CircleCI project: the toggles found under Project Settings > Advanced in the CircleCI web application.

## Availability

| | |
| --- | --- |
| **CircleCI Cloud** | Yes |
| **CircleCI Server** | Yes. The route is served by the long-standing v2 API, which a Server installation's gateway forwards `/api` to by default. **Reasoned rather than measured**: no CircleCI Server installation has been available to test against, so this is derived from which routes a Server installation exposes. See the CircleCI Server note on the provider index page. |
| **API** | `GET` and `PATCH /api/v2/project/{project-slug}/settings` |
| **Organization type** | Any. GitLab, GitHub App and GitHub Enterprise Server projects address the slug as `circleci/{org-id}/{project-id}`. Every toggle below except `forks_receive_secret_env_vars` and `oss` was **measured writable in both directions** on four organization classes — a classic GitHub OAuth organization, a standalone organization backed by the GitHub App, a standalone organization backed by GitLab, and a standalone organization with no VCS integration at all. `forks_receive_secret_env_vars` cannot be *enabled* on a standalone organization, and `oss` cannot be written anywhere; see Notes. |
| **Token** | A personal API token with permission to change the project's settings. |

## `circleci_project_settings` or `circleci_project`?

Use **one or the other for a given project, never both.** Both write the same settings record through the same API, so two resources managing one project will fight: each apply reverts what the other last wrote, and the diff never settles.

| | `circleci_project` | `circleci_project_settings` |
| --- | --- | --- |
| Creates and destroys the project | yes | no |
| Manages the project's settings | yes, all of them | yes, only the ones you name |
| Works for a project Terraform did not create | no | yes |
| Settings left out of the configuration | left to CircleCI's default on create | left untouched |

So: manage a project Terraform creates with `circleci_project`, and manage a project that already exists with `circleci_project_settings`.

## Only what you name is written

Every writable setting is optional and none is computed. A setting the configuration does not mention is left out of the request entirely, stays `null` in state, and keeps whatever value CircleCI holds for it. (`oss` is the one exception: it is read-only, so it is computed and simply reports what CircleCI holds.) That has three consequences worth knowing:

- **Nothing is adopted silently.** Reading the project's current settings into state would make "not managed" indistinguishable from "managed as `false`", and the next apply would start writing settings you never asked for.
- **Two configurations may manage disjoint settings** on the same project without fighting, which is what makes this usable alongside a team that manages other settings by hand.
- **Removing a setting from the configuration stops managing it; it does not revert it.** CircleCI has no route that restores a default, so the setting keeps the value Terraform last applied. The provider warns when this happens.

### The defaults a never-set setting has

This resource only ever writes what you name, so what a project *starts* from matters. A setting no one has ever changed is not uniformly `false`:

| Setting | Default when never set |
| --- | --- |
| `auto_cancel_builds` | `false` |
| `build_fork_prs` | `false` |
| `build_prs_only` | `false` |
| `disable_ssh` | `false`, unless an organization-level value says otherwise |
| `forks_receive_secret_env_vars` | **`true`** on a private project, `false` on a public one. Measured `true` on a project read moments after it was created, and on every private fixture project checked. |
| `set_github_status` | **`true`** — measured on a project read moments after it was created, and on four fixture projects across four organization classes |
| `setup_workflows` | **`true`** — measured the same way; CircleCI documents this as applying to projects created after 2023-12-01 |
| `write_settings_requires_admin` | `false`, unless an organization-level value says otherwise |
| `pr_only_branch_overrides` | the repository's default branch, for example `["main"]` |
| `oss` | derived from the repository, and read-only |

## Example Usage

```terraform
# Manage the advanced settings of a project that already exists in CircleCI.
# Only the settings named here are written; everything else is left as it is.
resource "circleci_project_settings" "example" {
  slug = "github/my-org/my-repo"

  auto_cancel_builds            = true
  build_fork_prs                = false
  forks_receive_secret_env_vars = false
  set_github_status             = true
}

# Build only branches with an open pull request, with exceptions.
resource "circleci_project_settings" "prs_only" {
  slug = "github/my-org/my-other-repo"

  build_prs_only           = true
  pr_only_branch_overrides = ["main", "develop"]
}

# Adopt a project's settings without managing any of them yet. Useful as a first
# step: import the resource, then add settings one at a time.
resource "circleci_project_settings" "adopted" {
  slug = "circleci/9a1b2c3d-4e5f-6789-abcd-ef0123456789/my-repo"
}
```

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `slug` (String) The project's slug in the format `vcs-type/org-name/repo-name`. For example, `github/CircleCI-Public/terraform-provider-circleci`. Changing this value forces a new resource to be created.

### Optional

- `auto_cancel_builds` (Boolean) Except for the default branch, cancel any outstanding workflows on a branch when a newer pipeline is triggered on that branch. Scheduled workflows and re-runs are never auto-cancelled.

Leave this unset to let CircleCI manage it; the provider only writes settings that appear in the configuration.
- `build_fork_prs` (Boolean) Run builds for pull requests opened from forks of this repository.

Leave this unset to let CircleCI manage it; the provider only writes settings that appear in the configuration.
- `build_prs_only` (Boolean) Build only branches that have an open pull request associated with them. Use `pr_only_branch_overrides` to list branches that should always build.

Leave this unset to let CircleCI manage it; the provider only writes settings that appear in the configuration.
- `disable_ssh` (Boolean) Disable SSH re-runs for this project, so jobs cannot be re-run with SSH debugging access.

Leave this unset to let CircleCI manage it; the provider only writes settings that appear in the configuration.
- `forks_receive_secret_env_vars` (Boolean) Run forked pull requests with this project's configuration, environment variables and secrets. The build cache is also shared between the original repository and all forks, so enabling this exposes both to anyone who can open a pull request.

~> **Cannot be enabled on a standalone organization.** For a project whose slug begins `circleci/`, setting this to `true` is rejected at plan time, because CircleCI's API answers `403 Permission denied.` for that write — even when the setting is already `true` — and applies every other setting in the same request before refusing. A project's default is `true`, so on those organizations the setting is effectively one-way: `false` is accepted and cannot be undone through the API. Classic organizations (`gh/…`, `bb/…`) accept both values.

Leave this unset to let CircleCI manage it; the provider only writes settings that appear in the configuration.
- `pr_only_branch_overrides` (Set of String) Branches that always trigger a build, even when `build_prs_only` is enabled. The set replaces whatever CircleCI currently holds. Leave it unset to leave the project's existing overrides alone. CircleCI accepts at most 100 branches. Order is not significant: CircleCI does not preserve the order branches are sent in.

~> **Cannot be cleared.** Setting this to `[]` is rejected at plan time. CircleCI's API accepts an empty list with HTTP 200 but silently leaves the existing branches in place, so there is no way to clear the list through this route. Remove the attribute from the configuration instead: that stops managing it and leaves the existing branches as they are.
- `set_github_status` (Boolean) Report the status of every pushed commit to GitHub's status API. Updates are reported per job.

Leave this unset to let CircleCI manage it; the provider only writes settings that appear in the configuration.
- `setup_workflows` (Boolean) Allow a setup workflow to conditionally trigger configuration outside the primary `.circleci` directory, update pipeline parameters before a build runs, and generate customised configuration.

Leave this unset to let CircleCI manage it; the provider only writes settings that appear in the configuration.
- `write_settings_requires_admin` (Boolean) Require organization administrator permissions to change this project's settings. Enabling this can lock the provider itself out of further changes if its token does not belong to an administrator.

Leave this unset to let CircleCI manage it; the provider only writes settings that appear in the configuration.

### Read-Only

- `oss` (Boolean) Whether the project is treated as free and open source, which grants additional credits and makes builds visible to everyone.

~> **Read-only.** This is reported by the API but cannot be set through it. The settings endpoint rejects the field outright — `400 Unexpected field 'advanced.oss'.` — and because it rejects the whole request, including it broke every project create and settings update. CircleCI derives it from whether the repository is public together with an organization-level flag, so set it in the CircleCI web application rather than here.

## Import

Import is supported using the project slug (`vcs-type/org-name/repo-name`):

```shell
terraform import circleci_project_settings.example "github/my-org/my-repo"
```

The import records only the slug and leaves every setting `null`, so the first plan afterwards shows exactly the settings your configuration asks to manage rather than every setting the project happens to have.

## Notes

- **`oss` is read-only.** CircleCI reports it but the settings API does not accept it: a request carrying it answers `400 Unexpected field 'advanced.oss'.` and is rejected in full, so a single unwritable field would fail every write. CircleCI derives the value from whether the repository is public together with an organization-level flag, so set it in the CircleCI web application. This resource reports it and never writes it.
- **`build_fork_prs = true` requires `forks_receive_secret_env_vars` to be set explicitly.** The provider reports an error at validate time otherwise, because the unset default is **`true`** on a private project: fork pull requests would receive the project's environment variables, secrets and build cache, so anyone who can open one could read them.
- **`pr_only_branch_overrides` replaces the whole set, and cannot be cleared.** Leaving it unset leaves the project's existing overrides alone. Setting it to `[]` is rejected at plan time: CircleCI accepts an empty list with `200` and silently keeps the branches already in force, so there is no request that clears the list. Remove the attribute from the configuration instead. CircleCI accepts at most 100 branches — 101 answers `400 Field 'pr_only_branch_overrides' only supports up to 100 branches.` It is a set rather than a list because CircleCI does not preserve the order branches are sent in — as a list it produced a plan that never converged.
- **`forks_receive_secret_env_vars` cannot be enabled on a standalone organization.** For a project whose slug begins `circleci/`, asking for `true` is rejected at plan time, because the API answers `403 Permission denied.` for that write — even when the setting is already `true` — and, unlike the `oss` rejection, applies every other setting in the same request before refusing. Since the default is `true`, the setting is effectively one-way there: `false` is accepted and cannot be undone through the API. Classic organizations (`gh/…`, `bb/…`) accept both values.
- **`write_settings_requires_admin = true` can lock the provider out** of further changes if its token does not belong to an organization administrator.
- **`terraform destroy` writes nothing.** Settings cannot be deleted or reset, so destroying this resource only drops it from state and the project keeps its current values.
