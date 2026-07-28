# Fetches a workflow by id. Available on both CircleCI Cloud and
# CircleCI Server.
data "circleci_workflow" "this" {
  id = "1e2d3c4b-5a69-7887-9a0b-1c2d3e4f5061"
}

check "workflow_succeeded" {
  assert {
    condition     = contains(["success", "running", "on_hold"], data.circleci_workflow.this.status)
    error_message = "Workflow ${data.circleci_workflow.this.name} is in status ${data.circleci_workflow.this.status}."
  }
}
