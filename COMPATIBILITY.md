<!-- Copyright (c) CircleCI -->
<!-- SPDX-License-Identifier: MPL-2.0 -->

# Compatibility notes

The evidence behind every `yes` / `no` / `?` in the compatibility tables in
[`README.md`](./README.md#compatibility) and in the [provider index page's Cloud/Server
summary](https://registry.terraform.io/providers/CircleCI-Labs/circleci/latest/docs). Each
cell that needs more than its own word links here.

This page is organised by **why** a limitation exists, not by which resource it affects —
the cause usually applies to more than one row, and knowing the cause is what lets you
predict a cell nobody has filled in yet. If you only want to know whether a specific
resource works on a specific integration, the tables you followed a link from are the
answer; this page is where that answer comes from.

Nothing on this page changes a verdict. If your installation disagrees with a cell, that
is a bug report against the table, not against this page.

## Organization types

CircleCI has two organization shapes, and a fair amount of what differs between VCS
integrations actually traces back to which shape the organization is, not to the VCS
itself.

- A **classic** organization — slug `gh/<org>` or `bb/<org>` — is `github` or `bitbucket`
  type, backed directly by a GitHub OAuth or Bitbucket Cloud connection.
- A **standalone** organization — slug `circleci/<uuid>` — is `circleci` type. GitHub App,
  GitLab.com, GitLab self-managed and GitHub Enterprise Server connections all live under
  standalone organizations.

A CircleCI Server installation is always `github` type.

### Groups and GitHub App discovery need a standalone organization

`circleci_group`, `circleci_project_group` and the GitHub App discovery data sources all
require a `circleci` type (standalone) organization, so `github` and `bitbucket`
organizations cannot use them even on CircleCI Cloud. The provider holds an organization
UUID rather than a slug and cannot tell the type without an extra lookup, so it rules out
`deployment = "server"` outright — a Server installation is always `github` type — and
leaves the Cloud half of the check to the API itself. GitHub App discovery additionally
needs an actual GitHub App installation, so a standalone organization connected to GitLab
has nothing to report either.

### CircleCI has two unrelated concepts called a group

*VCS security groups* are documented as available for `github` type organizations only
and require the GitHub OAuth integration. *CircleCI RBAC groups* (`circleci_group`)
require a `circleci` type organization. Those two requirements are mutually exclusive, and
the API does not document which one `circleci_context_restriction`'s
`restriction_type = "group"` expects. Verify against your own organization before relying
on it.

### GitHub App discovery has nothing to report without an actual GitHub App connection

`circleci_github_app_installation`, `circleci_github_app_repository` and
`circleci_github_app_repositories` read the organization's GitHub App installation, so an
organization that has none has nothing to report: any `github`/`bitbucket` (OAuth)
organization, and any standalone organization connected to GitLab rather than GitHub. The
provider additionally requires a `circleci` type organization, which is why CircleCI
Server is a definite **no**.

### Whether GitHub Enterprise Server reports as a GitHub App installation is unchecked

A GitHub Enterprise Server connection is also a GitHub App connection under the covers,
but whether the GitHub App discovery routes actually report it has not been checked.

### GitLab, GitHub App and GHES projects use a UUID slug, not a name

These three integrations address a project as `circleci/<orgUUID>/<projectUUID>` rather
than `<provider>/<org>/<project>`. The project settings endpoint's `provider` path segment
has no `gitlab` value at all — GitLab projects are addressed the same way GitHub App and
GHES projects are.

### Classic organizations still need one v1.1 API call to follow a project

Project creation issues a `POST /api/v1.1/project/.../follow`, because no v2 route follows
a project and an unfollowed project never runs. Only classic (`github`/`bitbucket`)
organizations need it — a standalone `circleci/<uuid>` organization follows the project as
part of creating it. This is the provider's one remaining v1.1 dependency, and v1.1 is the
only API version with an active deprecation initiative. (An earlier internal HTTP client
sent this call to a hardcoded `https://circleci.com` regardless of the configured host, so
it could not work against CircleCI Server; the provider's own client honours `host`
instead.)

### Creating an organization on CircleCI Server is untested

*Reading* an organization is a plain v2 route, served directly by CircleCI's API, and
should work on Server. *Creating* one is a different question: a Server installation's
organizations come from its VCS and are always `github` type, so a create there likely has
nothing to do. Untested either way. Destroy deliberately mirrors create either way too — an
adopted organization is released from state, and only a genuinely created one is deleted.

## What each VCS integration cannot do

### Checkout keys need a VCS that checks out over SSH

Checkout keys are not available to projects that use the GitHub App, GitHub Enterprise
Server, GitLab.com or GitLab self-managed — those integrations check out over HTTPS and
need no keys. GitHub OAuth and Bitbucket Cloud projects can use them. There is a
read/write asymmetry worth knowing: only `POST` carries the restriction, so a `GET` on
GitLab succeeds and returns the auto-provisioned key — a `circleci_checkout_key` resource
reads fine on an unsupported integration and fails only on create. `user-key` additionally
requires a *user* API token rather than a project token.

### Bitbucket cannot restrict a context by group

"Bitbucket repositories do not provide an API that allows CircleCI contexts to be
restricted." `circleci_context_restriction` with `type = "group"` is therefore
unavailable on Bitbucket Cloud specifically, distinct from the broader ambiguity about
what "group" means — see "CircleCI has two unrelated concepts called a group" above.

### GitHub OAuth, GitLab and Bitbucket Cloud have no stored pipeline definition

For these three integrations there is no stored pipeline-definition entity: CircleCI
generates a synthetic UUID from the project ID instead. `circleci_pipeline_definition`
can read that synthetic definition but never create or update it.

### The getting started guide skips the pipeline definition step where there is nothing to create

Framed for the getting-started guide specifically: GitHub OAuth, GitLab and Bitbucket
Cloud projects have no stored pipeline-definition object for `circleci_pipeline_definition`
to create — CircleCI derives a synthetic one from the project's ID, the same fact as
"GitHub OAuth, GitLab and Bitbucket Cloud have no stored pipeline definition" above. The
project builds from the `.circleci/config.yml` in the branch that was pushed, exactly as
it did before Terraform was involved, and `circleci_pipeline_definition` can still read
the synthetic definition.

### GitHub OAuth triggers use the same endpoint under a narrower contract

`circleci_trigger` reaches the same endpoint for GitHub OAuth as for GitHub App, but
`event_preset` is required and limited to `all-pushes` or `only-build-prs`, and `disabled`
is unsupported.

### The getting started guide's GitHub OAuth trigger step uses a narrower contract

The same narrowing as "GitHub OAuth triggers use the same endpoint under a narrower
contract" above, called out separately because the getting-started guide's own trigger
step (step 6) walks through it directly: `event_preset` is required and limited to
`all-pushes` or `only-build-prs`, and `disabled` is unsupported.

### Bitbucket Cloud triggers have no schedule event source

Bitbucket Cloud supports schedule triggers as a product feature, but has no corresponding
value in the trigger API's `event_source.provider` enum, so a `circleci_trigger` with
`event_source_provider = "schedule"` cannot be created against a Bitbucket Cloud project.

### CircleCI's own documentation disagrees with itself about GitLab and fork pull requests

Whether `build_fork_prs` / `forks_receive_secret_env_vars` are supported on GitLab.com is
unresolved: CircleCI's VCS overview says GitLab.com supports it, while the pipelines and
open-source pages say it is unsupported for GitLab and GitHub App pipelines. This
provider's `?` for that cell reflects the contradiction rather than a gap in reading the
documentation.

### The URL orb allow list cannot authenticate against GitLab

`circleci_url_orb_allow_list_entry` entries can be managed against GitLab, but the `auth`
value cannot be GitLab: "GitLab authentication is not currently supported. Only use public
GitLab repositories with the `None` auth type."

### set_github_status controls VCS status updates everywhere, not just GitHub Checks

The `set_github_status` project setting is misleadingly named. It really means "VCS status
updates" and also controls Bitbucket and GitLab commit statuses. It is *not* GitHub
Checks, which is GitHub-only and has no corresponding setting.

## CircleCI Server: routing and versions

The API version alone does not decide what is available on CircleCI Server — whether a
Server installation's gateway forwards the route does. A path is forwarded to its backend,
or it is not, independent of whether the path is v2 or v3, and both combinations of
"available" and "not available" occur for both versions. "The route is v2" is therefore
never sufficient evidence of availability on its own, and the subsections below exist
because it is easy to assume otherwise.

### CircleCI Server does not route the v3 API at all

Organization settings, orb namespaces/orbs/orb versions, notification channels and
preferences, iOS code signing, deploy and release tracking, and the execution catalog are
all v3 and all unavailable on Server for this one reason. Set `deployment = "server"` and
these resources report an explicit error rather than a confusing HTTP 404.

### Pipeline definitions and triggers are v2 routes Server still does not forward

Unavailable on Server for a *different* reason than
[CircleCI Server does not route the v3 API at all](#circleci-server-does-not-route-the-v3-api-at-all),
worth distinguishing because it changes what would fix it. `pipeline-definitions` and
`triggers` are **v2** routes, not v3 — but a Server installation's gateway does not
forward them at all. v3 arriving on Server would therefore not make these work; the
missing piece is the route, not the API version. Set `deployment = "server"` and these
resources fail at plan time with an explicit error.

### Some v2 routes are owned by a backend a Server installation may not forward

`circleci_url_orb_allow_list_entry` and `circleci_otel_exporter` are owned by a separate
backend behind the API gateway, of which a Server installation forwards only a subset of
paths. The provider does not gate these, so it will attempt the request against a Server
installation regardless; whether it succeeds is unknown. Being v2 is not evidence on its
own here either — see
[Pipeline definitions and triggers are v2 routes Server still does not forward](#pipeline-definitions-and-triggers-are-v2-routes-server-still-does-not-forward).

### The v2 API is forwarded on CircleCI Server by default

Pipeline runs, workflows, jobs, users and user collaborations are served by the
long-standing v2 API, which a Server installation's gateway forwards `/api` to by default.
That is evidence from the route configuration rather than merely "it is v2" — but it is
still reasoned, not measured.

### Context owners on CircleCI Server are identified by account id, not slug

On CircleCI Server a context owner must be given as `owner.type: "account"` with an id;
owner slugs are not supported there. Context names must also be unique across every
organization in the account, not just within one organization.

### Project group grants are not exposed on CircleCI Server

The `circleci_project_group` route sits under a different path prefix than
`circleci_group`, and a CircleCI Server installation does not expose that prefix.

### Self-hosted runners on Server need runner_host pointed at the installation

Set `runner_host` to your Server installation: Server serves the runner API itself rather
than delegating to `runner.circleci.com`.

### Usage export and ephemeral runner tokens work the same on Cloud and Server

Usage export is a v2 route served on both Cloud and Server. Ephemeral runner tokens follow
`runner_host`, which on Server is the installation itself.

### Insights on Server depends on whether that installation runs the Insights service

The insights routes are v2, but Insights is a separate service that a given Server
installation may not run. The provider does not block `deployment = "server"`; without
Insights the request simply fails as not-found.

### Deploy and release tracking routes are absent from Server's gateway

Deploy and release tracking is not available on CircleCI Server, and its routes are absent
from a Server installation's gateway entirely — not merely ungated. Set
`deployment = "server"` and these data sources report an explicit error.

## Plan requirements

### Config policies need the Scale plan on Cloud, or CircleCI Server 4.2 or later

`circleci_config_policy_bundle` and `circleci_config_policy_settings` require the Scale
plan on CircleCI Cloud, or CircleCI Server 4.2 and later.

### OIDC custom claims need CircleCI Server 4.4 or later, and are unavailable air-gapped

`circleci_oidc_custom_claims` requires CircleCI Server 4.4 and later, and is not available
in air-gapped installations.

### Audit log streaming is gated on the Scale plan out of caution, not confirmed routing

`circleci_audit_log_config` requires the Scale plan, a CircleCI Cloud billing concept a
Server installation has no equivalent of. This is the provider's one Cloud-only gate that
is **not** about the v3 API: the routes are v2 and might well be routed on Server, but
nobody has been able to check, so `deployment = "server"` is refused out of caution rather
than from a confirmed absent route. If that turns out to be wrong for some installation,
the fix is to drop the gate.

### Spend budgets are served on an undocumented, fixed address

`circleci_budget` is served on a CircleCI origin that carries no published specification,
at a fixed address independent of `host` and `deployment`. A Server installation's gateway
never routes to it, so `deployment = "server"` reports an explicit error rather than a
confusing failure.

## How well evidenced each verdict is

**Every column in the compatibility tables is reasoned, not measured.** There is no
per-integration CI matrix. `TESTING.md` enumerates the seven integration types this
provider is meant to work against and what each one is for; the acceptance tests whose
result depends on `CIRCLECI_TEST_VCS_TYPE` skip by name when it names an unsupported
integration (see "What a run covers" in `TESTING.md`), but a single CI job still only ever
configures one integration at a time, so the suite has never actually been *run* against
most of them. The cells in the tables come from CircleCI's own documentation and from
which routes each deployment actually serves, not from a test result.

**GitHub Enterprise Server and Bitbucket Cloud are the least evidenced VCS integrations.**
The `github_server` provider value in `circleci_pipeline_definition` and `circleci_trigger`
exists for GitHub Enterprise Server and nothing else, and has never been exercised.
Bitbucket Cloud is exercised no more than GHES — its documented differences, such as no
`group` context restrictions and no `schedule` value in the trigger event-source enum, are
read off documentation rather than observed.

**CircleCI Server is the least evidenced deployment of all.** No installation has been
available to test against at all. "The route is v2" is not sufficient evidence on its own
either way: `circleci_pipeline_definition` is a v2 route and still unavailable there — see
[Pipeline definitions and triggers are v2 routes Server still does not forward](#pipeline-definitions-and-triggers-are-v2-routes-server-still-does-not-forward).

Nothing on this page is *known* to be wrong for any integration or for CircleCI Server.
Please open an issue if your installation disagrees with a cell — that is how these get
upgraded from reasoned to measured.

## Other provider notes

### Six resources accept a secret as write-only and never store it

Six resources accept their secret as a write-only argument that is never persisted to
state: `circleci_context_environment_variable` and `circleci_project_environment_variable`
(`value_wo`), `circleci_webhook` (`signing_secret_wo`), `circleci_otel_exporter`
(`headers_wo`), `circleci_ios_signing_certificate` (`certificate_blob_wo`,
`certificate_password_wo`) and `circleci_ios_signing_config`
(`provisioning_profiles_wo`). See the "Managing secrets" guide.
