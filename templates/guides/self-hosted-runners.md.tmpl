---
page_title: "Self-hosted runners"
subcategory: "Guides"
description: |-
  Namespace, resource class, token and agent: standing up CircleCI self-hosted runners with Terraform.
---

# Self-hosted runners

A CircleCI self-hosted runner is a machine you own that claims jobs from
CircleCI. Four objects stand between an empty organization and a job running on
your own hardware, and they must be built in order:

**namespace → resource class → token → agent.**

Terraform manages the first three. The fourth — the runner agent itself — runs on
your machine and is installed there, so Terraform's part is handing it a
credential.

| Object | Terraform type | What it is |
|---|---|---|
| Namespace | `circleci_orb_namespace` | The globally unique prefix that names your resource classes |
| Resource class | `circleci_runner_resource_class` | A pool of interchangeable runners, named `<namespace>/<class>` |
| Token | `circleci_runner_token` or `circleci_ephemeral_runner_token` | The credential an agent authenticates with |
| Agent | — | Installed on the machine; not an API object |

Availability:

| | Cloud | Server |
|---|:--:|:--:|
| `circleci_orb_namespace` | yes | **no** — v3, which Server does not route |
| `circleci_runner_resource_class` | yes | yes (set `runner_host`) |
| `circleci_runner_token` | yes | yes (set `runner_host`) |
| `circleci_ephemeral_runner_token` | yes | yes (set `runner_host`) |

## Before you start

### The runner API lives on its own origin

Runner administration is not served from the same origin as the rest of the API.
On CircleCI Cloud it is `https://runner.circleci.com`, which is the provider's
default. On CircleCI Server your own installation serves it, so **`runner_host`
must be set** or every runner request goes to the wrong host:

```terraform
provider "circleci" {
  host        = "https://circleci.example.com"
  deployment  = "server"
  runner_host = "https://circleci.example.com"
}
```

`runner_host` may also be set with the `CIRCLE_RUNNER_HOST` environment variable.
On Cloud you can leave it alone.

### The organization must have accepted the runner terms of service

Runner onboarding is gated on an organization setting. Until
`is_runner_terms_of_service_accepted` is true, resource classes and tokens cannot
be created for the organization at all:

```terraform
resource "circleci_organization_settings" "runners" {
  org_id                              = var.org_id
  is_runner_terms_of_service_accepted = true
}
```

~> `circleci_organization_settings` is **CircleCI Cloud only** — it is served by
the v3 API, which Server does not route. On Server, accept the terms in the web
application instead.

Only the settings a configuration declares are written, so this resource can
manage that one toggle and leave every other organization setting alone. That
also means a second `circleci_organization_settings` resource elsewhere managing
a disjoint set of toggles is safe.

### The organization is a UUID

Every runner argument that names an organization takes a **UUID**, never a slug.
The provider validates the shape at plan time rather than letting the runner API
answer an opaque 400. Look one up with the `circleci_organization` data source if
you do not have it.

## Step 1 — the namespace

A resource class name is always `<namespace>/<class>`. The namespace half is not
a free-form string you invent inline: it is a real object, owned by your
organization, and it must exist first.

The non-obvious part is **which resource creates it**. There is no
`circleci_runner_namespace`. A runner namespace and an *orb* namespace are the
same object, so the resource is `circleci_orb_namespace`:

```terraform
resource "circleci_orb_namespace" "acme" {
  name   = "acme"
  org_id = var.org_id
}
```

Three consequences worth knowing before you pick a name:

- **The name is claimed across all of CircleCI**, not just within your
  organization. If somebody else has `acme`, you cannot have it.
- **An organization may own exactly one namespace.** If your organization already
  publishes orbs, it already has a namespace, and that is the one your resource
  classes will be named after. Read it with the `circleci_orb_namespace` data
  source rather than creating a second one.
- **Renaming it renames in place.** The namespace keeps its id and its orbs, but
  every `<namespace>/<class>` string and every orb reference elsewhere stops
  resolving. Rename deliberately.

~> **CircleCI Server** cannot create namespaces through this provider —
`circleci_orb_namespace` is a v3 resource and Server does not route v3. Create
the namespace outside Terraform (the CircleCI CLI or the web application), then
reference its name as a string or variable in step 2.

## Step 2 — the resource class

A resource class is a pool of interchangeable runners. Configuration selects one
with `resource_class: <namespace>/<class>`, and any agent registered against it
may claim the job.

```terraform
resource "circleci_runner_resource_class" "builders" {
  org_id         = var.org_id
  resource_class = "${circleci_orb_namespace.acme.name}/builders"
  description    = "Self-hosted x86 build runners"
}
```

