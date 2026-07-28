data "circleci_audit_log_configs" "all" {
  organization_id = "00000000-0000-0000-0000-000000000000"
}

# There is at most one config per target_type, so this is a reasonable way to
# check whether S3 streaming is configured at all.
output "has_s3_streaming" {
  value = anytrue([
    for config in data.circleci_audit_log_configs.all.audit_log_configs : config.target_type == "S3"
  ])
}

# Configs CircleCI currently reports as unable to deliver.
output "disconnected_configs" {
  value = [
    for config in data.circleci_audit_log_configs.all.audit_log_configs : config.id
    if config.connection_status == "DISCONNECTED"
  ]
}
