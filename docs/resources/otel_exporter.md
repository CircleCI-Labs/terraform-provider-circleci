---
page_title: "circleci_otel_exporter Resource - circleci"
subcategory: ""
description: |-
  Manages an OTLP exporter, where CircleCI sends OpenTelemetry traces for an organization's pipelines.
---

# circleci_otel_exporter (Resource)

Manages an OTLP exporter: where CircleCI sends OpenTelemetry traces for an organization's pipelines.

## Availability

| | |
| --- | --- |
| **CircleCI Cloud** | Yes |
| **CircleCI Server** | **No.** The provider gates this and reports an explicit error rather than attempting the request. Settled by route ownership rather than API version: `/api/v2/otel` is proxied to a backend that a CircleCI Server installation does not deploy, and its gateway has no route for this path either. Note that being v2 is not evidence either way on its own: some v2 routes are forwarded by a Server installation's gateway and others are not — `circleci_pipeline_definition` is v2 and unavailable, while the URL orb allow list is v2 and available. |
| **API** | `GET` and `POST /api/v2/otel/exporters`, `GET` and `DELETE /api/v2/otel/exporters/{id}` |
| **Organization type** | Any. |
| **Token** | A personal API token belonging to an organization admin. |

!> **Experimental.** CircleCI flags the OTLP exporter endpoints as experimental. Their request and response shapes may change, or they may be withdrawn, without the deprecation notice the rest of the v2 API carries. Pin the provider version if that matters to you.

## Example Usage

```terraform
# Export traces to a collector over OTLP/gRPC. Note that the endpoint is a bare
# host and port: no scheme.

# --- the write-only path, which is the one to prefer -------------------------
#
# headers_wo is a write-only argument (Terraform 1.11 or later): the provider sends
# the headers to CircleCI and then discards them, so they land in neither Terraform
# state nor the plan file. Headers usually carry the collector's credentials, which
# is what makes this worth doing.
#
# Declaring the variable `ephemeral` means Terraform will not persist it either,
# which closes the last gap. In a real configuration this would usually come from a
# secret manager's ephemeral resource instead of a variable -- for example
# `ephemeral.vault_kv_secret_v2.collector.data["api_key"]`. An ephemeral value may
# only be referenced from a write-only argument.
variable "collector_api_key_wo" {
  description = "Collector credential, read from a secret manager."
  type        = string
  sensitive   = true
  ephemeral   = true
}

# Because nothing derived from the headers is stored, Terraform cannot tell that
# they changed. headers_wo_version is the rotation counter: increment it whenever
# any header changes, or the new headers are never sent. One counter covers the
# whole map.
#
# Incrementing it forces a new resource with a new id, exactly as editing `headers`
# does. That is not a choice: the API has no update route, so traces are not
# exported during the gap.
resource "circleci_otel_exporter" "grpc" {
  org_id   = "00000000-0000-0000-0000-000000000000"
  endpoint = "otel.example.com:4317"
  protocol = "grpc"

  headers_wo = {
    "x-api-key" = var.collector_api_key_wo
  }
  headers_wo_version = 1 # bump to 2 to send rotated credentials
}

# --- the state-backed path, still supported ----------------------------------
#
# headers is the original spelling. It works identically from CircleCI's point of
# view, and it is recorded in Terraform state in cleartext. It does keep one thing
# headers_wo cannot: because the API returns header *names* in full, the provider
# compares the names in state against the names CircleCI reports, and so notices a
# header added or removed outside Terraform. headers_wo stores no names, so it
# notices neither that nor a changed value.
#
# Set at most one of headers and headers_wo.
variable "collector_api_key" {
  type      = string
  sensitive = true
}

resource "circleci_otel_exporter" "grpc_stateful" {
  org_id   = "00000000-0000-0000-0000-000000000000"
  endpoint = "otel-legacy.example.com:4317"
  protocol = "grpc"

  headers = {
    "x-api-key" = var.collector_api_key
  }
}

# --- no headers at all --------------------------------------------------------
#
# Neither attribute is required: an exporter that needs no credentials sets
# neither. That is why the two conflict rather than being exactly-one-of.

# OTLP/HTTP, usually on port 4318.
resource "circleci_otel_exporter" "http" {
  org_id   = "00000000-0000-0000-0000-000000000000"
  endpoint = "otel-http.example.com:4318"
  protocol = "http"
}

# A collector reachable only over a private network, where transport security is
# handled elsewhere. Anything sent in headers or headers_wo travels in the clear.
resource "circleci_otel_exporter" "internal" {
  org_id   = "00000000-0000-0000-0000-000000000000"
  endpoint = "collector.internal:4317"
  protocol = "grpc"
  insecure = true
}

# CircleCI reports configuration problems it detects, such as an endpoint that
# stopped resolving. This is unaffected by which header spelling was used.
output "exporter_issues" {
  value = circleci_otel_exporter.grpc.issues
}
```

