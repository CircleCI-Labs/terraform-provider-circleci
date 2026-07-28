// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"strconv"

	"terraform-provider-circleci/internal/httpcl"
)

// RequestOption customizes a single API request. It re-exports the httpcl
// option type so that internal/provider never needs to import httpcl directly.
type RequestOption = func(*httpcl.Request)

// RouteParams supplies values for the %s placeholders in a route. Values are
// path-escaped, so callers may pass raw names, slugs and fingerprints.
func RouteParams(v ...any) RequestOption {
	return httpcl.RouteParams(v...)
}

// Query sets a query parameter.
func Query(key, val string) RequestOption {
	return httpcl.QueryParam(key, val)
}

// OptionalQuery sets a query parameter, omitting it entirely when val is empty.
func OptionalQuery(key, val string) RequestOption {
	return httpcl.OptionalQueryParam(key, val)
}

// Filter sets a v3 filter[key]=val query parameter, omitting it when val is
// empty. v3 collections are always scoped by filters rather than nested paths.
func Filter(key, val string) RequestOption {
	return httpcl.OptionalQueryParam("filter["+key+"]", val)
}

// PageLimit sets the v3 page[limit] query parameter. A limit of zero or less is
// omitted so the server default applies.
func PageLimit(n int) RequestOption {
	if n <= 0 {
		return func(*httpcl.Request) {}
	}

	return httpcl.QueryParam("page[limit]", strconv.Itoa(n))
}

// PageCursor sets the v3 page[cursor] query parameter. An empty cursor is
// omitted, requesting the first page.
func PageCursor(cursor string) RequestOption {
	return httpcl.OptionalQueryParam("page[cursor]", cursor)
}

// PageToken sets the v2 page-token query parameter, omitting it when empty. v2
// paginates with an opaque page-token rather than v3's page[cursor].
func PageToken(token string) RequestOption {
	return httpcl.OptionalQueryParam("page-token", token)
}
