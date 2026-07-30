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
