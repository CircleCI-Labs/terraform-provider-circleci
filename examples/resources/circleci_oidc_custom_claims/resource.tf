# Organization-wide claims: every job in the organization gets these unless a
# project overrides them.
resource "circleci_oidc_custom_claims" "org" {
  org_id   = "00000000-0000-0000-0000-000000000000"
  audience = ["sts.amazonaws.com"]
  ttl      = "1h"
}

# Project-level claims take precedence over the organization's, so a project that
# needs a different audience or a shorter-lived token declares its own.
resource "circleci_oidc_custom_claims" "project" {
  org_id     = "00000000-0000-0000-0000-000000000000"
  project_id = "11111111-1111-1111-1111-111111111111"
  audience   = ["https://vault.example.com"]
  ttl        = "15m"
}

# Only the audience is managed here; the token lifetime stays at CircleCI's
# default.
resource "circleci_oidc_custom_claims" "audience_only" {
  org_id     = "00000000-0000-0000-0000-000000000000"
  project_id = "22222222-2222-2222-2222-222222222222"
  audience   = ["my-identity-provider"]
}
