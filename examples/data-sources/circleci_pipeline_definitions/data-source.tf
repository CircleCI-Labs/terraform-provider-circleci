# Lists every pipeline definition on a project. Unlike the singular
# circleci_pipeline_definition data source, this needs no definition ID, so it
# is the way to discover definitions Terraform did not create.
#
# CircleCI Cloud only: CircleCI Server does not route pipeline-definitions.
data "circleci_pipeline_definitions" "project" {
  project_id = "00000000-0000-0000-0000-000000000000"
}

output "circleci_pipeline_definition_names" {
  value = [for definition in data.circleci_pipeline_definitions.project.pipeline_definitions : definition.name]
}

# Triggers hang off a pipeline definition, so pair the two to walk every trigger
# on the project.
data "circleci_triggers" "all" {
  for_each = {
    for definition in data.circleci_pipeline_definitions.project.pipeline_definitions :
    definition.name => definition.id
  }

  project_id             = data.circleci_pipeline_definitions.project.project_id
  pipeline_definition_id = each.value
}
