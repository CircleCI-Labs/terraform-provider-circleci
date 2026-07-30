# A .mobileprovision file is less sensitive than the certificate's private key --
# it authorizes rather than signs -- but it identifies devices and app identifiers
# and is not meant to be public. Source it from a secret manager, like the
# certificate it is paired with.

# The certificate has its own write-only path; see
# circleci_ios_signing_certificate's example for what these two variables are.
variable "distribution_certificate_p12_base64" {
  description = "Base64-encoded .p12 distribution certificate."
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

resource "circleci_ios_signing_certificate" "distribution" {
  org_id                  = "00000000-0000-0000-0000-000000000000"
  file_name               = "distribution.p12"
  certificate_blob_wo     = var.distribution_certificate_p12_base64
  certificate_password_wo = var.distribution_certificate_password
  certificate_wo_version  = 1
}

# --- the write-only path, which is the one to prefer -------------------------
#
# provisioning_profiles_wo is a write-only argument (Terraform 1.11 or later): the
# provider sends the list to CircleCI and then discards it, so neither the blobs
# nor the file names land in Terraform state or the plan file.
#
# Declaring the variables `ephemeral` means Terraform will not persist them either,
# which closes the last gap. In a real configuration these would usually come from
# a secret manager's ephemeral resource instead of variables -- for example
# `ephemeral.vault_kv_secret_v2.ios_signing.data["release_mobileprovision"]`. An
# ephemeral value may only be referenced from a write-only argument, so this is the
# only shape that keeps the profile out of state end to end.
variable "release_provisioning_profile_base64" {
  description = "Base64-encoded .mobileprovision file, read from a secret manager."
  type        = string
  sensitive   = true
  ephemeral   = true
}

variable "adhoc_provisioning_profile_base64" {
  description = "Base64-encoded .mobileprovision file for ad-hoc distribution."
  type        = string
  sensitive   = true
  ephemeral   = true
}

# Because nothing derived from the list is stored, Terraform cannot tell that it
# changed. provisioning_profiles_wo_version is the rotation counter: increment it
# whenever anything in the list changes, or the new profiles are never sent. One
# counter covers the whole list, because the list is one rotatable unit -- adding,
# removing, renewing or reordering a profile already replaces the resource rather
# than updating an entry in place.
#
# Incrementing it forces a new resource with a new id. That is not a choice: the
# API has no update route for a signing configuration.
#
# Note that file_name is write-only here too. Every child of a write-only nested
# attribute must itself be write-only, so the name goes with the blob. Nothing is
# lost by that: CircleCI never reports a profile's content back, and the
# circleci_ios_signing_configs data source reads profile names from CircleCI rather
# than from Terraform state.
resource "circleci_ios_signing_config" "release" {
  org_id         = "00000000-0000-0000-0000-000000000000"
  name           = "release-signing"
  certificate_id = circleci_ios_signing_certificate.distribution.id

  provisioning_profiles_wo = [
    {
      file_name = "release.mobileprovision"
      blob      = var.release_provisioning_profile_base64
    },
    {
      file_name = "adhoc.mobileprovision"
      blob      = var.adhoc_provisioning_profile_base64
    },
  ]
  provisioning_profiles_wo_version = 1 # bump to 2 to send a changed list
}

# --- the state-backed path, still supported ----------------------------------
#
# provisioning_profiles is the original spelling. It works identically from
# CircleCI's point of view, and every blob in it is recorded in Terraform state in
# cleartext. Use it only where write-only arguments are not available (Terraform
# below 1.11), and encrypt state at rest either way.
#
# Set exactly one of provisioning_profiles and provisioning_profiles_wo.
variable "internal_provisioning_profile_base64" {
  description = "Base64-encoded .mobileprovision file for internal builds."
  type        = string
  sensitive   = true
}

resource "circleci_ios_signing_config" "internal" {
  org_id         = "00000000-0000-0000-0000-000000000000"
  name           = "internal-signing"
  certificate_id = circleci_ios_signing_certificate.distribution.id

  provisioning_profiles = [
    {
      file_name = "internal.mobileprovision"
      blob      = var.internal_provisioning_profile_base64
    },
  ]
}

# certificate_file_name and certificate_type are filled in from the paired
# certificate, on both paths. A profile's content is never reported back on either,
# so there is nothing to read here beyond what the certificate contributes.
output "release_config_certificate_type" {
  value = circleci_ios_signing_config.release.certificate_type
}
