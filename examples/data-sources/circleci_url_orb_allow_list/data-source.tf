data "circleci_url_orb_allow_list" "example" {
  organization = "gh/acme"
}

output "allowed_orb_prefixes" {
  value = [for entry in data.circleci_url_orb_allow_list.example.entries : entry.prefix]
}
