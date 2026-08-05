---
page_title: "circleci_ios_signing_certificate Resource - circleci"
subcategory: ""
description: |-
  Manages an Apple code-signing certificate for signing iOS builds.
---

# circleci_ios_signing_certificate (Resource)

Uploads an Apple code-signing certificate (a `.p12` file and its password) to a CircleCI organization, for signing iOS builds. Pair it with one or more provisioning profiles using [`circleci_ios_signing_config`](./ios_signing_config).

## Availability

| | |
| --- | --- |
| **CircleCI Cloud** | Yes |
| **CircleCI Server** | No — served by the CircleCI v3 API, which a Server installation does not route to its public API service. Using this with `deployment = "server"` reports an explicit error rather than the confusing HTTP 404 the request would otherwise produce. |
| **API** | `POST /api/v3/signing/certificates`, `GET` and `DELETE /api/v3/signing/certificates/{id}` |
| **Organization type** | Any. The certificate is only usable by a **macOS executor**, so an organization with no macOS plan can hold one but never use it. |
| **Token** | A personal API token belonging to an organization admin. |

## Example Usage

```terraform
# The certificate content and password are genuine credentials -- the private key
# half of a code-signing identity. Never write a real .p12's bytes as a literal in
# configuration or commit one to the repo the configuration lives in; source both
# from a secret manager instead.

# --- the write-only path, which is the one to prefer -------------------------
#
# certificate_blob_wo and certificate_password_wo are write-only arguments
# (Terraform 1.11 or later): the provider sends them to CircleCI and then discards
# them, so they land in neither Terraform state nor the plan file.
#
# Declaring the variables `ephemeral` means Terraform will not persist them either,
# which closes the last gap. In a real configuration these would usually come from
# a secret manager's ephemeral resource instead of variables -- for example
# `ephemeral.vault_kv_secret_v2.ios_signing.data["p12_base64"]`. An ephemeral value
# may only be referenced from a write-only argument, so this is the only shape that
# keeps the certificate out of state end to end.
variable "distribution_certificate_p12_base64" {
  description = "Base64-encoded .p12 distribution certificate, supplied out of band or read from a secret manager."
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

# Because nothing derived from the certificate is stored, Terraform cannot tell
# that it changed. certificate_wo_version is the rotation counter: increment it
# whenever the certificate or its password changes, or the new certificate is never
# uploaded. One counter covers both values, because a .p12 and the password that
# decrypts it are a single rotatable unit -- re-exporting a certificate always
# produces a new pair.
#
# Incrementing it replaces the resource, which uploads the new certificate and
# deletes the old one. That is not a choice: the API has no update route.
resource "circleci_ios_signing_certificate" "distribution" {
  org_id                  = "00000000-0000-0000-0000-000000000000"
  file_name               = "distribution.p12"
  certificate_blob_wo     = var.distribution_certificate_p12_base64
  certificate_password_wo = var.distribution_certificate_password
  certificate_wo_version  = 1 # bump to 2 to upload a replacement certificate
}

# --- the state-backed path, still supported ----------------------------------
#
# certificate_blob and certificate_password are the original spelling. They work
# identically from CircleCI's point of view, and they are recorded in Terraform
# state in cleartext -- anyone who can read the state file can sign iOS builds as
# your organization. Use them only where write-only arguments are not available
# (Terraform below 1.11), and encrypt state at rest either way.
#
# Set exactly one of certificate_blob and certificate_blob_wo.
variable "development_certificate_p12_base64" {
  description = "Base64-encoded .p12 development certificate, e.g. from Vault or AWS Secrets Manager."
  type        = string
  sensitive   = true
}

variable "development_certificate_password" {
  description = "Password protecting the .p12 above."
  type        = string
  sensitive   = true
}

resource "circleci_ios_signing_certificate" "development" {
  org_id               = "00000000-0000-0000-0000-000000000000"
  file_name            = "development.p12"
  certificate_blob     = var.development_certificate_p12_base64
  certificate_password = var.development_certificate_password
}

# cert_type, fingerprint and the timestamps are reported back by CircleCI; the
# certificate content and password are not, and never will be. This is the same on
# both paths -- the write-only attributes cost nothing in what you can read back.
output "distribution_certificate_fingerprint" {
  value = circleci_ios_signing_certificate.distribution.fingerprint
}
```

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `file_name` (String) A display name for the certificate, for example `distribution.p12`. This is a label only; it does not have to match the file the `certificate_blob` bytes came from. Limited to 40 characters by the API. Changing this value forces a new resource to be created.

