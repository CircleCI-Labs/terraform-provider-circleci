# internal/httpcl — vendored code

This package is **vendored, not authored here.** It is copied from
[`CircleCI-Public/circleci-cli`](https://github.com/CircleCI-Public/circleci-cli)
`internal/httpcl`, which is MIT licensed (that repo in turn copied it from
`chunk-cli`, and carries a `TODO: extract to a shared module`).

It is vendored rather than imported because the upstream package lives under
`internal/`, so Go's internal-package rule makes it unimportable.

## Why this package rather than circleci-sdk-go

`circleci-sdk-go` bakes the API version into the client's base URL, so one client
can only ever speak one API version. This package takes a bare origin and lets
the caller prefix each route, which is what allows a single client to serve v3 on
CircleCI Cloud and v2 on CircleCI Server.

It also returns a typed `*HTTPError` carrying the status code and raw body.
`circleci-sdk-go` collapses every failure into `fmt.Errorf("%s: %s", status,
body)`, which forced the provider to detect deleted resources with
`strings.Contains(err.Error(), "404")` — a check that also matched 5xx responses
whose bodies happened to contain "404", silently dropping live resources from
Terraform state.

## Rules

**Do not edit these files to add provider features.** Everything
provider-specific belongs in `internal/circleci`, which composes on top. Keeping
this package identical to upstream is what makes it replaceable in one step if
CircleCI publishes a shared client module.

`internal/provider` must not import this package directly — only
`internal/circleci`.

## Divergences from upstream

Kept to the minimum needed to compile and to log usefully. If you re-vendor,
these are the only changes to reapply.

| File | Change | Why |
|---|---|---|
| `client.go` | Dropped the `internal/iostream` import; `iostream.DebugContext(...)` → `Debug(...)` | `iostream` is CLI-internal and unimportable. |
| `debug.go` | **Added** (not upstream) | Declares the `Debug` hook replacing `iostream`. `internal/circleci` points it at `tflog` in an `init`. |
| `client_test.go` | Import path rewritten; `iostream.Testing(ctx)` → `context.Background()` | Same reason; the tests otherwise run unmodified and pass. |

Upstream revision vendored: `CircleCI-Public/circleci-cli@main`, 2026-07-27.

## Note on Go version

`error.go` uses `errors.AsType`, added in the Go 1.26 standard library. This is
why the module's `go` directive is `1.26.0`. CI already runs `cimg/go:1.26.1`.
