# Requires every workflow job that deploys to production to wait on a manual
# approval job. Without a policy this is unenforceable: the commit that removes the
# approval gate is the same commit that would need to be reviewed for removing it.
package org

policy_name["require_approval"]

# Jobs whose name marks them as touching production. Matching on the name is crude,
# but the compiled configuration is what a policy sees, and a naming convention is
# cheaper to hold to than a job-level annotation.
production(job_name) {
	startswith(job_name, "deploy-production")
}

# A workflow entry is either a bare string or a single-key object carrying
# `requires`, `context` and friends, so both shapes have to be handled.
workflow_jobs[[workflow_name, job_name, job]] {
	job_name := input.workflows[workflow_name].jobs[_]
	is_string(job_name)
	job := {}
}

workflow_jobs[[workflow_name, job_name, job]] {
	entry := input.workflows[workflow_name].jobs[_]
	is_object(entry)
	some job_name
	job := entry[job_name]
}

# An approval job is one declared with `type: approval` in the same workflow.
approval_job(workflow_name, job_name) {
	[workflow_name, job_name, job] := workflow_jobs[_]
	job.type == "approval"
}

require_production_approval[reason] {
	[workflow_name, job_name, job] := workflow_jobs[_]
	production(job_name)
	not gated(workflow_name, job)
	reason := sprintf(
		"workflow %q runs %q with no approval job in its requires",
		[workflow_name, job_name],
	)
}

gated(workflow_name, job) {
	approval_job(workflow_name, job.requires[_])
}

enable_rule["require_production_approval"]

hard_fail["require_production_approval"]
