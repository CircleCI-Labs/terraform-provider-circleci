# See circleci_ios_signing_certificate's example for why these come from
# variables (backed by a secret manager) rather than literals or files
# committed to the repository.
variable "distribution_certificate_p12_base64" {
  type      = string
  sensitive = true
}

variable "distribution_certificate_password" {
  type      = string
  sensitive = true
}

variable "release_provisioning_profile_base64" {
  description = "Base64-encoded .mobileprovision file."
  type        = string
  sensitive   = true
}

resource "circleci_ios_signing_certificate" "distribution" {
  organization_id      = "00000000-0000-0000-0000-000000000000"
  file_name            = "distribution.p12"
  certificate_blob     = var.distribution_certificate_p12_base64
  certificate_password = var.distribution_certificate_password
}

resource "circleci_ios_signing_config" "release" {
  organization_id = "00000000-0000-0000-0000-000000000000"
  name            = "release-signing"
  certificate_id  = circleci_ios_signing_certificate.distribution.id

  provisioning_profiles = [
    {
      file_name = "release.mobileprovision"
      blob      = var.release_provisioning_profile_base64
    },
  ]
}

# certificate_file_name and certificate_type are filled in from the paired
# certificate; provisioning_profiles[*].blob is never reported back, so there
# is nothing to read here beyond file_name.
output "release_config_certificate_type" {
  value = circleci_ios_signing_config.release.certificate_type
}
