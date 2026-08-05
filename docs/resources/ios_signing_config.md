---
page_title: "circleci_ios_signing_config Resource - circleci"
subcategory: ""
description: |-
  Pairs an iOS signing certificate with provisioning profiles.
---

# circleci_ios_signing_config (Resource)

Pairs a [`circleci_ios_signing_certificate`](./ios_signing_certificate) with one or more Apple provisioning profiles, for signing iOS builds.

## Availability

| | |
| --- | --- |
| **CircleCI Cloud** | Yes |
| **CircleCI Server** | No — served by the CircleCI v3 API, which a Server installation does not route to its public API service. Using this with `deployment = "server"` reports an explicit error rather than the confusing HTTP 404 the request would otherwise produce. |
| **API** | `POST /api/v3/signing/configs` and `DELETE /api/v3/signing/configs/{id}`, read back by listing `GET /api/v3/signing/configs` |
| **Organization type** | Any. The configuration is only usable by a **macOS executor**. |
| **Token** | A personal API token belonging to an organization admin. |

## Example Usage

```terraform
# A .mobileprovision file is less sensitive than the certificate's private key --
# it authorizes rather than signs -- but it identifies devices and app identifiers
# and is not meant to be public. Source it from a secret manager, like the
# certificate it is paired with.

# The certificate has its own write-only path; see
# circleci_ios_signing_certificate's example for what these two variables are.
variable "distribution_certificate_p12_base64" {
  description = "Base64-encoded .p12 distribution certificate."
  type        = string
  sensitive   = true
  ephemeral   = true
}

variable "distribution_certificate_password" {
  description = "Password protecting the .p12 above."
  type        = string
  sensitive   = true
  ephemeral   = true
}

resource "circleci_ios_signing_certificate" "distribution" {
  org_id                  = "00000000-0000-0000-0000-000000000000"
  file_name               = "distribution.p12"
  certificate_blob_wo     = var.distribution_certificate_p12_base64
  certificate_password_wo = var.distribution_certificate_password
  certificate_wo_version  = 1
}

# --- the write-only path, which is the one to prefer -------------------------
#
# provisioning_profiles_wo is a write-only argument (Terraform 1.11 or later): the
# provider sends the list to CircleCI and then discards it, so neither the blobs
# nor the file names land in Terraform state or the plan file.
#
# Declaring the variables `ephemeral` means Terraform will not persist them either,
# which closes the last gap. In a real configuration these would usually come from
# a secret manager's ephemeral resource instead of variables -- for example
# `ephemeral.vault_kv_secret_v2.ios_signing.data["release_mobileprovision"]`. An
# ephemeral value may only be referenced from a write-only argument, so this is the
# only shape that keeps the profile out of state end to end.
variable "release_provisioning_profile_base64" {
  description = "Base64-encoded .mobileprovision file, read from a secret manager."
  type        = string
  sensitive   = true
  ephemeral   = true
}

variable "adhoc_provisioning_profile_base64" {
  description = "Base64-encoded .mobileprovision file for ad-hoc distribution."
  type        = string
  sensitive   = true
  ephemeral   = true
}

# Because nothing derived from the list is stored, Terraform cannot tell that it
# changed. provisioning_profiles_wo_version is the rotation counter: increment it
# whenever anything in the list changes, or the new profiles are never sent. One
# counter covers the whole list, because the list is one rotatable unit -- adding,
# removing, renewing or reordering a profile already replaces the resource rather
# than updating an entry in place.
#
# Incrementing it forces a new resource with a new id. That is not a choice: the
# API has no update route for a signing configuration.
#
# Note that file_name is write-only here too. Every child of a write-only nested
# attribute must itself be write-only, so the name goes with the blob. Nothing is
# lost by that: CircleCI never reports a profile's content back, and the
# circleci_ios_signing_configs data source reads profile names from CircleCI rather
# than from Terraform state.
resource "circleci_ios_signing_config" "release" {
  org_id         = "00000000-0000-0000-0000-000000000000"
  name           = "release-signing"
  certificate_id = circleci_ios_signing_certificate.distribution.id

  provisioning_profiles_wo = [
    {
      file_name = "release.mobileprovision"
      blob      = var.release_provisioning_profile_base64
    },
    {
      file_name = "adhoc.mobileprovision"
      blob      = var.adhoc_provisioning_profile_base64
    },
  ]
  provisioning_profiles_wo_version = 1 # bump to 2 to send a changed list
}

# --- the state-backed path, still supported ----------------------------------
#
# provisioning_profiles is the original spelling. It works identically from
# CircleCI's point of view, and every blob in it is recorded in Terraform state in
# cleartext. Use it only where write-only arguments are not available (Terraform
# below 1.11), and encrypt state at rest either way.
#
# Set exactly one of provisioning_profiles and provisioning_profiles_wo.
variable "internal_provisioning_profile_base64" {
  description = "Base64-encoded .mobileprovision file for internal builds."
  type        = string
  sensitive   = true
}

resource "circleci_ios_signing_config" "internal" {
  org_id         = "00000000-0000-0000-0000-000000000000"
  name           = "internal-signing"
  certificate_id = circleci_ios_signing_certificate.distribution.id

  provisioning_profiles = [
    {
      file_name = "internal.mobileprovision"
      blob      = var.internal_provisioning_profile_base64
    },
  ]
}

# certificate_file_name and certificate_type are filled in from the paired
# certificate, on both paths. A profile's content is never reported back on either,
# so there is nothing to read here beyond what the certificate contributes.
output "release_config_certificate_type" {
  value = circleci_ios_signing_config.release.certificate_type
}
```

