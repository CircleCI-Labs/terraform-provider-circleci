// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package httpcl

import "context"

// Debug is called once per request with alternating key/value pairs describing
// the call. It exists so this package stays free of any logging dependency:
// upstream (circleci-cli) calls its own internal/iostream here, which is not
// importable, so the provider installs a tflog-backed implementation instead.
//
// See DIVERGENCES.md. Replaced at init by internal/circleci; the default is a
// no-op so httpcl remains usable (and testable) on its own.
var Debug = func(_ context.Context, _ string, _ ...any) {}