### Keeping the collector credentials out of state

`headers_wo` is a write-only alternative to `headers`, available with Terraform 1.11 or later. Set **at most** one of the two — unlike the other write-only pairs in this provider, neither is required, because an exporter that needs no credentials sets neither.

Because a write-only argument is never persisted, it is the only kind of argument an `ephemeral` value may be assigned to, which is what lets the credential come straight from a secret manager without passing through a `.tfvars` file:

```terraform
ephemeral "vault_kv_secret_v2" "collector" {
  mount = "secret"
  name  = "observability/otel-collector"
}

resource "circleci_otel_exporter" "grpc" {
  org_id   = "00000000-0000-0000-0000-000000000000"
  endpoint = "otel.example.com:4317"
  protocol = "grpc"

  headers_wo = {
    "x-api-key" = ephemeral.vault_kv_secret_v2.collector.data["api_key"]
  }
  headers_wo_version = 1
}
```

The trade between the two is a real one, in both directions:

* `headers` is recorded in Terraform state in cleartext, and rotating a credential is just editing the value — Terraform sees the change and replaces the exporter.
* `headers_wo` is sent to CircleCI and then discarded: it appears in neither state nor the plan file. But because nothing derived from it is stored, Terraform cannot see that it changed, so **`headers_wo_version` must be incremented every time any header changes**. Editing `headers_wo` on its own produces no diff and is never sent. One counter covers the whole map.

Incrementing `headers_wo_version` forces a new resource, exactly as editing `headers` does — see "Replacement, not update" below. That is a property of the API, not of the write-only path.

See [Managing secrets](../guides/managing-secrets) for the whole picture.

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `endpoint` (String) Where CircleCI sends spans. Two forms are accepted:

- a bare host and port, such as `otel.example.com:4317` — the port is required; or
- an `http://` or `https://` URL, such as `https://otel.example.com/v1/traces`, which is only valid together with `protocol = "http"`.

Any other scheme, `grpc://` included, is rejected. The host must resolve publicly: CircleCI refuses an endpoint that resolves to a private, loopback or link-local address. Changing this value forces a new resource to be created.
- `protocol` (String) The OTLP transport: `grpc` (usually port 4317) or `http` (usually port 4318). Changing this value forces a new resource to be created.

### Optional

