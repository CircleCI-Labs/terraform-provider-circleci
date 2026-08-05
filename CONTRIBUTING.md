<!-- Copyright (c) CircleCI -->
<!-- SPDX-License-Identifier: MPL-2.0 -->

# Contributing

Thanks for considering a contribution.

Before you spend time on anything substantial, please open an issue describing what you
want to change. That is not a formality: this provider manages live CircleCI
infrastructure, and several attributes that look like obvious improvements are the way
they are because the API behaves in a way that is not obvious. An issue first saves you
from finding that out in review.

## Getting set up

You need Go (the version in `go.mod` — currently 1.26), a `terraform` binary on your
`PATH` for the example-validation and acceptance tests, and
[Task](https://taskfile.dev) to run the project's commands.

```sh
task mod-download
task build
task test:fast          # ~510 tests, about two seconds, no terraform binary needed
```

`task test:fast` covers `internal/circleci` and `internal/httpcl` — the API client and its
HTTP layer. It is the loop to work in.

## Before you open a pull request

```sh
task fmt                # gosimports; run this first or the linter will complain
task lint               # golangci-lint, must be 0 issues
task test               # the full suite: ~1700 tests, about 10 minutes
task validate-examples  # terraform validate over every directory under examples/
task ci:diff            # fails if generated files are stale — see below
```

`task ci:diff` is the one that catches people out. `docs/` is **generated** by
`tfplugindocs` from `templates/` and `examples/`, and it is committed. If you change a
schema description, or add a resource, you must run `task generate-doc` and commit the
result. Do not hand-edit anything under `docs/` — it will be overwritten.

## What the guard tests are for

A few tests exist to enforce properties rather than behaviour, and they will fail your
build for reasons that are not bugs in your code:

| Test | What it wants |
|---|---|
| `TestEveryConstructorIsRegistered` | A new resource or data source must be added to the matching list in `provider.go`. It finds constructors by parsing the package, so it cannot be fooled |
| `TestEveryRegisteredTypeHasAnExample` | Every registered type needs a directory under `examples/` with the entry filename `tfplugindocs` looks for (`resource.tf`, `data-source.tf`, `ephemeral-resource.tf` or `function.tf`) |
| `TestEveryExampleFileIsIncludedByATemplate` | An example nothing references is dead weight; wire it into the type's template |
| `TestGatedTypesDocumentThatServerIsUnavailable` | A type that refuses `deployment = "server"` must say so on its own documentation page |
| `TestNoInternalReferences` and `TestNoGoFileCitationIsForeign` | Comments may explain what the API does; they may not cite a file or component a reader cannot go and look at. See the header of `internal/provider/no_internal_references_test.go` |

## Adding a resource or data source

The shape to follow, in order:

1. The client method in `internal/circleci/`, with a test using an `httptest` fake.
2. The resource or data source in `internal/provider/`.
3. Registration in `provider.go`.
4. An example under `examples/`.
5. A template under `templates/`, including an `## Availability` table stating whether it
   works on CircleCI Cloud, on CircleCI Server, and on which VCS integrations.
6. `task generate-doc`, and commit the generated page.

Two things about tests, learned the hard way here:

- **Derive fakes from what the API actually sends**, not from what you expect it to send.
  Several bugs in this provider's history survived a full green suite because the mock and
  the client were wrong in the same way. If you cannot verify a field name, say so in a
  comment rather than guessing quietly.
- **A test that asserts a value the test itself just set proves nothing.** If a test cannot
  fail, it is worse than absent, because it looks like coverage.

## Acceptance tests

Tests named `TestAcc…` talk to a real CircleCI organization. They skip by name when their
credentials are absent, so a clean checkout is green without any setup. See `TESTING.md`
for the environment variables and what each one is for.

Do not point them at an organization you care about. They create and delete contexts,
projects, groups and runner resource classes.

## Commit messages and pull requests

Describe *why*, not just what. A diff shows what changed; it cannot show what you tried
that did not work, or which API behaviour forced an odd-looking decision. If you worked
something out about the API that is not written down anywhere, put it in a comment next to
the code that depends on it — that is the most valuable thing you can leave behind.

If your change alters behaviour a practitioner could notice, add a `CHANGELOG.md` entry.

## Security

Do not report security issues through a public issue or pull request. See
[`SECURITY.md`](./SECURITY.md).
