terraform {
  required_providers {
    circleci = {
      source  = "CircleCI-Public/circleci"
      version = "~> 0.4"
    }
  }
}

# CircleCI Cloud (the default).
#
# host defaults to https://circleci.com and deployment defaults to "cloud".
# Supply the API token with the CIRCLE_TOKEN environment variable rather than in
# configuration, so it is not committed to source control.
provider "circleci" {
  # key = "*****" # prefer the CIRCLE_TOKEN environment variable
}

# CircleCI Server.
#
# Server does not route the v3 API, so deployment = "server" makes the provider
# use v2 throughout. Resources that exist only on v3 (organization settings,
# orbs, orb namespaces) are unavailable there and report an explicit error.
provider "circleci" {
  alias = "server"

  host       = "https://circleci.example.com"
  deployment = "server"

  # On Server the self-hosted runner API is served by your own installation
  # rather than by runner.circleci.com.
  runner_host = "https://circleci.example.com"
}
