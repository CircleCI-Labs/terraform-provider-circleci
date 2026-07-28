# Lists every pipeline definition on a project.
# CircleCI Cloud only: CircleCI Server does not route pipeline-definitions.
data "circleci_pipelines" "project" {
  project_id = "00000000-0000-0000-0000-000000000000"
}

output "circleci_pipeline_names" {
  value = [for pipeline in data.circleci_pipelines.project.pipelines : pipeline.name]
}

# The triggers data source is scoped by pipeline definition, so pair the two to
# walk every trigger on the project.
data "circleci_triggers" "all" {
  for_each = {
    for pipeline in data.circleci_pipelines.project.pipelines : pipeline.name => pipeline.id
  }

  project_id  = data.circleci_pipelines.project.project_id
  pipeline_id = each.value
}
