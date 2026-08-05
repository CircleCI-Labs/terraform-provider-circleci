---
page_title: "circleci_config_policy_bundle Resource - circleci"
subcategory: ""
description: |-
  Manages an organization's whole bundle of CircleCI config policies.
---

# circleci_config_policy_bundle (Resource)

Manages an organization's whole bundle of CircleCI config policies: the Rego documents evaluated against a pipeline's configuration before it runs.

## Availability

| | |
| --- | --- |
| **CircleCI Cloud** | Yes, on the Scale plan. |
| **CircleCI Server** | Yes on CircleCI Server 4.2 and later. **Reasoned rather than measured**: no CircleCI Server installation has been available to test against, so this is derived from which routes a Server installation exposes. See the CircleCI Server note on the provider index page. |
| **API** | `GET` and `POST /api/v2/owner/{org_id}/context/{policy_context}/policy-bundle`, `GET .../policy-bundle/{name}` |
| **Organization type** | Any. The `{policy_context}` path segment is a *policy* context — a namespace for a bundle of Rego documents — and has nothing to do with a CircleCI context. |
| **Token** | A personal API token belonging to an organization admin. |

## Example Usage

```terraform
data "circleci_organization" "acme" {
  slug = "gh/acme"
}

# One resource owns the whole bundle: every policy for the organization's config
# policy context lives in this map. Reading the Rego from files keeps it
# reviewable and testable with `circleci policy test ./policies`.
resource "circleci_config_policy_bundle" "config" {
  owner_id = data.circleci_organization.acme.id

  policies = {
    "allow-docker.rego"     = file("${path.module}/policies/allow-docker.rego")
    "require-approval.rego" = file("${path.module}/policies/require-approval.rego")
  }
}

# Inline Rego works too, for a single short policy.
#
# Note this is a *different* organization. There is exactly one bundle per
# organization, because `policy_context` accepts only "config" — a second
# circleci_config_policy_bundle for the same owner_id would delete this one's
# policies on every apply, and the other would put them back on the next.
data "circleci_organization" "sandbox" {
  slug = "gh/acme-sandbox"
}

resource "circleci_config_policy_bundle" "sandbox" {
  owner_id = data.circleci_organization.sandbox.id

  policies = {
    "deny-all.rego" = <<-EOT
      package org

      policy_name["deny_all"]

      enable_rule["deny_all"]

      hard_fail["deny_all"]

      deny_all["the sandbox organization does not run pipelines"]
    EOT
  }
}

# Uploading policies does not enforce them; this is the switch that does.
resource "circleci_config_policy_settings" "config" {
  owner_id = data.circleci_organization.acme.id
  enabled  = true

  # Enable enforcement only once the bundle is in place.
  depends_on = [circleci_config_policy_bundle.config]
}
```

The two policies the example reads from disk:

```rego
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
```

```rego
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
```

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `owner_id` (String) Unique identifier (UUID) of the organization that owns the policy bundle. Changing this value forces a new resource to be created.
- `policies` (Map of String) The complete bundle, mapping each policy's name to its Rego source. Names are conventionally filenames ending in `.rego`. Reading policies from disk with `file()` keeps them reviewable:

```terraform
policies = {
  "allow-docker.rego" = file("${path.module}/policies/allow-docker.rego")
}
```

Setting this to `{}` removes every policy from the context. The whole bundle must stay under roughly 2.5 MiB, which the API enforces.

### Optional

- `policy_context` (String) Which policy context the bundle belongs to. `config` is the only accepted value, and the default: it is the context evaluated against pipeline configuration. Changing this value forces a new resource to be created.

~> A policy context is **not** a CircleCI context. It has nothing to do with `circleci_context` or the environment variables that live there: it is a namespace for a bundle of policies. CircleCI's documentation mentions a `custom` context for policies evaluated against caller-supplied data, but no route accepts it — every handler that takes this path segment validates it against the single value `config` and answers 400 for anything else — so it is not offered here.

## Why a bundle and not a policy

The obvious design would be a `circleci_config_policy` resource holding one Rego document. The API makes that impossible.

There is exactly one write route for policies, and it **replaces the entire bundle** for a policy context: `POST .../policy-bundle` takes a map of every policy name to its content, deletes any policy absent from that map, and has no per-policy counterpart. There is no route that adds a single policy, and none that deletes one.

Two per-policy resources pointed at the same organization would therefore each upload a bundle containing only their own document, deleting the other's. Terraform would report both as successfully applied, and on every plan each would find its policy missing and put it back — an apply loop with a different policy winning each time, and enforcement silently missing half the rules in between.

So the bundle is the unit of management. One `circleci_config_policy_bundle` resource per `owner_id` and `policy_context`, holding every policy for that context.

!> **Never declare two `circleci_config_policy_bundle` resources for the same `owner_id` and `policy_context`.** Terraform cannot detect the conflict, and they will clobber each other on every apply.

Keeping the Rego itself in files under `policies/` and referencing them with `file()` gives you the best of both: reviewable, individually testable policy documents (`circleci policy test`), assembled into one bundle by Terraform.

## Removing policies

A policy is deleted by removing its key from `policies`. Setting `policies = {}` empties the context entirely, and destroying the resource does the same — since there is no delete route, an upload of an empty bundle is how a bundle is removed.

Because the whole context is replaced on every apply, drift shows up naturally: a policy added out-of-band is removed on the next apply, and a bundle emptied out-of-band is recreated.

## Enforcement is separate

Uploading a bundle does not evaluate it. Use `circleci_config_policy_settings` to switch evaluation on, and order the two with `depends_on` so enforcement never turns on before the policies land.

## Limits

The whole bundle must stay under roughly **2.5 MiB**; the API answers `413` beyond that.

## Import

Import is supported using `owner_id`, which assumes the default `config` policy context:

```shell
terraform import circleci_config_policy_bundle.config "00000000-0000-0000-0000-000000000000"
```

or `owner_id/policy_context` for any other context:

```shell
terraform import circleci_config_policy_bundle.custom "00000000-0000-0000-0000-000000000000/custom"
```
