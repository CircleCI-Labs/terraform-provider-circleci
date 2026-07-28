ephemeral "circleci_usage_export" "example" {
  organization_id = "00000000-0000-0000-0000-000000000000"
  start           = "2024-01-01T00:00:00Z"
  end             = "2024-02-01T00:00:00Z"
}

# The download URLs are signed and grant data access on their own, so they are
# never written to a state or plan file. Pass them directly to whatever
# downloads the export within the same run — for example a provisioner, or an
# external program invoked via the `terraform_data` resource's write-only
# arguments — rather than storing them anywhere that outlives this apply.
