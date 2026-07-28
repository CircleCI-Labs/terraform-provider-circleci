# The certificate content and password are genuine credentials. Never write a
# real .p12's bytes as a literal in configuration or commit one to the repo the
# configuration lives in -- source both from a secret manager instead. This
# example uses input variables as the obvious placeholder for "wherever your
# secret manager puts it"; see the resource documentation's "Security" section.
variable "distribution_certificate_p12_base64" {
  description = "Base64-encoded .p12 distribution certificate, e.g. from Vault or AWS Secrets Manager."
  type        = string
  sensitive   = true
}

variable "distribution_certificate_password" {
  description = "Password protecting the .p12 above."
  type        = string
  sensitive   = true
}

resource "circleci_ios_signing_certificate" "distribution" {
  organization_id      = "00000000-0000-0000-0000-000000000000"
  file_name            = "distribution.p12"
  certificate_blob     = var.distribution_certificate_p12_base64
  certificate_password = var.distribution_certificate_password
}

# cert_type, fingerprint and the timestamps are reported back by CircleCI; the
# certificate content and password are not, and never will be.
output "distribution_certificate_fingerprint" {
  value = circleci_ios_signing_certificate.distribution.fingerprint
}
