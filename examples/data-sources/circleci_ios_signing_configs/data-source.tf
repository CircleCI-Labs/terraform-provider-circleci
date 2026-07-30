# There is no singular circleci_ios_signing_config data source: the API has no
# route to fetch one configuration by id. List and match on name instead.
data "circleci_ios_signing_configs" "all" {
  org_id = "00000000-0000-0000-0000-000000000000"
}

output "release_signing_config_id" {
  value = [
    for cfg in data.circleci_ios_signing_configs.all.configs :
    cfg.id if cfg.name == "release-signing"
  ][0]
}
