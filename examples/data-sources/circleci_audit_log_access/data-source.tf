data "circleci_audit_log_access" "org" {
  organization_id = "00000000-0000-0000-0000-000000000000"
}

# Fail the plan with a clear message instead of letting a
# circleci_audit_log_config create fail with an opaque 403 at apply time.
check "audit_log_streaming_entitled" {
  assert {
    condition     = data.circleci_audit_log_access.org.has_access
    error_message = "This organization is not entitled to audit log streaming; it requires a CircleCI Cloud Scale plan."
  }
}
