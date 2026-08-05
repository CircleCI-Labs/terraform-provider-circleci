<!-- Copyright (c) CircleCI -->
<!-- SPDX-License-Identifier: MPL-2.0 -->

# Security

## Reporting a vulnerability

**Do not open a public issue or pull request for a security vulnerability.**

Report it through CircleCI's coordinated disclosure channels:

- CircleCI's vulnerability disclosure programme: <https://hackerone.com/circleci>
- CircleCI's security page, for anything that does not fit the programme:
  <https://circleci.com/security/>

Please include what you were doing, what happened, and what you expected — and if the
issue is in this provider rather than in the CircleCI API, the provider version and a
minimal Terraform configuration that reproduces it.

## What this provider does with your credentials

Worth understanding before you adopt it, because the risks are not hypothetical:

- **It authenticates with a CircleCI personal API token**, taken from the `key` provider
  attribute or the `CIRCLE_TOKEN` environment variable. The token is sent as a
  `Circle-Token` header and is never written to logs or diagnostics. Prefer the environment
  variable: an attribute value can end up in a checked-in `.tfvars`.

- **Terraform state is not a secret store.** Any attribute you set is written to state in
  plain text, and marking an attribute `Sensitive` only suppresses it from console output —
  it does not encrypt anything. Anyone who can read your state file can read those values.

- **Six resources let you keep secrets out of state entirely**, via write-only attributes
  that Terraform never persists:

  | Resource | Write-only attribute |
  |---|---|
  | `circleci_context_environment_variable` | `value_wo` |
  | `circleci_project_environment_variable` | `value_wo` |
  | `circleci_webhook` | `signing_secret_wo` |
  | `circleci_otel_exporter` | `headers_wo` |
  | `circleci_ios_signing_certificate` | `certificate_blob_wo`, `certificate_password_wo` |
  | `circleci_ios_signing_config` | `provisioning_profiles_wo` |

  Use these in preference to their plain counterparts, sourced from a secret manager
  through an ephemeral resource. The "Managing secrets" guide in the documentation shows
  the pattern. Because a write-only attribute cannot produce a plan diff, each pairs with a
  version counter you increment to rotate the value.

- **Some capabilities use CircleCI API routes that are not publicly documented**, and are
  therefore gated to CircleCI Cloud and could change without notice. Each affected
  resource says so on its own documentation page.

## Fork pull requests and CI

This repository builds on CircleCI. Be aware that if a project is configured to run
builds for pull requests from forks *and* to pass secrets to them, a malicious pull
request can read those secrets. This project's own settings keep those two apart; if you
fork it and enable CI, check the same.
