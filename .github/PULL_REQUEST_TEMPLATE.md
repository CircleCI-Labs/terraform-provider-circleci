<!-- Copyright (c) CircleCI -->
<!-- SPDX-License-Identifier: MPL-2.0 -->

## What this changes

<!-- What a practitioner would notice, and why. If it fixes something, say what was
     wrong rather than only what is now right. -->

## Why

<!-- The part a diff cannot show: what forced this shape, what you tried that did not
     work, or which API behaviour made the obvious approach wrong. If you worked
     something out about the CircleCI API that is not written down anywhere, put it in a
     comment next to the code as well as here. -->

## Checks

- [ ] `task fmt`
- [ ] `task lint` — 0 issues
- [ ] `task test`
- [ ] `task validate-examples`
- [ ] `task ci:diff` — clean (run `task generate-doc` and commit `docs/` if you changed a
      schema description or added a type)

## If this adds a resource or data source

- [ ] Registered in the matching list in `provider.go`
- [ ] Example under `examples/`, using the entry filename `tfplugindocs` expects
- [ ] Template under `templates/` with an `## Availability` table stating CircleCI Cloud
      vs CircleCI Server, and the VCS integrations it works on
- [ ] `ImportState` implemented, and a test proving the plan after import is **empty**
- [ ] Secrets, if any, offered as a write-only attribute so they need not enter state

## If this changes behaviour

- [ ] `CHANGELOG.md` entry
- [ ] A test that would have failed before the change

<!-- Security issues do not belong in a pull request. See SECURITY.md. -->
