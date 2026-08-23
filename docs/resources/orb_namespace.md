---
page_title: "circleci_orb_namespace Resource - circleci"
subcategory: ""
description: |-
  Manages a CircleCI orb registry namespace.
---

# circleci_orb_namespace (Resource)

Manages a CircleCI orb registry namespace.

A namespace is the globally unique prefix that owns a set of orbs, as in `<namespace>/<orb>`. An organization may own one namespace, and the name is claimed across all of CircleCI rather than only within your organization, so a name another organization already took cannot be created.

A namespace is also what a self-hosted runner resource class is named after: a resource class is always `<namespace>/<class>`. That makes this resource the way to create the namespace a `circleci_runner_resource_class` needs.

!> **Creating a namespace is permanent.** A namespace name is a global, one-per-organization claim with no self-service way to undo it. Read the whole page before applying this resource for the first time: there is no `terraform destroy` that removes a namespace from CircleCI, and no `name` you can change your mind about later.

## Availability

| | |
| --- | --- |
| **CircleCI Cloud** | Yes |
| **CircleCI Server** | No — served by the CircleCI v3 API, which a Server installation does not route to its public API service. Using this with `deployment = "server"` reports an explicit error at plan time rather than the confusing HTTP 404 the request would otherwise produce. |
| **API** | `POST /api/v3/namespaces`, `GET /api/v3/namespaces/{id}` — this resource calls no other route. The v3 API also routes `POST /api/v3/namespaces/{id}/rename` and `DELETE /api/v3/namespaces/{id}`, but both answer `403 Forbidden` unconditionally (see "Renaming" and "Deleting" below), so this provider does not call either. |
| **Organization type** | Any. An organization is normally limited to one namespace. |
| **Token** | A personal API token belonging to an organization admin. |

## Example Usage

```terraform
# A namespace is the globally unique prefix that owns an organization's orbs,
# as in "<namespace>/<orb>". CircleCI Cloud only.
resource "circleci_orb_namespace" "example" {
  name   = "acme"
  org_id = "00000000-0000-0000-0000-000000000000"
}

# The same namespace also names self-hosted runner resource classes, which are
# always "<namespace>/<class>". Create the namespace before the resource class.
resource "circleci_runner_resource_class" "builders" {
  org_id         = "00000000-0000-0000-0000-000000000000"
  resource_class = "${circleci_orb_namespace.example.name}/builders"
  description    = "Self-hosted build runners"
}
```

## Renaming

Changing `name` on a namespace already applied fails at `terraform plan`, before Terraform attempts anything, with a diagnostic explaining why: a namespace name is a permanent, global, one-per-organization claim, and CircleCI's rename route answers `403 Forbidden` unconditionally — measured against a live organization-admin token, including a no-op rename of a namespace to its own existing name. CircleCI's support documentation describes renaming or transferring a namespace as a support-ticket process, not an API call, and this investigation found no account permission that produced a different answer.

This is deliberately not `RequiresReplace`. Replacing this resource means destroying the old namespace before creating the new one, and destroying a namespace cannot actually happen (see "Deleting" below) — so a `RequiresReplace` plan would promise a destroy-then-create that can never finish: the destroy leaves the old namespace in place, and the create that follows then tries to claim a name CircleCI still considers taken. Refusing the rename outright, before either half of that plan runs, is the only shape that cannot leave a namespace half-migrated.

If a namespace really has been renamed by CircleCI support, update `name` in this configuration to match the new name and run `terraform apply` (or `-refresh-only`) — once `name` agrees with what CircleCI reports, the attribute plans as unchanged.

`organization_id` also cannot change in practice: CircleCI has no route that moves a namespace between organizations, so changing it plans a replacement. Given the paragraph above, expect that replacement's create half to fail with "namespace already exists" rather than to succeed — the destroy half leaves the old namespace behind exactly as `terraform destroy` does (see "Deleting" below), so the name is never actually freed for the new organization to claim.

## Deleting

`terraform destroy` on this resource succeeds — it removes the resource from Terraform state — but the namespace itself, and every orb in it, keeps existing in CircleCI. Every destroy raises a warning that names the namespace and its id and says that removing it is not self-service: CircleCI's delete route answers `403 Forbidden` unconditionally, measured against a live organization-admin token, on a namespace the calling organization had just created, on one belonging to an unrelated organization, and even on a namespace whose owning organization had since been deleted. A CircleCI support ticket is the only way to actually remove one, or to free its name for reuse.

This is a deliberate, announced version of `terraform destroy` reporting success on an object that survives it — not an oversight. The alternative this resource used to implement was to call the delete route and surface its `403` as a failed destroy: an honest failure, but one with no path forward, on every namespace ever created, forever, since the API answer never changes. Between a destroy that silently lies about what happened and one that errors forever with nothing a practitioner can do about it, this resource takes a third option: succeed, and say plainly, every time, exactly what is left behind and where to actually act on it.

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `name` (String) The namespace name, unique across all of CircleCI. This is permanent once created: CircleCI's rename route answers 403 Forbidden unconditionally (see the resource description), so changing this value fails at `terraform plan`, with a diagnostic explaining why, rather than being attempted against the API.

### Optional

- `org_id` (String) The unique identifier (UUID) of the organization that owns this namespace.

This is the same field as the deprecated `organization_id`; set exactly one of the two.

Changing this value forces a new resource to be created.
- `organization_id` (String, Deprecated) The unique identifier (UUID) of the organization that owns this namespace.

~> **Deprecated in favour of `org_id`**, which matches CircleCI's own naming. Both work and mean the same thing; set exactly one. Switching from this attribute to `org_id` does not replace the resource.

### Read-Only

- `id` (String) Unique identifier (UUID) of the namespace.

## Import

The import ID is `<organization_id>/<namespace_name>`, or `<organization_id>/<namespace_id>`.

```shell
# The import ID is "<organization_id>/<namespace_name>". The organization is part
# of it because the CircleCI API does not report which organization owns a
# namespace, so it cannot be discovered during the read that follows the import.
terraform import circleci_orb_namespace.example "00000000-0000-0000-0000-000000000000/acme"

# The namespace UUID works in place of the name.
terraform import circleci_orb_namespace.example "00000000-0000-0000-0000-000000000000/11111111-1111-1111-1111-111111111111"
```

The organization id has to be part of the import ID because the CircleCI API's namespace representation does not include the owning organization, so the read that follows the import cannot discover it. Importing with the name alone would leave `organization_id` null, and the next plan would then want to replace the namespace — destroying every orb in it. The provider therefore rejects a bare name with an explicit error.
