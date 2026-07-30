data "circleci_organization_settings" "example" {
  org_id = "00000000-0000-0000-0000-000000000000"
}

# Runner onboarding is gated on the terms of service, so check before creating
# runner resources.
output "runners_available" {
  value = data.circleci_organization_settings.example.is_runner_terms_of_service_accepted
}

output "private_orbs_available" {
  value = data.circleci_organization_settings.example.enable_private_orbs
}
