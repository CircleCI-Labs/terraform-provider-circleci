---
page_title: "Migrating from a community CircleCI provider"
subcategory: "Guides"
description: |-
  Map resources from the mrolla, kelvintaywl and SectorLabs CircleCI providers onto the official provider.
---

# Migrating from a community CircleCI provider

Several community CircleCI providers predate this one. The most widely used is
`mrolla/circleci`, which has not been released since 2021. This guide maps its
resources — and those of `kelvintaywl/circleci` and `SectorLabs/circleci` — onto
the official provider.

The resource names differ, so migration is not a drop-in `source` swap. Use
[`moved` blocks](https://developer.hashicorp.com/terraform/language/moved) where
a direct equivalent exists, and `terraform import` where it does not.

## Resource mapping

| Community resource | Provider | Official equivalent |
|---|---|---|
| `circleci_context` | mrolla, healx, SectorLabs | `circleci_context` |
| `circleci_context_environment_variable` | mrolla, healx, SectorLabs | `circleci_context_environment_variable` |
| `circleci_environment_variable` | mrolla, healx, SectorLabs, TomTucka | `circleci_project_environment_variable` |
| `circleci_env_var` | kelvintaywl | `circleci_project_environment_variable` |
| `circleci_context_env_var` | kelvintaywl | `circleci_context_environment_variable` |
| `circleci_project` | kelvintaywl, TomTucka | `circleci_project` |
| `circleci_checkout_key` | kelvintaywl, SectorLabs | `circleci_checkout_key` |
| `circleci_webhook` | kelvintaywl | `circleci_webhook` |
| `circleci_runner_resource_class` | kelvintaywl | `circleci_runner_resource_class` |
| `circleci_runner_token` | kelvintaywl | `circleci_runner_token` |
| `circleci_schedule` | kelvintaywl, healx | `circleci_trigger` — see below |

| Community data source | Official equivalent |
|---|---|
| `circleci_context` | `circleci_context` |
| `circleci_project` | `circleci_project` |
| `circleci_checkout_keys` | `circleci_checkout_keys` |
| `circleci_webhooks` | `circleci_webhooks` |
| `circleci_runner_resource_classes` | `circleci_runner_resource_classes` |
| `circleci_runner_tokens` | `circleci_runner_tokens` |

## Organizations are identified by ID, not slug

The most significant difference. Community providers generally take an
organization as a VCS slug such as `github/acme`. The official provider takes an
organization UUID in `organization_id`.

Look one up with the `circleci_organization` data source, or from the CircleCI
web application URL.

```terraform
data "circleci_organization" "acme" {
  id = "00000000-0000-0000-0000-000000000000"
}

resource "circleci_context" "build" {
  organization_id = data.circleci_organization.acme.id
  name            = "build"
}
```

## Renaming a resource with `moved`

Where the official resource is equivalent, rename in place rather than
destroying and recreating. This is critical for contexts and environment
variables, since recreating them would briefly break running pipelines.

```terraform
moved {
  from = circleci_environment_variable.api_token
  to   = circleci_project_environment_variable.api_token
}
```

`moved` only rewrites addresses within one provider. Because the underlying
provider is changing, the reliable sequence is:

1. Remove the community resource from state without destroying the real object:
   `terraform state rm circleci_environment_variable.api_token`
2. Rewrite the configuration to use the official resource type.
3. Import the existing object:
   `terraform import circleci_project_environment_variable.api_token "gh/acme/repo/API_TOKEN"`
4. Run `terraform plan` and confirm it is empty.

Each resource's own documentation gives its import ID format.

## Values that cannot be read back

Environment variable values, webhook signing secrets and runner tokens are never
returned by the API. After importing, Terraform cannot verify the stored value,
so the first `plan` may show a change for the value attribute even when nothing
differs. Supply the same value in configuration to converge.

Runner tokens are returned only once, at creation. An imported
`circleci_runner_token` cannot recover its `token` attribute — if you need the
value, create a new token instead of importing.

## Scheduled pipelines

`circleci_schedule` in the community providers wraps the legacy
`/api/v2/project/{slug}/schedule` endpoint. The official provider does not
implement it, because scheduling is now expressed as a trigger. See
[Migrating scheduled pipelines](./migrating-scheduled-pipelines).

## Why migrate

The community providers target v1.1 and v2 of the CircleCI API. v1.1 has an
active deprecation initiative and customer notices have already been sent, so
resources built on it will stop working. This provider is maintained by CircleCI
and tracks current API versions, including v3 on CircleCI Cloud.
