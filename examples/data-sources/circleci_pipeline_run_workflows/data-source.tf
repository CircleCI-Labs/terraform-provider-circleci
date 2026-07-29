# Lists every workflow in a pipeline run. Available on both CircleCI Cloud and
# CircleCI Server.
data "circleci_pipeline_run_workflows" "this" {
  run_id = "1e2d3c4b-5a69-7887-9a0b-1c2d3e4f5061"
}

# This data source is the link that makes the run -> workflow -> job chain
# usable: circleci_workflow_jobs needs a workflow ID, and nothing else in the
# provider could produce one.
data "circleci_workflow_jobs" "each" {
  for_each = {
    for workflow in data.circleci_pipeline_run_workflows.this.workflows :
    workflow.name => workflow.id
  }

  workflow_id = each.value
}

output "circleci_failed_workflows" {
  value = [
    for workflow in data.circleci_pipeline_run_workflows.this.workflows : workflow.name
    if workflow.status == "failed"
  ]
}
