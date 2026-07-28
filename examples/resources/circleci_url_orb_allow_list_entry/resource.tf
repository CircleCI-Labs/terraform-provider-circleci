# Allow URL orbs served from a specific GitHub path, fetched with the
# organization's GitHub App installation.
resource "circleci_url_orb_allow_list_entry" "circleci_public" {
  organization = "gh/acme"
  name         = "CircleCI-Public orbs"
  prefix       = "https://raw.githubusercontent.com/CircleCI-Public/orbs/refs/heads/main/"
  auth         = "github-app"
}

# A publicly readable mirror needs no credentials. Keep the prefix as narrow as
# possible: any URL that starts with it is permitted.
resource "circleci_url_orb_allow_list_entry" "internal_mirror" {
  organization = "00000000-0000-0000-0000-000000000000"
  name         = "internal orb mirror"
  prefix       = "https://orbs.internal.example.com/approved/"
  auth         = "none"
}