### Keeping the provisioning profiles out of state

`provisioning_profiles_wo` is a write-only alternative to `provisioning_profiles`, available with Terraform 1.11 or later. Set exactly one of the two.

Because a write-only argument is never persisted, it is the only kind of argument an `ephemeral` value may be assigned to, which is what lets the profile come straight from a secret manager without passing through a `.tfvars` file:

```terraform
ephemeral "vault_kv_secret_v2" "ios_signing" {
  mount = "secret"
  name  = "ios/signing"
}

resource "circleci_ios_signing_config" "release" {
  org_id         = "00000000-0000-0000-0000-000000000000"
  name           = "release-signing"
  certificate_id = circleci_ios_signing_certificate.distribution.id

  provisioning_profiles_wo = [
    {
      file_name = "release.mobileprovision"
      blob      = ephemeral.vault_kv_secret_v2.ios_signing.data["release_mobileprovision"]
    },
  ]
  provisioning_profiles_wo_version = 1
}
```

The trade between the two is a real one, in both directions:

* `provisioning_profiles` records every `blob` in Terraform state in cleartext, and changing a profile is just editing the list — Terraform sees the change and replaces the resource.
* `provisioning_profiles_wo` is sent to CircleCI and then discarded: nothing about it appears in state or the plan file. But because nothing derived from it is stored, Terraform cannot see that it changed, so **`provisioning_profiles_wo_version` must be incremented every time anything in the list changes**. Editing `provisioning_profiles_wo` on its own produces no diff and is never sent.

**One counter covers the whole list**, because the list is one rotatable unit rather than a collection of independently rotatable ones: adding, removing, renewing or reordering a profile already replaces the resource (see "Immutability" below), so there is nothing a per-entry counter could express.

#### `file_name` is write-only on that path too

This is a constraint of the plugin framework rather than a design choice: every child attribute of a write-only nested attribute must itself be write-only, so `file_name` leaves state along with `blob`.

Nothing depends on it being there:

* CircleCI never returns a profile's content, and this resource's `Read` deliberately does not fold the API's list of profile *names* back into state either — on the `provisioning_profiles` path that would contradict the configured, non-null `blob` beside each name.
* Importing has never recovered either field, on either path.
* `certificate_file_name` and `certificate_type` come from the paired certificate, not from a profile.
* The [`circleci_ios_signing_configs`](../data-sources/ios_signing_configs) data source does report profile names, but it reads them from CircleCI rather than from this resource's state, so it is unaffected by which spelling created the configuration.

See [Managing secrets](../guides/managing-secrets) for the whole picture.

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `certificate_id` (String) Unique identifier (UUID) of the `circleci_ios_signing_certificate` this configuration signs with. Changing this value forces a new resource to be created.
- `name` (String) The configuration's name. May only contain letters, numbers and hyphens, and is limited to 50 characters by the API. Changing this value forces a new resource to be created.

### Optional