~> **It is not part of the certificate's identity.** CircleCI keys a stored certificate on the organization and the certificate's own fingerprint, so uploading the same `.p12` twice returns the first upload's id and leaves its `file_name` alone. Two of these resources holding the same certificate under different names therefore collide onto one id, and the second apply fails with Terraform reporting an inconsistent result. Use one resource per distinct certificate.

### Optional

> **NOTE**: [Write-only arguments](https://developer.hashicorp.com/terraform/language/resources/ephemeral#write-only-arguments) are supported in Terraform 1.11 and later.

- `certificate_blob` (String, Sensitive) The certificate's `.p12` file, base64-encoded (standard encoding), for example `filebase64("distribution.p12")`. CircleCI never returns this value, so it cannot be read back into state on import or drift detection, and **it is stored in Terraform state in cleartext** -- prefer `certificate_blob_wo`, and see the resource-level "Security" section. Changing this value forces a new resource to be created.

Set exactly one of `certificate_blob` and `certificate_blob_wo`. `certificate_password` is required alongside this one.
- `certificate_blob_wo` (String, Sensitive, [Write-only](https://developer.hashicorp.com/terraform/language/resources/ephemeral#write-only-arguments)) The certificate's `.p12` file, base64-encoded (standard encoding), as a write-only argument: Terraform sends it to CircleCI but never records it in state or in a plan file. Requires Terraform 1.11 or later. This is the preferred way to supply a signing certificate — see the resource-level "Security" section.

Because nothing derived from the certificate is stored, Terraform cannot see that it changed. `certificate_password_wo` and `certificate_wo_version` are both required alongside it, and the version must be incremented every time the certificate changes, or the new certificate is never sent — incrementing it replaces the resource, because CircleCI has no route that updates a signing certificate in place.

Set exactly one of `certificate_blob` and `certificate_blob_wo`.
- `certificate_password` (String, Sensitive) The password that unlocks `certificate_blob`. Stored in Terraform state in cleartext, for the same reason as `certificate_blob`; prefer `certificate_password_wo`. Changing this value forces a new resource to be created.

Required alongside `certificate_blob`, and only valid with it: use `certificate_password_wo` with `certificate_blob_wo`.
- `certificate_password_wo` (String, Sensitive, [Write-only](https://developer.hashicorp.com/terraform/language/resources/ephemeral#write-only-arguments)) The password that unlocks `certificate_blob_wo`, as a write-only argument: Terraform sends it to CircleCI but never records it in state or in a plan file. Requires Terraform 1.11 or later.

Required alongside `certificate_blob_wo`, and covered by the same `certificate_wo_version`: a `.p12` and the password that decrypts it are one rotatable unit, so there is no counter of its own to bump.
- `certificate_wo_version` (Number) Rotation counter for `certificate_blob_wo` and `certificate_password_wo`. Increment it whenever either changes: a write-only value leaves no trace in state, so this is the only thing Terraform has to compare, and changing `certificate_blob_wo` on its own is not a change as far as Terraform is concerned.

One counter covers both values because a `.p12` and its password are a single rotatable unit — the password decrypts that specific file, so re-exporting a certificate always produces a new pair. Separate counters would let you send a new certificate with the old password.

Required when `certificate_blob_wo` is set, and must be at least 1. Incrementing it forces a new resource to be created, which uploads the replacement certificate and deletes the old one; the API has no update route.
- `org_id` (String) The unique identifier (UUID) of the organization that owns this signing certificate.

This is the same field as the deprecated `organization_id`; set exactly one of the two.

Changing this value forces a new resource to be created.
- `organization_id` (String, Deprecated) The unique identifier (UUID) of the organization that owns this signing certificate.

~> **Deprecated in favour of `org_id`**, which matches CircleCI's own naming. Both work and mean the same thing; set exactly one. Switching from this attribute to `org_id` does not replace the resource.

### Read-Only

- `cert_type` (String) The certificate's type. CircleCI derives this from the certificate itself — it matches the X.509 Subject Common Name against a fixed set of Apple prefixes — rather than accepting it as input, so it cannot be set and is always Computed. A certificate whose common name matches none of them is rejected on upload rather than stored with a fallback type.

One of `distribution` (`iPhone Distribution:` / `Apple Distribution:`), `development` (`iPhone Developer:` / `Apple Development:`), `developer-id-application`, `developer-id-installer`, `mac-development`, `mac-app-distribution` or `mac-installer-distribution`.

~> The last of those matters for what you can build on top. `circleci_ios_signing_config` requires at least one provisioning profile, and CircleCI **refuses** provisioning profiles for a `developer-id-application`, `developer-id-installer` or `mac-installer-distribution` certificate — Apple's workflow has no profile for those. A certificate of one of those three types can be uploaded with this resource, but no `circleci_ios_signing_config` can be created against it.
- `created_at` (String) When the certificate was uploaded, as an RFC 3339 timestamp with millisecond precision.
- `expires_at` (String) When the certificate expires, as an RFC 3339 timestamp with millisecond precision, or an empty string if CircleCI could not determine an expiry from the certificate.
- `fingerprint` (String) The certificate's fingerprint, as reported by CircleCI.
- `id` (String) Unique identifier (UUID) of the certificate.

## Immutability

The CircleCI API has no update endpoint for a signing certificate: the routes are `GET`/`POST /signing/certificates` and `GET`/`DELETE /signing/certificates/{id}`, nothing else. Every configurable attribute — `organization_id`, `file_name`, `certificate_blob`, `certificate_password` and `certificate_wo_version` — is therefore `RequiresReplace`. Changing any of them uploads a brand new certificate and deletes the old one; it is not a design preference, there is simply no other operation the API supports.

There is also no read route for a certificate's content or password, on this or any other endpoint. `cert_type`, `fingerprint`, `created_at` and `expires_at` are the only attributes CircleCI genuinely reports back; see "Security" below for what that means for the certificate inputs.

## One resource per certificate

`file_name` is a label, not part of a certificate's identity. CircleCI keys a stored certificate on the organization and the certificate's own fingerprint, and re-uploading the same `.p12` returns the id of the first upload and leaves its `file_name` alone.

Two of these resources holding the same certificate under different names therefore collide onto one id: the second apply reads back the first one's `file_name` and Terraform reports the provider produced an inconsistent result. Use one resource per distinct certificate.

## Certificate types

`cert_type` is derived from the certificate rather than configured — CircleCI matches its X.509 Subject Common Name against a fixed set of Apple prefixes, and rejects a certificate matching none of them outright. There are seven values, and which one a certificate has decides whether [`circleci_ios_signing_config`](./ios_signing_config) can be used with it at all:

| `cert_type` | Apple common name prefix | Works with `circleci_ios_signing_config` |
| --- | --- | --- |
| `distribution` | `iPhone Distribution:` / `Apple Distribution:` | Yes |
| `development` | `iPhone Developer:` / `Apple Development:` | Yes |
| `mac-development` | `Mac Developer:` | Yes |
| `mac-app-distribution` | `3rd Party Mac Developer Application:` | Yes |
| `developer-id-application` | `Developer ID Application:` | No |
| `developer-id-installer` | `Developer ID Installer:` | No |
| `mac-installer-distribution` | `3rd Party Mac Developer Installer:` | No |

The three that do not work are the ones with no provisioning profile in Apple's own workflow, and CircleCI enforces that: it **refuses** a signing configuration carrying any provisioning profile for a certificate of one of those types. `circleci_ios_signing_config` requires at least one profile, so there is nothing to pair such a certificate with. You can still upload it with this resource — it just cannot be referenced by a signing configuration.

## Security

The certificate content and its password are real credentials — the private key half of a code-signing identity, and the password protecting it — not display values. Anyone who can read them can sign iOS builds as your organization.

There are two ways to supply them, and they differ in exactly one respect: whether Terraform keeps a copy.

| | Stored in state | Rotate by |
|---|---|---|
| `certificate_blob_wo` + `certificate_password_wo` | nothing | incrementing `certificate_wo_version` |
| `certificate_blob` + `certificate_password` | both values, in cleartext | editing the value |

**Prefer the write-only attributes** (Terraform 1.11 or later). `certificate_blob_wo` and `certificate_password_wo` are write-only arguments: the provider sends them to CircleCI and nothing is persisted — not to state, not to a plan file. Because nothing derived from the certificate is stored, Terraform cannot see that it changed, which is the whole cost: you rotate by incrementing `certificate_wo_version`, and forgetting to bump it means the new certificate is simply never uploaded. One counter covers both values, because a `.p12` and the password that decrypts it are a single rotatable unit — re-exporting a certificate always produces a new pair, and separate counters would let you upload a new certificate with the old password.

The point of a write-only argument is that it accepts an **ephemeral** value, which Terraform also refuses to persist. That closes the last gap — the certificate is then in neither state nor a `.tfvars` file:

```terraform
ephemeral "vault_kv_secret_v2" "ios_signing" {
  mount = "secret"
  name  = "circleci/ios-signing"
}

resource "circleci_ios_signing_certificate" "distribution" {
  org_id    = data.circleci_organization.acme.id
  file_name = "distribution.p12"

  certificate_blob_wo     = ephemeral.vault_kv_secret_v2.ios_signing.data["p12_base64"]
  certificate_password_wo = ephemeral.vault_kv_secret_v2.ios_signing.data["password"]
  certificate_wo_version  = 1
}
```

`certificate_blob` and `certificate_password` remain supported and undeprecated. Both are marked `Sensitive`, which keeps them out of Terraform's plan and apply output, **but `Sensitive` does not encrypt state** — on this path both values are written to Terraform state in cleartext, because that is where Terraform keeps the copy `terraform plan` compares against. CircleCI never returns either value once uploaded, so state is the only copy there is.

Set exactly one of `certificate_blob` and `certificate_blob_wo`; each needs its own password, so `certificate_password` goes with the first and `certificate_password_wo` with the second.

Either way:

* Use a state backend that encrypts at rest (Terraform Cloud, or a backend like S3 with server-side encryption and a restrictive bucket policy) and restrict who can read it. This is the whole mitigation on the state-backed path, and still worth doing on the write-only one.
* Source the `.p12` and its password from a secret manager (Vault, AWS Secrets Manager, etc.) — never from a file committed to the repository the configuration lives in. With the write-only attributes, use the secret manager's `ephemeral` resource rather than a data source, so the value is not persisted on the way through.
* A leaked certificate is revoked by deleting this resource, which deletes it from CircleCI too, and replacing it with a newly issued one — not by an in-place update, since none exists.
* Moving an existing certificate onto the write-only attributes does not scrub it from state versions already stored. Completing the move means rotating the certificate as well as pruning old state.

This resource deliberately has **no computed attribute that echoes back a masked form** of the certificate or its password (contrast [`circleci_webhook`](./webhook)'s `has_signing_secret`, which exists because that API genuinely reports whether a secret is configured). A certificate cannot exist without content and a password — one of the two spellings is always configured — so there is no "configured or not" ambiguity for a boolean to resolve, and a `Sensitive` string that only ever held a fixed mask would look like a credential without being one.

See the [Managing secrets](../guides/managing-secrets) guide for how this compares with the provider's other secret-bearing resources.

## Import

Import is supported using the certificate's id:

```shell
terraform import circleci_ios_signing_certificate.distribution "11111111-1111-1111-1111-111111111111"
```

`organization_id`, `file_name`, `cert_type`, `fingerprint`, `created_at` and `expires_at` all come back from the subsequent read. `certificate_blob` and `certificate_password` do not, and cannot: CircleCI never discloses either value again after the upload that set them, on this or any other route.

!> **The very next plan replaces the imported certificate.** Both credential attributes — `certificate_blob`/`certificate_password` and `certificate_blob_wo`/`certificate_password_wo` plus `certificate_wo_version` — are `RequiresReplace` (see "Immutability" above), and `RequiresReplace` fires on *any* change to the attribute's value, including from null (what import leaves behind) to whatever the configuration supplies. There is no exception in the plugin framework for "this is import filling in a gap the API cannot fill," and this provider adds none of its own: adding one would mean silently accepting a config-supplied credential into state without ever uploading it, which is worse. So a configuration written after import — by either spelling, since one of the two is always required — plans a **destroy and recreate** of the certificate on the first `terraform plan` that follows, not an empty plan. This was confirmed against a real plan; see `TestAccIOSSigningCertificateResource_ImportForcesReplacement` and its write-only counterpart.

Practically, that leaves import useful for one thing: carrying the identity and the read-only attributes into state without a create call, for example while migrating a configuration that used to manage this resource by hand. It does not let a practitioner adopt the specific certificate object CircleCI already has — the replacement uploads a new one with a new `id` and deletes the old one. If keeping the exact existing certificate matters, do not import; leave it unmanaged instead.
