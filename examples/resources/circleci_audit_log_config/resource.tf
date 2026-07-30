# Stream audit log events to an AWS S3 bucket. CircleCI assumes this role via
# OIDC to write to the bucket, so it must trust CircleCI's OIDC provider and
# have write access to the bucket.
resource "circleci_audit_log_config" "s3" {
  org_id        = "00000000-0000-0000-0000-000000000000"
  target_type   = "S3"
  arn           = "arn:aws:iam::123456789012:role/circleci-audit-logs"
  bucket_name   = "acme-audit-logs"
  bucket_prefix = "circleci"
  region        = "us-east-1"
}

# An S3-compatible destination, such as a self-hosted MinIO. endpoint is
# required here; region is optional and defaults to "us-east-1" server-side.
resource "circleci_audit_log_config" "minio" {
  org_id      = "00000000-0000-0000-0000-000000000000"
  target_type = "S3_COMPATIBLE"
  arn         = "arn:minio:iam::role/circleci-audit-logs"
  bucket_name = "acme-audit-logs"
  endpoint    = "https://minio.example.com"
}

# Keep a config in place without deleting it: is_disabled stops delivery, but
# unlike a delete, it can be turned back on without losing the destination
# configuration. Note that re-enabling it does not re-verify connectivity, so
# it only matters at the moment streaming is turned back on.
resource "circleci_audit_log_config" "paused" {
  org_id      = "00000000-0000-0000-0000-000000000000"
  target_type = "S3"
  is_disabled = true
  arn         = "arn:aws:iam::123456789012:role/circleci-audit-logs"
  bucket_name = "acme-audit-logs-cold"
  region      = "us-east-1"
}

output "s3_connection_status" {
  value = circleci_audit_log_config.s3.connection_status
}