> **NOTE**: [Write-only arguments](https://developer.hashicorp.com/terraform/language/resources/ephemeral#write-only-arguments) are supported in Terraform 1.11 and later.

- `org_id` (String) The unique identifier (UUID) of the organization that owns this signing configuration.

This is the same field as the deprecated `organization_id`; set exactly one of the two.

Changing this value forces a new resource to be created.
- `organization_id` (String, Deprecated) The unique identifier (UUID) of the organization that owns this signing configuration.

~> **Deprecated in favour of `org_id`**, which matches CircleCI's own naming. Both work and mean the same thing; set exactly one. Switching from this attribute to `org_id` does not replace the resource.
- `provisioning_profiles` (Attributes List) The provisioning profiles paired with the certificate: between 1 and 100 of them. Changing this list in any way -- adding, removing or reordering a profile -- forces a new resource to be created, since there is no update route.

Each `blob` is recorded in Terraform state in cleartext. Use `provisioning_profiles_wo` instead to keep the list out of state, at the cost of having to bump `provisioning_profiles_wo_version` to change it. Set exactly one of the two.

~> CircleCI parses every profile and checks it against the certificate. A profile that does not authorize `certificate_id`'s certificate is rejected, as are two profiles sharing a bundle identifier and profile type — the list is a set keyed on parsed contents, not on `file_name`. See the resource documentation for which certificate types accept profiles at all. (see [below for nested schema](#nestedatt--provisioning_profiles))
- `provisioning_profiles_wo` (Attributes List, [Write-only](https://developer.hashicorp.com/terraform/language/resources/ephemeral#write-only-arguments)) The provisioning profiles paired with the certificate — the same list as `provisioning_profiles`, with the same bounds — as a write-only argument: Terraform sends them to CircleCI but never records them in state or in a plan file. Requires Terraform 1.11 or later.

Because nothing derived from the list is stored, Terraform cannot see that it changed. `provisioning_profiles_wo_version` is required alongside it, and must be incremented every time any profile changes, or the new profiles are never sent — incrementing it forces a new resource to be created, exactly as editing `provisioning_profiles` does, since there is no update route.

Set exactly one of `provisioning_profiles` and `provisioning_profiles_wo`.

~> **`file_name` is write-only here too, and not by choice.** Every child of a write-only nested attribute must itself be write-only, so `file_name` leaves state on this path along with `blob`. Nothing in this provider depends on it being there: the API never reports profile content back, this resource's `Read` and import both leave the list alone, and the `circleci_ios_signing_configs` data source reads profile names from CircleCI rather than from state. (see [below for nested schema](#nestedatt--provisioning_profiles_wo))
- `provisioning_profiles_wo_version` (Number) Rotation counter for `provisioning_profiles_wo`. Increment it whenever anything in that list changes: a write-only value leaves no trace in state, so this is the only thing Terraform has to compare, and editing `provisioning_profiles_wo` on its own is not a change as far as Terraform is concerned.

One counter covers the whole list, because the whole list is one unit: adding, removing, renewing or reordering a profile already replaces the resource rather than updating an entry.

Required when `provisioning_profiles_wo` is set, and must be at least 1. Incrementing it forces a new resource to be created, with a new `id`.

### Read-Only

- `certificate_file_name` (String) The paired certificate's display name, as CircleCI reports it back on this configuration.
- `certificate_type` (String) The paired certificate's type, as CircleCI reports it back on this configuration: one of `distribution`, `development`, `developer-id-application`, `developer-id-installer`, `mac-development`, `mac-app-distribution` or `mac-installer-distribution`. See `circleci_ios_signing_certificate`'s `cert_type`, which is the same value.
- `id` (String) Unique identifier (UUID) of the signing configuration.

<a id="nestedatt--provisioning_profiles"></a>
### Nested Schema for `provisioning_profiles`

Required:

- `blob` (String, Sensitive) The profile's `.mobileprovision` file, base64-encoded (standard encoding), for example `filebase64("release.mobileprovision")`. Write-only: CircleCI never returns this value -- see the resource-level "Security" section.
- `file_name` (String) A display name for the profile, for example `release.mobileprovision`. Limited to 40 characters by the API.


<a id="nestedatt--provisioning_profiles_wo"></a>
### Nested Schema for `provisioning_profiles_wo`

Required:

- `blob` (String, Sensitive, [Write-only](https://developer.hashicorp.com/terraform/language/resources/ephemeral#write-only-arguments)) The profile's `.mobileprovision` file, base64-encoded (standard encoding), for example `filebase64("release.mobileprovision")`.
- `file_name` (String, [Write-only](https://developer.hashicorp.com/terraform/language/resources/ephemeral#write-only-arguments)) A display name for the profile, for example `release.mobileprovision`. Limited to 40 characters by the API.

## What CircleCI validates

A signing configuration is not stored as written. CircleCI parses every provisioning profile — a `.mobileprovision` file is a CMS-wrapped plist, and it reads the plist — and cross-checks each one against the certificate. Four rules follow, and each is a refusal at apply rather than something this provider can see at plan time:

| Rule | How it fails |
| --- | --- |
| `name` must be unique within the organization | Conflict; the existing configuration is not replaced |
| each profile must authorize `certificate_id`'s certificate (its fingerprint must appear in the profile's `DeveloperCertificates`) | Bad request, `invalid provisioning profile` |
| no two profiles may share a bundle identifier and profile type | Conflict; the list is a set keyed on parsed contents, not on `file_name` |
| the profile count must match the certificate's type — see below | Bad request, in either direction |

That last rule is the one that decides whether this resource is usable at all. CircleCI derives it from the referenced certificate's `cert_type`, which is derived from the certificate itself, and it has two sides:

* For a `distribution`, `development`, `mac-development` or `mac-app-distribution` certificate, **at least one** provisioning profile is required.
* For a `developer-id-application`, `developer-id-installer` or `mac-installer-distribution` certificate, **any** provisioning profile is refused — Apple's workflow has no provisioning profile for those.

This resource requires at least one profile, so it can only be used with the first group. A certificate in the second group has nothing to pair with it here, and the failure is a CircleCI error at apply reading `provisioning profiles are not allowed for this certificate type`. It cannot be caught at plan time: `cert_type` is unknown while the certificate is itself being created in the same apply. See [`circleci_ios_signing_certificate`](./ios_signing_certificate)'s "Certificate types" section for the full table.

`provisioning_profiles` and `provisioning_profiles_wo` both accept between 1 and 100 entries, which is the API's own cap, and both are checked at plan time so a 101st profile is not a mid-apply refusal.

## Immutability

The CircleCI API has no update endpoint for a signing configuration: the routes are `GET`/`POST /signing/configs` and `DELETE /signing/configs/{id}` — there is not even a `GET /signing/configs/{id}` to read one back by itself (see "No singular data source" below). Every attribute is therefore `RequiresReplace`, `provisioning_profiles_wo_version` included: adding, removing or renewing a provisioning profile, renaming the configuration, or repointing it at a different certificate are all a new resource, not an update.

`provisioning_profiles_wo` itself carries no such marker, and could not usefully: a write-only attribute is null in both the plan and the state, so a plan modifier comparing the two never fires. `provisioning_profiles_wo_version` is what Terraform can see, so it is what drives the replacement.

One consequence of there being no update route is that this resource cannot reproduce `hashicorp/terraform-provider-vault#2900`, where a write-only value sent only on the apply that bumped its version was omitted from an unrelated update and a full-replace endpoint then wiped the credential. Here every write is a create. The provider still sends the profiles on every write rather than gating them on the version, so the guarantee does not depend on the API staying that way.

## Security

CircleCI never returns a provisioning profile's content. With `provisioning_profiles` that means the only copy this provider can compare against on the next `terraform plan` lives in Terraform state, in cleartext — the same position and the same consequences as `circleci_ios_signing_certificate`'s state-backed attributes. See that resource's "Security" section for how to source a credential and protect state accordingly.

A provisioning profile is less sensitive than a certificate's private key — it authorizes devices and app identifiers rather than signing anything itself — but it is still not intended to be public, and the same practices apply: source it from a secret manager, and use an encrypted state backend.

`provisioning_profiles_wo` removes the state copy entirely; see "Keeping the provisioning profiles out of state" above. It is the better option wherever Terraform 1.11 is available.

## No singular data source

There is no `GET /signing/configs/{id}` route, so there is also no singular `circleci_ios_signing_config` data source. Use [`circleci_ios_signing_configs`](../data-sources/ios_signing_configs) and match on `name` or `id` in your configuration — which is exactly what this resource's own `Read` does internally, since it has no more direct route available either.

## Import

Import is supported using `<organization_id>/<config_id>`, because there is no route to look a signing configuration up by id alone — reading one means listing the organization's configurations and matching on id:

```shell
terraform import circleci_ios_signing_config.release "00000000-0000-0000-0000-000000000000/11111111-1111-1111-1111-111111111111"
```

`organization_id`, `name`, `certificate_id`, `certificate_file_name` and `certificate_type` all come back from the subsequent read. `provisioning_profiles` does not: the list route reports each profile's `file_name` but never its `blob`, and populating the list with entries that carry a null `blob` would conflict with `blob` being `Required` inside every element — so the whole attribute is left null after import, exactly as `certificate_blob` is on `circleci_ios_signing_certificate`.

!> **The very next plan replaces the imported configuration.** Every configurable attribute here is `RequiresReplace` (see "Immutability" above), including `provisioning_profiles` and `provisioning_profiles_wo_version`, and `RequiresReplace` fires on *any* change to the attribute's value, including from null (what import leaves behind) to whatever the configuration supplies. A configuration written after import — by either spelling, since the list is required to have at least one entry — plans a **destroy and recreate** on the first `terraform plan` that follows, not an empty plan. This mirrors `circleci_ios_signing_certificate` exactly, and was confirmed the same way, against a real plan; see `TestAccIOSSigningConfigResource_ImportForcesReplacement` and its write-only counterpart.

Practically, that leaves import useful for carrying the identity and the read-only attributes into state without a create call — for example while migrating a hand-managed configuration — rather than for adopting the specific configuration object CircleCI already has: the replacement creates a new one with a new `id` and deletes the old one. If keeping the exact existing configuration matters, do not import; leave it unmanaged instead.
