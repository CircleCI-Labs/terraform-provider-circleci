resource "circleci_orb_namespace" "example" {
  name            = "acme"
  organization_id = "00000000-0000-0000-0000-000000000000"
}

resource "circleci_orb" "example" {
  namespace_id = circleci_orb_namespace.example.id
  name         = "node"
}

# Publishing is permanent: a published orb version cannot be edited, republished
# with different source, or deleted. `terraform destroy` only forgets it.
resource "circleci_orb_version" "v1_0_0" {
  orb_id  = circleci_orb.example.id
  version = "1.0.0"
  yaml    = file("${path.module}/orb.yml")
}

# To change the source, publish another version. The previous one stays
# published, which is what lets pipelines pin to it.
resource "circleci_orb_version" "v1_0_1" {
  orb_id  = circleci_orb.example.id
  version = "1.0.1"
  yaml    = file("${path.module}/orb.yml")
}

# A dev version is the exception: it is mutable and expires, so editing the YAML
# of a dev version works. Use one while iterating.
resource "circleci_orb_version" "dev" {
  orb_id  = circleci_orb.example.id
  version = "dev:alpha"
  yaml    = file("${path.module}/orb.yml")
}

output "orb_reference" {
  description = "What a .circleci/config.yml should reference."
  value       = "${circleci_orb.example.full_name}@${circleci_orb_version.v1_0_1.version}"
}