Interpolating the namespace resource's `name` rather than hard-coding the prefix
is what makes Terraform order the two correctly — otherwise the resource class
can be planned before the namespace it names exists.

The provider validates that `resource_class` has exactly two slash-separated
parts, so a bare class name fails at plan time.

Changing `resource_class` replaces the resource: the name *is* the identity.
Changing `org_id` does not, because the service derives the owning organization
from the namespace anyway.

### Deleting a resource class that still has tokens

`DELETE` refuses with HTTP 409 while tokens still reference the class. Set
`force_delete` to delete it anyway:

```terraform
resource "circleci_runner_resource_class" "builders" {
  org_id         = var.org_id
  resource_class = "${circleci_orb_namespace.acme.name}/builders"
  description    = "Self-hosted x86 build runners"
  force_delete   = true
}
```

If Terraform manages every token for the class, it will destroy them first and
`force_delete` is unnecessary. It is for classes whose tokens were created
elsewhere.

## Step 3 — the token

A token is what a runner agent authenticates with. The runner API returns its
value **exactly once**, in the response to the request that created it; it can
never be read back. That single fact is what makes the choice between the two
token types matter.

### The ephemeral resource, for a token consumed in this run

```terraform
ephemeral "circleci_ephemeral_runner_token" "builders" {
  org_id         = var.org_id
  resource_class = circleci_runner_resource_class.builders.resource_class
  nickname       = "bootstrap"
}
```

This is the right shape for a credential. An ephemeral resource holds **no
state**: `Open` creates the token, the run uses it, and `Close` deletes it again.
The secret is never written to a state file or to a plan file, so it cannot be
read later by anyone who can read your state, and it does not need rotating
because it does not outlive the run.

That is worth spelling out, because the alternative is genuinely bad. A managed
resource's attributes are persisted, and every subsequent `terraform plan` reads
them back out of state. A runner token in state is a long-lived credential
capable of registering a machine against your resource class, sitting in a file
for as long as the resource exists.

Ephemeral values may only flow into places Terraform guarantees not to persist:
provider configuration, another ephemeral resource, a write-only attribute, a
`provisioner` or `connection` block, or a local value or output that is itself
only used in one of those. That is the constraint, and it is the point — the type
system stops the secret from reaching state. Ephemeral resources need Terraform
1.10 or later.

-> **Best-effort deletion.** If `Close`'s delete fails for a reason other than
the token already being gone, the provider raises a warning naming the token id
rather than silently leaving a possibly-live credential unaccounted for. If you
see that warning, check and delete the token by hand.

### The managed resource, for a token that must outlive the run

```terraform
resource "circleci_runner_token" "builders" {
  org_id         = var.org_id
  resource_class = circleci_runner_resource_class.builders.resource_class
  nickname       = "builders-fleet"
}

output "runner_token" {
  value     = circleci_runner_token.builders.token
  sensitive = true
}
```

Use this when nothing in this Terraform run consumes the token — when it will be
handed to a fleet provisioned entirely outside Terraform later. Accept that the
value lives in state, and treat the state file accordingly.

~> An imported `circleci_runner_token` has an **empty** `token`, because the API
does not return token values after creation. Importing is only useful for
tracking the token's lifecycle; if you need the value, create a new token.

`circleci_runner_tokens` lists a class's tokens, including ones created outside
Terraform — metadata only, never the secrets.

## Step 4 — install the agent

