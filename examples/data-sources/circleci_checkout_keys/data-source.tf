data "circleci_checkout_keys" "all" {
  project_slug = "github/my-org/my-repo"
}

# All fingerprints, for an add_ssh_keys step.
output "fingerprints" {
  value = [for key in data.circleci_checkout_keys.all.checkout_keys : key.fingerprint]
}

# Just the deploy keys, with SHA256 fingerprints.
data "circleci_checkout_keys" "sha256" {
  project_slug = "github/my-org/my-repo"
  digest       = "sha256"
}

output "deploy_keys" {
  value = [
    for key in data.circleci_checkout_keys.sha256.checkout_keys : key.fingerprint
    if key.type == "deploy-key"
  ]
}
