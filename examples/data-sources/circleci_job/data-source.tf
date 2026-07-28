# Fetches a job by project slug and job number. Available on both
# CircleCI Cloud and CircleCI Server.
data "circleci_job" "this" {
  project_slug = "gh/CircleCI-Public/api-preview-docs"
  job_number   = 578122
}

check "job_used_expected_resource_class" {
  assert {
    condition     = data.circleci_job.this.resource_class == "medium"
    error_message = "Job ${data.circleci_job.this.name} ran on ${data.circleci_job.this.resource_class}, not medium."
  }
}
