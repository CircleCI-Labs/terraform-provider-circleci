data "circleci_organization" "acme" {
  slug = "gh/acme"
}

data "circleci_context" "deploy" {
  name   = "production-deploy"
  org_id = data.circleci_organization.acme.id
}

# Metadata only. The CircleCI API returns a context environment variable's value on
# no route at all, not even the one that just set it, so this data source reports
# timestamps and confirms that the variable exists — it cannot read the secret.
data "circleci_context_environment_variable" "registry_token" {
  context_id = data.circleci_context.deploy.id
  name       = "REGISTRY_TOKEN"
}

output "circleci_registry_token_metadata" {
  value = {
    context_id = data.circleci_context_environment_variable.registry_token.context_id
    created_at = data.circleci_context_environment_variable.registry_token.created_at

    # CircleCI bumps this on every write, including one that stores an identical
    # value, so it tracks when the variable was last written rather than when it
    # last changed.
    updated_at = data.circleci_context_environment_variable.registry_token.updated_at
  }
}
