# Restricts docker executors to an allow-list of registries: CircleCI's
# convenience images and the organization's own Artifactory. A config policy is the
# only place this can be enforced, because a pull request that adds an unapproved
# image can also edit any check that lives in .circleci/config.yml.
#
# Run `circleci policy test ./policies` before applying: an uploaded policy takes
# effect for the whole organization the moment enforcement is enabled.
package org

policy_name["allow_docker"]

# Prefixes an image may start with. A bare "postgres:14" matches neither and is
# therefore denied — pin it through the mirror instead.
allowed_prefixes := ["cimg/", "acme.jfrog.io/ci/"]

# Every docker image the compiled configuration asks for, paired with the job that
# asked for it, so the denial message can name the job.
images[[job_name, image]] {
	some job_name
	image := input.jobs[job_name].docker[_].image
}

approved(image) {
	startswith(image, allowed_prefixes[_])
}

deny_unapproved_images[reason] {
	[job_name, image] := images[_]
	not approved(image)
	reason := sprintf("job %q uses unapproved docker image %q", [job_name, image])
}

# Uploading the policy does not evaluate it and evaluating it does not block
# anything: enable_rule opts the rule into evaluation, and hard_fail makes a
# violation stop the pipeline rather than only annotate it.
enable_rule["deny_unapproved_images"]

hard_fail["deny_unapproved_images"]
