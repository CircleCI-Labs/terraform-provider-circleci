# Lists every job in a workflow's job graph. Available on both CircleCI
# Cloud and CircleCI Server.
data "circleci_workflow_jobs" "this" {
  workflow_id = "1e2d3c4b-5a69-7887-9a0b-1c2d3e4f5061"
}

output "circleci_failed_jobs" {
  value = [
    for job in data.circleci_workflow_jobs.this.jobs : job.name
    if job.status == "failed"
  ]
}
