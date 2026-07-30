---
page_title: "Managing secrets"
subcategory: "Guides"
description: |-
  Where CircleCI secrets end up when Terraform manages them, and how to keep them out of state.
---

# Managing secrets

Terraform stores the values it manages so it can compare them on the next plan.
For a secret that means the secret is written to state, and **`sensitive = true`
does not change that** — it hides values from CLI output, nothing more.
HashiCorp's own documentation is explicit:

> Terraform stores values with the `sensitive` argument in both state and plan
> files, and anyone who can access those files can access your sensitive values.

That is the normal position across the ecosystem, including HashiCorp's Vault
provider. It is not a defect. But since Terraform 1.11 there is a better option
for secrets specifically, and this provider supports it.

## The short version

| You want | Use |
|---|---|
| The simplest thing that works | `value` / `signing_secret` / `certificate_blob` — stored in state |
| The secret never written to state | `value_wo` / `signing_secret_wo` / `certificate_blob_wo` plus a `_wo_version` |

Both are supported side by side, indefinitely. Nothing is deprecated, and
switching does not destroy anything.

## What CircleCI's API returns

This shapes everything below, so it is worth stating plainly. **CircleCI never
returns a secret you have written.** Verified against the live API:

| Attribute | On read the API returns |
|---|---|
| `circleci_context_environment_variable.value` | `null` — and a `truncated_value` (the last few characters), plus `updated_at` |
| `circleci_project_environment_variable.value` | a masked value such as `xxxx1234` |
| `circleci_webhook.signing_secret` | a mask of asterisks |
| `circleci_ios_signing_certificate.certificate_blob` | nothing — no read route exists at all |
| `circleci_ios_signing_config.provisioning_profiles[*].blob` | nothing — the list response reports only each profile's `file_name` |
| `circleci_otel_exporter.headers` | every value as the placeholder `xxxx`; the header *names* in full |

So the provider cannot verify a secret by reading it back. With the state-backed
attributes it compares configuration against its own stored copy; that copy is
the reason the secret is in state.

~> **The truncated and masked forms are not usable as change signals**, and this
provider deliberately does not compare them. Changing a value while keeping its
last four characters produces an identical `truncated_value` — confirmed by
testing against the live API — so a rotation would go undetected. A community
CircleCI provider does compare the mask, and silently misses exactly that case.

## Using write-only arguments

A write-only argument is never persisted — not to state, not to the plan. It goes
from configuration to the API and is gone.

```terraform
variable "registry_token" {
  type      = string
  sensitive = true
  ephemeral = true
}

resource "circleci_context_environment_variable" "registry_token" {
  context_id = circleci_context.build.id
  name       = "REGISTRY_TOKEN"

  value_wo         = var.registry_token
  value_wo_version = 1
}
```

Because the value is not stored, **Terraform cannot see that it changed.** That
is the whole trade: you get a secret that is absent from state, and in exchange
you tell the provider when to write it by incrementing `value_wo_version`.

```terraform
resource "circleci_context_environment_variable" "registry_token" {
  context_id = circleci_context.build.id
  name       = "REGISTRY_TOKEN"

  value_wo         = var.registry_token
  value_wo_version = 2 # bumped when the token was rotated
}
```

-> Set exactly one of `value` and `value_wo`. Setting `value_wo` without a
version is an error rather than a silent no-op — without that check the secret
would be written once and then be unrotatable forever, with Terraform reporting
"no changes" every time you edited it.

### Feeding one from a secret manager

The point of a write-only argument is that it can accept an **ephemeral** value,
which Terraform also refuses to persist. That closes the last gap: the secret is
in neither state nor a `.tfvars` file.

```terraform
ephemeral "vault_kv_secret_v2" "registry" {
  mount = "secret"
  name  = "circleci/registry"
}

resource "circleci_context_environment_variable" "registry_token" {
  context_id = circleci_context.build.id
  name       = "REGISTRY_TOKEN"

  value_wo         = ephemeral.vault_kv_secret_v2.registry.data["token"]
  value_wo_version = 1
}
```

An ephemeral value may only be referenced from a write-only argument. Assigning
one to an ordinary attribute is an error.

## Where each option is available

| Resource | State-backed | Write-only |
|---|---|---|
| `circleci_context_environment_variable` | `value` | `value_wo` + `value_wo_version` |
| `circleci_project_environment_variable` | `value` | `value_wo` + `value_wo_version` |
| `circleci_webhook` | `signing_secret` | `signing_secret_wo` + `signing_secret_wo_version` |
| `circleci_ios_signing_certificate` | `certificate_blob`, `certificate_password` | `certificate_blob_wo` + `certificate_password_wo` + `certificate_wo_version` |
| `circleci_ios_signing_config` | `provisioning_profiles[*].blob` | `provisioning_profiles_wo` + `provisioning_profiles_wo_version` |
| `circleci_otel_exporter` | `headers` | `headers_wo` + `headers_wo_version` |