Installing the agent is a step on your machine, not an API call, so Terraform's
job is to get the token there. Follow
[CircleCI's installation instructions](https://circleci.com/docs/runner-installation/)
for your platform; the agent's configuration needs two values from above:

| Agent setting | Where it comes from |
|---|---|
| authentication token | `circleci_ephemeral_runner_token.<name>.token` or `circleci_runner_token.<name>.token` |
| runner name / resource class | `circleci_runner_resource_class.<name>.resource_class` |

The natural Terraform shape is to pass the token to whatever stands the machine
up, in the same apply that created it. With an ephemeral token, that means a
`provisioner` or `connection` block, or a write-only attribute of some other
provider's resource — those are the places an ephemeral value is allowed to go.
Nothing about that path writes the secret to state.

If the machines are provisioned outside Terraform, use `circleci_runner_token`
and move the value into your secrets manager, then delete it from anywhere else
it landed.

## Using the resource class

Once an agent has connected, jobs select the pool by name in
`.circleci/config.yml`:

```yaml
version: 2.1

jobs:
  build:
    machine: true
    resource_class: acme/builders
    steps:
      - checkout
      - run: make build

workflows:
  build:
    jobs:
      - build
```

The value is exactly the `resource_class` string Terraform manages. Cloud
resource classes, by contrast, are a configuration-level choice with no API
object behind them and nothing for this provider to manage.

## Observing a runner fleet

Four data sources, and it matters which of them describe desired state and which
describe live state.

| Data source | Reads | Nature |
|---|---|---|
| `circleci_runner_resource_class` | One class, by `resource_class` name | Configuration |
| `circleci_runner_resource_classes` | Every class in an organization or namespace | Configuration |
| `circleci_runner_tokens` | A class's tokens, metadata only | Configuration |
| `circleci_runners` | The **agents** that have registered | Live state |
| `circleci_runner_task_counts` | `unclaimed_task_count`, `running_task_count` | Live state |

```terraform
data "circleci_runners" "builders" {
  resource_class = circleci_runner_resource_class.builders.resource_class
}

data "circleci_runner_task_counts" "builders" {
  resource_class = circleci_runner_resource_class.builders.resource_class
}
```

`circleci_runners` lists agents, not classes. A runner appears only once its
agent has connected at least once, so the list reflects registration state and
changes without Terraform. `circleci_runner_task_counts` is intended for
autoscaling: a sustained non-zero `unclaimed_task_count` means there is not
enough capacity.

~> Both are point-in-time reads that change constantly. Feed them to something
that acts on the current value; using one to derive a managed resource's
attribute produces a plan that never settles.

`circleci_runners` and `circleci_runner_resource_classes` both require at least
one filter — `resource_class`, `namespace`, or the organization — because the
runner API rejects an unfiltered list.

## A complete example

```terraform
terraform {
  # 1.10 or later, for the ephemeral resource below.
  required_version = ">= 1.10"

  required_providers {
    circleci = {
      source  = "CircleCI-Public/circleci"
      version = "~> 0.4"
    }
  }
}

provider "circleci" {
  # key comes from the CIRCLE_TOKEN environment variable.
  # runner_host defaults to https://runner.circleci.com on CircleCI Cloud.
}

variable "org_id" {
  type        = string
  description = "CircleCI organization UUID. The runner API does not accept a slug."
}

# Runner onboarding is gated on this. CircleCI Cloud only.
resource "circleci_organization_settings" "runners" {
  org_id                              = var.org_id
  is_runner_terms_of_service_accepted = true
}

# A resource class is "<namespace>/<class>", and the namespace is an orb
# namespace: the same object, claimed globally, one per organization.
resource "circleci_orb_namespace" "acme" {
  name   = "acme"
  org_id = var.org_id
}

resource "circleci_runner_resource_class" "builders" {
  org_id         = var.org_id
  resource_class = "${circleci_orb_namespace.acme.name}/builders"
  description    = "Self-hosted x86 build runners"

  depends_on = [circleci_organization_settings.runners]
}

# Never written to state: created on Open, deleted on Close.
ephemeral "circleci_ephemeral_runner_token" "bootstrap" {
  org_id         = var.org_id
  resource_class = circleci_runner_resource_class.builders.resource_class
  nickname       = "bootstrap"
}

data "circleci_runner_task_counts" "builders" {
  resource_class = circleci_runner_resource_class.builders.resource_class
}

output "unclaimed_tasks" {
  value = data.circleci_runner_task_counts.builders.unclaimed_task_count
}
```

The `depends_on` is there because accepting the terms of service is a
precondition the resource class does not otherwise reference, so nothing in the
configuration would order the two.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| Requests hang or 404 on Server | `runner_host` not set to your Server hostname |
| "must be an organization UUID" at plan time | An organization slug was passed where a UUID is required |
| "must be in the format 'namespace/name'" | `resource_class` is missing its namespace prefix |
| Resource class creation is rejected | `is_runner_terms_of_service_accepted` is not yet true for the organization |
| Namespace creation fails with an explicit v3 error | `deployment = "server"`; create the namespace outside Terraform |
| Namespace name already taken | Namespaces are globally unique across CircleCI, and an organization may own only one |
| Resource class delete answers 409 | Tokens still reference it; set `force_delete` or destroy the tokens |
| Imported `circleci_runner_token` has an empty `token` | The API never returns a token value after creation |
| A runner is missing from `circleci_runners` | Its agent has not connected yet; the list is registration state |

## Related

- [Getting started](./getting-started) — provider configuration, projects,
  pipelines and triggers.
- [How CircleCI's objects relate](./object-model) — where runners sit relative to
  everything else, and why a token is an ephemeral resource.
