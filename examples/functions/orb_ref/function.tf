# namespace/orb@version — the fully qualified form the orb version API's
# filter[ref] parameter requires. A bare version string such as "1.2.3"
# resolves nothing on its own.
output "qualified_ref" {
  value = provider::circleci::orb_ref("circleci", "node", "1.2.3")
}

output "dev_release_ref" {
  value = provider::circleci::orb_ref("circleci", "node", "dev:my-branch")
}
