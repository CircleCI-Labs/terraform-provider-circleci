locals {
  parsed = provider::circleci::parse_project_slug("gh/my-org/my-repo")
}

output "org" {
  value = local.parsed.org
}

output "vcs_type" {
  value = local.parsed.vcs_type
}
