---
page_title: "circleci_usage_export Ephemeral Resource - circleci"
subcategory: ""
description: |-
  Starts a CircleCI usage export job and waits for it to finish, returning signed download URLs.
---

# circleci_usage_export (Ephemeral Resource)

Starts a CircleCI usage export job and waits for it to finish, returning signed URLs the job's data can be downloaded from.

This is an ephemeral resource rather than a managed resource or a data source: creating the job is a side-effecting POST (ruling out a data source), there is nothing to converge a plan onto or meaningfully destroy (ruling out a managed resource), and its result is a signed URL that grants data access on its own — exactly the kind of value that should never be written to a state file. Ephemeral values are never persisted to state or plan files, which is the whole reason this shape is a better fit here than either alternative.

`Open` creates the job and polls it to completion; there is no `Close`, because there is nothing to clean up — a usage export job cannot be canceled or deleted through the API, and CircleCI retains the exported data on its own schedule regardless of what this ephemeral resource does.

> **Polling.** `Open` polls the job's status every 10 seconds (CircleCI rate-limits this endpoint to 10 requests per minute) and gives up after `poll_timeout` (10 minutes by default) if the job has not reached `"completed"` or `"failed"` by then, returning a clear error rather than hanging the run indefinitely. Raise `poll_timeout` for an unusually large export.

## Availability

| | |
| --- | --- |
| **CircleCI Cloud** | Yes |
| **CircleCI Server** | No. This entry used to say yes, on the grounds that usage export is a v2 API rather than v3 — and the API version was the wrong thing to reason from. CircleCI Server's gateway does route both usage export paths to its public API service, but Server does not deploy the reporting service those routes are proxied to, and its public API service is configured with a placeholder upstream for that one backend. The routes therefore exist on Server and cannot succeed there. Using this with `deployment = "server"` reports an explicit error rather than letting the request fail inside CircleCI. **Reasoned rather than measured**: no CircleCI Server installation has been available to test against, so this is derived from which services a Server installation deploys and how its public API service is configured. See the CircleCI Server note on the provider index page. |
| **API** | `POST /api/v2/organizations/{org_id}/usage_export_job` and `GET /api/v2/organizations/{org_id}/usage_export_job/{id}` |
| **Organization type** | Any. |
| **Token** | A personal API token belonging to an organization admin. |

## Limits

CircleCI validates the export window before queueing anything, and each rule is a
separate refusal:

| Rule | Checked at plan time |
| --- | --- |
| `end` must not be before `start` | Yes |
| `end` minus `start` must not exceed 31 days | Yes |
| every `shared_org_ids` entry must be a UUID | Yes |
| `start` must be within the last 366 days | No — depends on when the apply runs |
| neither bound may be in the future | No — depends on when the apply runs |

The first three are refused before any request is made, because CircleCI reports
a bad `shared_org_ids` entry as an unattributed "malformed request body" that
names neither the field nor the entry. The last two are left to CircleCI: they
depend on agreeing what "now" is, and a configuration wrongly refused here could
not be applied at all, where one refused by CircleCI at least says so.

`shared_org_ids` is honoured rather than ignored — it reaches the export query as
an additional set of organizations to report usage for.

The signed URLs in `download_urls` are minted at the moment the job was observed
to be complete and are valid for 36 hours from then (measured: the signed
URL's own `X-Amz-Expires` reads 129599 seconds, matching), so consume them
within the same run rather than passing them on.

**`download_urls` can be an empty list even when `state` is `"completed"`.**
Measured against a live organization: a window with no matching usage data
completes normally with no URLs at all, rather than failing or reporting
something else. Do not treat an empty `download_urls` on a completed job as an
error condition on its own.

## Example Usage

```terraform
ephemeral "circleci_usage_export" "january" {
  org_id = "00000000-0000-0000-0000-000000000000" # the organization's UUID, from Organization Settings in the CircleCI web app
  start  = "2024-01-01T00:00:00Z"                 # RFC 3339; the window is inclusive of `start` and exclusive of `end`
  end    = "2024-02-01T00:00:00Z"

  # Organizations that share billing with `org_id`, to fold into one export
  # rather than exporting each separately.
  shared_org_ids = [
    "11111111-1111-1111-1111-111111111111",
  ]

  # Open polls until the job reaches "completed" or "failed", then gives up. The
  # default is 10 minutes; raise it for a wide date range or many shared
  # organizations. CircleCI rate-limits the status check to 10 requests a minute,
  # so polling happens no more often than every 10 seconds regardless.
  poll_timeout = "20m"
}

# Each download URL is signed and grants access to the data on its own, so they
# are never written to a state or plan file. Pass them directly to whatever
# consumes the export within the same run: an ephemeral value is allowed inside a
# provisioner, which Terraform does not persist, but not in a resource argument
# that would be.
resource "terraform_data" "download_usage_export" {
  provisioner "local-exec" {
    command = "download-usage-export ${join(" ", ephemeral.circleci_usage_export.january.download_urls)}"
  }
}

# Open never returns while the job is still "created" or "processing", so `state`
# here is always terminal. `error_reason` is empty unless it is "failed".
check "usage_export_completed" {
  assert {
    condition     = ephemeral.circleci_usage_export.january.state == "completed"
    error_message = "The usage export job finished in state ${ephemeral.circleci_usage_export.january.state}: ${ephemeral.circleci_usage_export.january.error_reason}"
  }
}
```

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `end` (String) End of the export window, as an RFC 3339 timestamp. CircleCI caps a single export at 744h0m0s and refuses an `end` before `start` or in the future; both of the first two are checked before any request is made. Split a wider range across several exports.
- `start` (String) Start of the export window, as an RFC 3339 timestamp (e.g. `"2024-01-01T00:00:00Z"`). CircleCI refuses a `start` more than 366 days in the past, or in the future.

### Optional

- `org_id` (String) The unique identifier (UUID) of the organization that owns the usage data being exported.

This is the same field as the deprecated `organization_id`; set exactly one of the two.
- `organization_id` (String, Deprecated) The unique identifier (UUID) of the organization that owns the usage data being exported.

~> **Deprecated in favour of `org_id`**, which matches CircleCI's own naming. Both work and mean the same thing; set exactly one.
- `poll_timeout` (String) How long to wait for the export job to reach a terminal state, as a Go duration string (e.g. `"20m"`). Defaults to `10m0s`. Raise this for an unusually large date range; CircleCI's own rate limit on checking a job's status (10 requests per minute) means this resource polls no more often than every 10s regardless of this value.
- `shared_org_ids` (List of String) UUIDs of additional organizations that share billing with `org_id`, to include in the export. CircleCI honours this rather than ignoring it, but rejects the whole request if any entry is not a UUID, without saying which — so entries are checked here first.

### Read-Only

- `download_urls` (List of String, Sensitive) Signed URLs the export's data can be downloaded from. Marked sensitive because each URL itself grants access to the data — anyone holding the URL can download it, with no further authentication. Because this is ephemeral data, these URLs are never written to a state or plan file.

Each URL is minted at the moment the job was observed to be complete and is valid for 36h0m0s from then, so consume them within the same run rather than passing them on.

Can be an empty list even when `state` is `"completed"`: measured against a live organization, a window with no matching usage data completes normally with no URLs at all, rather than failing or omitting a result. Check for this rather than assuming a completed job always has something to download.
- `error_reason` (String) Why the job failed. Empty unless `state` is `"failed"`.
- `id` (String) The usage export job's id.
- `state` (String) The job's terminal state: `"completed"` or `"failed"`. `Open` only returns once the job has reached one of these — it never returns `"created"` or `"processing"`.
