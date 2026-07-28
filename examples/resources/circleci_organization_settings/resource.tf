# Manage only the toggles you care about. Anything left out of the configuration
# is not written, so CircleCI keeps whatever value it already has.
resource "circleci_organization_settings" "example" {
  organization_id = "00000000-0000-0000-0000-000000000000"

  # Accepting the runner terms of service is what unlocks self-hosted runners for
  # the organization; runner resource classes and tokens cannot be created until
  # this is true.
  is_runner_terms_of_service_accepted = true

  # Private orbs must be enabled before the organization can publish or reference
  # one.
  enable_private_orbs = true

  # Tighten up credentials and context usage.
  is_user_checkout_keys_disabled        = true
  is_context_group_restriction_required = true
}

# A second configuration may manage a disjoint set of toggles on the same
# organization, because each resource only writes what it declares.
resource "circleci_organization_settings" "ai_features" {
  organization_id = "00000000-0000-0000-0000-000000000000"

  enable_ai_agents              = false
  enable_ai_error_summarization = false
  enable_minor_ai_features      = false
}