Every secret this provider writes now has a write-only spelling. Nothing is
deprecated: the state-backed names keep working, indefinitely, and switching
between them does not destroy anything on the two resources that update in place.

-> **The iOS signing certificate is the one to move first.** `certificate_blob`
is the private half of an Apple code-signing identity, so anyone who can read the
state file can sign builds as your organization — a materially worse exposure
than an environment variable. Its write-only path takes **one** version counter
for both values, `certificate_wo_version`, rather than one each: a `.p12` and the
password that decrypts it are a single rotatable unit, and separate counters
would let you upload a new certificate with the old password.

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
  certificate_wo_version  = 1 # bump to upload a replacement
}
```

Bumping it replaces the resource rather than rewriting it in place, unlike
`value_wo_version` and `signing_secret_wo_version`. That is not a different
policy: the certificate API has no update route at all, so replacement is already
the only way anything about a certificate changes. The same is true of
`provisioning_profiles_wo_version` and `headers_wo_version` below, for the same
reason.

### Two of the six are shaped slightly differently

Both differences follow from what the resource already was, not from a change in
policy.

-> **`circleci_otel_exporter` takes *at most* one of `headers` and `headers_wo`,
not exactly one.** An exporter that needs no credentials sets neither, which has
always been valid — so requiring one would have broken working configurations.
There is also a cost specific to this resource: on the `headers` path a header
*added or removed* outside Terraform is detected, because CircleCI returns header
names in full and the provider compares them against the names in state. On the
`headers_wo` path there are no names in state, so that detection is gone too.
Bumping the version re-asserts your headers either way.

-> **`circleci_ios_signing_config` moves the whole `provisioning_profiles` list at
once**, as `provisioning_profiles_wo`, with one counter for the list. The nested
`blob` cannot be made write-only on its own: the plugin framework requires every
child of a write-only nested attribute to be write-only, so `file_name` goes with
it and leaves state as well. Nothing depends on `file_name` being in state — the
resource never reads it back, importing never recovered it, and the
`circleci_ios_signing_configs` data source reads profile names from CircleCI
rather than from state.

## Detecting changes made outside Terraform

`circleci_context_environment_variable` detects a value changed in the CircleCI
UI or by another tool, and reports it as drift so the next apply re-asserts your
configured value. It does this by comparing the `updated_at` CircleCI reports
against the timestamp of Terraform's own last write — never by comparing the
secret, which it cannot read.

This works on **both** the state-backed and write-only paths, because it needs no
copy of the value.

The other secret-bearing resources have no equivalent, because their APIs expose
no per-value modification timestamp. An out-of-band change to a project
environment variable, a webhook signing secret, a signing certificate or a
provisioning profile is invisible to Terraform until you change the
configuration.

`circleci_otel_exporter` is a partial exception, and only on the `headers` path: a
header *added or removed* elsewhere is detected, because the names come back in
full, while a changed *value* is not. See the note above for why `headers_wo`
gives up even that much.

## Runner tokens

A runner token is issued by CircleCI rather than supplied by you, so the shape of
the problem is different, and there is an option that avoids state entirely:

```terraform
# Never written to state. Read fresh on every operation.
ephemeral "circleci_ephemeral_runner_token" "agent" {
  org_id         = data.circleci_organization.example.id
  resource_class = circleci_runner_resource_class.builders.resource_class
  nickname       = "builder-01"
}
```

The `circleci_runner_token` **resource** persists `token` in state, which is what
makes it referenceable later; the ephemeral resource does not, which is what
makes it safe. Prefer the ephemeral one unless you need the token to outlive the
operation. See [Self-hosted runners](self-hosted-runners).

## Securing state

Whatever else you do, treat the state file as a secret store, because it is one.

- **Store it remotely** with access controls, and review who has read access.
  Note that on HCP Terraform the lowest workspace role that can read state can
  read *all* of it.
- **Encrypt it at rest.** For an S3 backend, prefer SSE-KMS with a restrictive
  key policy over the default SSE-S3 — plain at-rest encryption is transparent to
  any caller already authorised to read the object, so it does little against the
  realistic risk, which is over-broad access.
- **Remember state history.** Adopting write-only arguments does not scrub
  secrets from state versions already stored. Completing the move means rotating
  the secret *and* pruning old state.

## What this provider deliberately does not do

Recorded because each was considered and rejected for a reason.

- **It does not store a hash of the secret.** Some providers do, to detect change
  without storing the value. A hash of a low-entropy secret is itself a
  disclosure, HashiCorp maintainers have discouraged the pattern, and where a
  hash is used to drive a plan it tends to produce a configuration that cannot
  converge.
- **It does not store the API's masked or truncated value.** It does not detect
  rotations that preserve the visible characters. See the callout above.
- **It does not encrypt secrets into state itself.** HashiCorp is removing that
  pattern from their own providers.
- **It does not warn you for using the state-backed attribute.** A validator
  exists for that; Terraform's maintainers have asked providers not to emit it,
  because it appears on every run and users of shared modules cannot act on it.
