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
