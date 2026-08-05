# A definition whose configuration is hosted by CircleCI itself, rather than read
# from a VCS repository. config_source_repo_external_id must be omitted here — the
# API rejects a repo on this branch outright — but checkout_source still needs a
# real repository: checkout_source has no CircleCI-hosted option, so a definition
# always checks out code from somewhere even when its configuration does not come
# from a repository.
data "circleci_project" "circleci_hosted_example" {
  slug = "github/acme/api"
}

data "circleci_github_app_repository" "circleci_hosted_example" {
  org_id    = data.circleci_project.circleci_hosted_example.org_id
  full_name = "acme/api"
}

resource "circleci_pipeline_definition" "circleci_hosted_example" {
  project_id  = data.circleci_project.circleci_hosted_example.id
  name        = "my-circleci-hosted-pipeline"
  description = "Pipeline whose configuration is hosted by CircleCI"

  config_source_provider  = "circleci"
  config_source_file_path = ".circleci/some-pipeline.yml"

  checkout_source_provider         = "github_app"
  checkout_source_repo_external_id = data.circleci_github_app_repository.circleci_hosted_example.external_id
}
