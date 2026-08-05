# Both lists are sets: CircleCI does not preserve the order addresses were
# submitted in, so order here does not matter and will never show as a change.
resource "circleci_organization_contacts" "example" {
  org_id = "00000000-0000-0000-0000-000000000000"

  primary_contacts = [
    "platform-team@example.com",
    "jane.doe@example.com",
  ]

  security_contacts = [
    "security@example.com",
  ]
}