> **NOTE**: [Write-only arguments](https://developer.hashicorp.com/terraform/language/resources/ephemeral#write-only-arguments) are supported in Terraform 1.11 and later.

- `headers` (Map of String, Sensitive) Extra headers sent with each export, typically the collector's credentials. Changing this value forces a new resource to be created.

They are recorded in Terraform state in cleartext. Use `headers_wo` instead to keep them out of state, at the cost of having to bump `headers_wo_version` to rotate them and of losing the drift detection described below. Set at most one of the two; setting neither sends no headers.

~> **Header values cannot be read back.** CircleCI encrypts them at rest and every read answers with the placeholder `xxxx`, so Terraform cannot detect a value changed outside Terraform. A header *added or removed* outside Terraform is detected, because the names are returned in full.
- `headers_wo` (Map of String, Sensitive, [Write-only](https://developer.hashicorp.com/terraform/language/resources/ephemeral#write-only-arguments)) Extra headers sent with each export, typically the collector's credentials, as a write-only argument: Terraform sends them to CircleCI but never records them in state or in a plan file. Requires Terraform 1.11 or later.

Because nothing derived from the headers is stored, Terraform cannot see that they changed. `headers_wo_version` is required alongside them, and must be incremented every time any header changes, or the new headers are never sent — incrementing it replaces the exporter, because CircleCI has no route that updates one in place.

Set at most one of `headers` and `headers_wo`. Setting neither sends no headers.

~> **A changed header value is never detected on either path.** CircleCI never discloses a header's value, only its name. A header *added or removed* outside Terraform is detected here too, through the computed `headers_wo_names` attribute, and — because `headers` already forces replacement — detecting one recreates the exporter, the same as it does on the `headers` path.
- `headers_wo_version` (Number) Rotation counter for `headers_wo`. Increment it whenever any header in `headers_wo` changes: a write-only value leaves no trace in state, so this is the only thing Terraform has to compare, and changing `headers_wo` on its own is not a change as far as Terraform is concerned.

Required when `headers_wo` is set, and must be at least 1. Incrementing it forces a new resource to be created, exactly as changing `headers` does: CircleCI has no route that updates an exporter in place. The exporter gets a new `id` and traces are not exported during the gap.
- `insecure` (Boolean) Whether to connect to the endpoint without transport security. CircleCI defaults this to `false`. Leave it false unless the collector is reachable only over a private network: headers, including any credentials, travel in the clear otherwise. Changing this value forces a new resource to be created.
- `org_id` (String) The unique identifier (UUID) of the organization that owns this exporter.

This is the same field as the deprecated `organization_id`; set exactly one of the two.

Changing this value forces a new resource to be created.
- `organization_id` (String, Deprecated) The unique identifier (UUID) of the organization that owns this exporter.

~> **Deprecated in favour of `org_id`**, which matches CircleCI's own naming. Both work and mean the same thing; set exactly one. Switching from this attribute to `org_id` does not replace the resource.

### Read-Only

- `headers_wo_names` (Set of String) The header names CircleCI reports for this exporter, populated only when headers are managed through `headers_wo`. CircleCI discloses every header name in full — only the values are masked — so recording the names here reveals nothing that reading the exporter does not already reveal, and it is what lets a read detect a header added or removed outside Terraform on this path, the way `headers` already does on its own.

~> **An added or removed header recreates the exporter.** Detecting one adopts CircleCI's header map into `headers`, and `headers` already forces replacement — the same behavior the `headers` path has always had for this kind of drift, not something new here. A changed header *value* is still undetectable on both paths: CircleCI never discloses values, only names.
- `id` (String) Unique identifier (UUID) of the exporter, assigned by CircleCI.
- `issues` (List of String) Validation problems CircleCI has detected with this exporter, such as an endpoint that no longer resolves. Empty when there are none.

## Replacement, not update

The CircleCI API has no route for updating an exporter, so `organization_id`, `endpoint`, `protocol`, `insecure`, `headers` and `headers_wo_version` all force replacement. Changing any of them destroys the exporter and creates a new one with a **new `id`**; traces are not exported during the gap.

`headers_wo` itself carries no such marker, and could not usefully: a write-only attribute is null in both the plan and the state, so a plan modifier comparing the two never fires. `headers_wo_version` is what Terraform can see, so it is what drives the replacement.

One consequence of there being no update route is that this resource cannot reproduce `hashicorp/terraform-provider-vault#2900`, where a write-only value sent only on the apply that bumped its version was omitted from an unrelated update and a full-replace endpoint then wiped the credential. Here every write is a create. The provider still sends the headers on every write rather than gating them on the version, so the guarantee does not depend on the API staying that way.

## Headers cannot be read back

CircleCI encrypts header values at rest and every read answers with the placeholder `xxxx`, never the value you sent. On the `headers` path two things follow:

* **A header value changed outside Terraform cannot be detected.** The provider keeps the value from state rather than overwriting your secret with the placeholder, so a plan stays clean either way.
* **A header added or removed outside Terraform *is* detected**, because the header *names* are returned in full. The provider then adopts the API's map, which surfaces the change as a diff and, since `headers` forces replacement, recreates the exporter.

For the same reason, importing an exporter that has headers records the placeholder values. Set the real values in your configuration and apply; the exporter is recreated with them.

### On the `headers_wo` path, no header drift is detected at all

The add/remove detection above works by comparing the names in state against the names CircleCI reports. On the write-only path there are no names in state: `headers_wo` is null there by construction, and a refresh is given no access to your configuration, so there is nothing to compare against. `headers` is therefore left null rather than being filled in from the API's answer — filling it in would put the placeholder into a state-backed attribute that forces replacement, and every later plan would want to recreate the exporter forever.

So on the write-only path:

* a changed header value is not detected — the same as on the `headers` path;
* a header **added or removed** outside Terraform is **also** not detected — unlike on the `headers` path.

Bumping `headers_wo_version` re-asserts your configured headers, which is the remedy for either. Importing an exporter leaves both attributes to be supplied afterwards, as it does today.

~> **`insecure = true` sends headers in the clear.** Anything in `headers` or `headers_wo` — including collector credentials — travels unencrypted. Use it only where the network itself is trusted.

## Limits

An organization may have at most **5** exporters. Creating a sixth is rejected.

## Endpoint format

`endpoint` is a bare host and port, for example `otel.example.com:4317`. Do not include a scheme: `https://otel.example.com:4317` is rejected. gRPC collectors conventionally listen on `4317` and HTTP collectors on `4318`.

## Import

Import is supported using `organization_id/exporter_id`:

```shell
terraform import circleci_otel_exporter.grpc "00000000-0000-0000-0000-000000000000/11111111-1111-1111-1111-111111111111"
```

Both parts are required. The API has no route for a single exporter, so reading one means listing its organization's exporters and selecting from them — the provider needs the organization ID to do that.
