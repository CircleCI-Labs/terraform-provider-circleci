data "circleci_runner_tokens" "example" {
  resource_class = "my-namespace/my-runner"
}

# Only token metadata is available: the secret itself is returned once, when the
# token is created, and can never be read back.
output "token_nicknames" {
  value = [for token in data.circleci_runner_tokens.example.tokens : token.nickname]
}

# Tokens issued outside Terraform, which a rotation policy may want to revoke.
output "unmanaged_token_ids" {
  value = [
    for token in data.circleci_runner_tokens.example.tokens : token.id
    if token.nickname != "managed-by-terraform"
  ]
}
