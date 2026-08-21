// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"errors"
	"fmt"
	"net/url"
)

// ProjectEnvironmentVariable is an environment variable set on a project, as
// returned by GET /api/v2/project/{project-slug}/envvar.
//
// Value is always masked: the API answers with four "x" characters followed by
// *up to* the last four characters of the real value, matching what the CircleCI
// web UI displays. The tail is shortened for a short value — the API
// reveals min(4, len/2) characters, rounded down — so a five-character value
// reveals two and a one-character value reveals none, leaving a bare "xxxx".
// The configured value can never be read back, so this is only useful for
// confirming which variable is set, not what it is set to.
//
// CreatedAt is kept as the string the API sent (an RFC 3339 timestamp) so that
// it round-trips into Terraform state exactly as received. It is null for
// variables created before CircleCI began recording the timestamp.
type ProjectEnvironmentVariable struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	CreatedAt string `json:"created_at"`
}

// projectEnvVarRoute renders the envvar collection route for a project.
//
// It reuses checkoutKeyProjectPath, the slug escaper written for the
// checkout-key routes, because the constraint is identical: RouteParams
// percent-escapes each value as a single path segment, which would turn the
// slug's separators into %2F, and encoded separators are rejected by
// intermediate proxies and do not match the route on CircleCI Server. Escaping
// each segment individually keeps the separators literal.
//
// Because the returned route already contains percent escapes it must not be
// passed through fmt.Sprintf afterwards, so callers pass no RouteParams.
func projectEnvVarRoute(projectSlug string) (string, error) {
	slug, err := checkoutKeyProjectPath(projectSlug)
	if err != nil {
		return "", err
	}

	return "/project/" + slug + "/envvar", nil
}

// ListProjectEnvironmentVariables returns every environment variable set on a
// project, following pagination to the last page. Values are masked; see
// ProjectEnvironmentVariable.
//
// The result is nil when the project has no environment variables.
func (c *Client) ListProjectEnvironmentVariables(ctx context.Context, projectSlug string) ([]ProjectEnvironmentVariable, error) {
	route, err := projectEnvVarRoute(projectSlug)
	if err != nil {
		return nil, err
	}

	return DrainV2(ctx, func(ctx context.Context, pageToken string) (PaginatedResponse[ProjectEnvironmentVariable], error) {
		var page PaginatedResponse[ProjectEnvironmentVariable]
		err := c.GetV2(ctx, route, &page, PageToken(pageToken))

		return page, err
	})
}

// projectEnvVarNameRoute renders the route addressing one environment
// variable by name.
//
// Like projectEnvVarRoute, the project slug is escaped and interpolated
// directly rather than through RouteParams, and for the same reason: this
// route (unlike circleci_organization's) does not accept a percent-encoded
// separator in place of the slug's literal "/". The name is escaped the same
// way, appended after the slug is already escaped, so it must not be passed
// through fmt.Sprintf either.
//
// A name that is exactly "." or ".." is rejected outright, before it is
// escaped — see isDotSegment in project.go. The slug's own segments are
// already checked inside projectEnvVarRoute, but that check does not extend
// to this trailing segment, which is appended after the slug is already
// escaped.
func projectEnvVarNameRoute(projectSlug, name string) (string, error) {
	route, err := projectEnvVarRoute(projectSlug)
	if err != nil {
		return "", err
	}

	if isDotSegment(name) {
		return "", fmt.Errorf("circleci: environment variable name %q is not allowed", name)
	}

	return route + "/" + url.PathEscape(name), nil
}

// ProjectEnvironmentVariableInput is the create body for a project
// environment variable: {name, value}.
//
// It is a separate type from ProjectEnvironmentVariable, for the same reason
// WebhookInput is separate from Webhook (see webhook.go): the read type's
// Value field only ever holds the masked form — four "x" characters followed
// by the value's last four characters — and reusing it for writes is exactly
// how a masked value ends up being written back as a literal one.
type ProjectEnvironmentVariableInput struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// CreateProjectEnvironmentVariable creates a project environment variable and
// returns it as stored.
//
// Even the create response's Value comes back already masked, the same way
// a read does. The literal value configured is never echoed back by any
// route, not even the one that just set it.
func (c *Client) CreateProjectEnvironmentVariable(
	ctx context.Context,
	projectSlug string,
	input ProjectEnvironmentVariableInput,
) (*ProjectEnvironmentVariable, error) {
	route, err := projectEnvVarRoute(projectSlug)
	if err != nil {
		return nil, err
	}

	var created ProjectEnvironmentVariable
	if err := c.PostV2(ctx, route, input, &created); err != nil {
		return nil, err
	}

	return &created, nil
}

// GetProjectEnvironmentVariable returns one environment variable by name, with
// Value masked (see ProjectEnvironmentVariable). A missing variable is
// reported as an error satisfying IsNotFound.
func (c *Client) GetProjectEnvironmentVariable(ctx context.Context, projectSlug, name string) (*ProjectEnvironmentVariable, error) {
	route, err := projectEnvVarNameRoute(projectSlug, name)
	if err != nil {
		return nil, err
	}

	var found ProjectEnvironmentVariable
	if err := c.GetV2(ctx, route, &found); err != nil {
		return nil, err
	}

	return &found, nil
}

// DeleteProjectEnvironmentVariable deletes a project environment variable by
// name.
func (c *Client) DeleteProjectEnvironmentVariable(ctx context.Context, projectSlug, name string) error {
	route, err := projectEnvVarNameRoute(projectSlug, name)
	if err != nil {
		return err
	}

	return c.DeleteV2(ctx, route)
}

// Context environment variable routes.
//
// Shapes and semantics here follow the API's own handlers and
// github.com/CircleCI-Public/circleci-cli's internal/apiclient/context.go
// (MIT), which agree on routes and field names.
//
// The write route is a PUT to a named item, not a POST to the collection: it is
// an upsert that atomically overwrites whatever value was there, rather than a
// create that would need a separate update route. There is no such update
// route — PUT is both.
const (
	contextEnvVarsRoute = "/context/%s/environment-variable"
	contextEnvVarRoute  = "/context/%s/environment-variable/%s"
)

// ContextEnvironmentVariable is an environment variable set on a context, as
// GET .../environment-variable returns it.
//
// TruncatedValue is the only trace of the value the API ever discloses, and it
// is NOT the same shape as ProjectEnvironmentVariable.Value. It carries the tail
// of the value on its own, with no mask prefix: the API reveals the last
// min(4, floor(len/2)) characters. So "FOOBARBAZ" reports "RBAZ", "FOOBAR"
// reports "BAR", "FOO" reports "O", and an empty value reports "". A project
// environment variable's masked value, by contrast, prefixes the same tail with
// "xxxx" — the two conventions look alike and are not, so do not reuse one
// helper for both.
//
// The formula is documented nowhere in the published spec, so it was measured:
// values of length 1 through 12 were written to a real context and the list route
// read back. Every length matched min(4, floor(len/2)) exactly — 1→"", 2→1 char,
// 3→1, 4→2, 5→2, 6→3, 7→3, 8→4, and 4 from 8 upwards. It is worth recording
// because it is the only thing that distinguishes two similarly named variables
// in a UI, and because half a short value is a real disclosure: a two-character
// value gives up one of its two characters.
//
// It is empty on the value returned by UpsertContextEnvironmentVariable, because
// the PUT route's own response struct carries no such field at all — only
// the list route does.
//
// It must not be used to detect that a value changed. Rotating a secret while
// keeping its last four characters leaves TruncatedValue identical, so any
// comparison of it silently misses that class of rotation. UpdatedAt is the
// usable signal — but note that it bumps on every write, including one that
// stores a byte-identical value, so it means "last written", not "last
// changed". Only a comparison against a timestamp the caller recorded after
// its own write says anything (see contextEnvironmentVariableResource.Read).
type ContextEnvironmentVariable struct {
	Variable       string `json:"variable"`
	ContextID      string `json:"context_id"`
	TruncatedValue string `json:"truncated_value"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// ContextEnvironmentVariableInput is the PUT upsert body.
//
// "value" is the only key the route reads, and the spelling is load-bearing in a
// way no status code will tell you about. Measured against a real context:
//
//	{"value":"ABCDEFGHIJKL"} -> 200, stored, truncated_value "IJKL"
//	{"val":"ABCDEFGHIJKL"}   -> 200, stored EMPTY, truncated_value ""
//	{"Value":"ABCDEFGHIJKL"} -> 200, stored EMPTY, truncated_value ""
//	{}                       -> 400 {"message":"Invalid body."}
//
// An empty object is the one body the route rejects. Any other key satisfies it,
// and the variable is then created with an empty value while the response looks
// exactly like a success — same status, same body, created_at and updated_at both
// set. Nothing downstream can catch it: the value is never returned on any route,
// and truncated_value is "" both for a dropped value and for a deliberately empty
// one. A CI job would read the variable as set-but-empty.
//
// This is the same silent-drop shape as the webhook signing-secret defect (see
// WebhookInput), which shipped because nothing pinned the request body. The key
// is pinned by TestContextEnvironmentVariableInputMarshalsValueKey, and the fake
// in internal/provider fails any test whose PUT body lacks it.
type ContextEnvironmentVariableInput struct {
	Value string `json:"value"`
}

// contextEnvVarPageSize is the number of environment variables the list route
// puts on a page.
//
// It is fixed by the service and no request parameter changes it: page-size,
// limit, page[limit] and page-limit were each sent and each answered 200 with a
// full page, measured against a real context holding more than this many.
const contextEnvVarPageSize = 100

// ContextEnvVarsTruncatedError reports that a context holds more environment
// variables than the list route will disclose, so the collection could not be
// listed completely.
//
// The measured pagination contract of GET /context/{id}/environment-variable,
// established by filling a real context past the cap and probing it, rather than
// read off the published spec (which documents none of this):
//
//   - The body is {"items": [...], "next_page_token": string|null}, items sorted
//     by name. There is no total, and no other count of any kind.
//   - next_page_token is null for a context holding contextEnvVarPageSize
//     variables or fewer and non-null for one holding more. Measured by deleting
//     one variable at a time from 109 down to 100: non-null at every count above
//     100, null at exactly 100.
//   - It is a real, correctly computed cursor. Base64-decoded it names the last
//     item on the page ("…:after ZZZ07").
//   - No request parameter feeds it back. page-token, page_token, pageToken,
//     page[token], next_page_token, page[cursor], cursor and after were each sent
//     carrying the advertised token; all eight answered 200 with page one again
//     and the same token. Unknown query parameters are ignored rather than
//     rejected, so a wrong spelling cannot be told from a right one by its
//     status.
//   - There is no route that reads one context environment variable by name:
//     GET /context/{id}/environment-variable/{name} answers 404 page not found.
//     A variable past the first page cannot be reached any other way either, so
//     do not advise reading them individually by name.
//   - The 100-variable cap is enforced per request but not atomically.
//     Sequential PUTs are refused at the cap with 400 "Cannot create more than
//     100 environment variables per context", while thirteen concurrent PUTs
//     from a count of 96 all succeeded, leaving 109. A context above the cap is
//     therefore not hypothetical.
//
// Because the token names the last item on the page, it changes whenever the tail
// of page one changes. Two consecutive first-page requests against a context that
// is being written to come back with two DIFFERENT non-null tokens — measured, by
// deleting a variable sorting before the page boundary between the two requests,
// which moved the token from "after ZZZ07" to "after ZZZ08". That is why
// truncation is detected from the first response's token on its own, and never by
// comparing a response's token against the token that was sent: such a comparison
// sees two unequal tokens, concludes pagination advanced, and drains for ever,
// adding contextEnvVarPageSize duplicates per iteration.
//
// Page carries the variables that WERE disclosed. They are reachable only through
// this error, so that no caller can mistake a partial collection for a complete
// one without having handled the truncation first — but a caller that only needs
// one named variable, and finds it, is not affected by the truncation at all and
// should not be made to fail.
type ContextEnvVarsTruncatedError struct {
	ContextID string
	Page      []ContextEnvironmentVariable
}

func (e *ContextEnvVarsTruncatedError) Error() string {
	return fmt.Sprintf(
		"listing environment variables of context %s: the context holds more than the %d variables "+
			"this route discloses, and the page token it advertises is not read back on any request, "+
			"so the rest cannot be fetched — not by this route and not by any other, as CircleCI has "+
			"no route that reads one context environment variable by name. Split the variables across "+
			"more than one context so that each holds at most %d",
		e.ContextID, contextEnvVarPageSize, contextEnvVarPageSize,
	)
}

// Find returns the variable named name from the page that was disclosed.
//
// A false second result means only that the variable was not in the part of the
// collection the API was willing to show. It is NOT evidence that the variable
// does not exist, and a caller must not treat it as a deletion — that mistake
// turns a refresh into a plan to recreate a variable that is alive, and the
// recreate overwrites whatever value is really stored on it.
func (e *ContextEnvVarsTruncatedError) Find(name string) (ContextEnvironmentVariable, bool) {
	for _, variable := range e.Page {
		if variable.Variable == name {
			return variable, true
		}
	}

	return ContextEnvironmentVariable{}, false
}

// AsContextEnvVarsTruncated reports whether err is a truncated-list error, and
// returns it so callers can reach the variables that were disclosed.
func AsContextEnvVarsTruncated(err error) (*ContextEnvVarsTruncatedError, bool) {
	var truncated *ContextEnvVarsTruncatedError
	if errors.As(err, &truncated) {
		return truncated, true
	}

	return nil, false
}

// ListContextEnvironmentVariables returns every environment variable set on a
// context. Values are truncated; see ContextEnvironmentVariable. The result is
// nil when the context has none.
//
// Like GetContext, this route sits behind the same middleware, so a context
// that no longer exists answers 403 rather than 404 — see context.go's
// GetContext comment.
//
// A context holding more variables than the route discloses is reported as a
// *ContextEnvVarsTruncatedError, which carries the page that was disclosed; that
// type's doc comment records the measured pagination contract behind everything
// below.
//
// There is exactly one request, and it carries no page parameter. Both follow
// from the same measurement: the route ignores every spelling of the page token,
// so a second request cannot reach anything the first did not, and sending a
// parameter that is ignored would advertise a pagination contract that does not
// exist. This is the reasoning behind itemsResponse in items.go, applied to a
// route that differs only in that it does put next_page_token in the body —
// where it serves as a truncation flag rather than as a cursor.
//
// It deliberately does NOT drain. DrainV2 terminates on a token that repeats,
// which this route's token does not: the token names the last item on the page,
// so any concurrent write that moves the page-one boundary changes it. A drain
// then follows a token that never advances, adding contextEnvVarPageSize
// duplicates per iteration until the provider runs out of memory — the one
// failure mode worse than a wrong answer, and one that was reproduced with a
// server answering the same page under a fresh token each time.
func (c *Client) ListContextEnvironmentVariables(ctx context.Context, contextID string) ([]ContextEnvironmentVariable, error) {
	var page PaginatedResponse[ContextEnvironmentVariable]
	if err := c.GetV2(ctx, contextEnvVarsRoute, &page, RouteParams(contextID)); err != nil {
		return nil, err
	}

	// A token at all means there is more, and that nothing can reach it.
	if page.NextPageToken != "" {
		return nil, &ContextEnvVarsTruncatedError{ContextID: contextID, Page: page.Items}
	}

	return page.Items, nil
}

// checkContextEnvVarName rejects a name that is exactly "." or ".." — see
// isDotSegment in project.go — before it reaches RouteParams. RouteParams
// percent-escapes each value as its own path segment, but escaping does not
// touch a segment made only of dots, so an unrejected name would put a
// literal "./" or "../" into the request path in place of the variable name.
func checkContextEnvVarName(name string) error {
	if isDotSegment(name) {
		return fmt.Errorf("circleci: context environment variable name %q is not allowed", name)
	}

	return nil
}

// UpsertContextEnvironmentVariable creates or overwrites an environment
// variable on a context and returns it as stored. The returned value's
// TruncatedValue is always "" — see ContextEnvironmentVariable's doc comment —
// so callers must not treat it as meaningful; the value that was just written
// is never echoed back at all, truncated or otherwise.
//
// The 100-variable cap applies to the create half only. Measured on a context
// already holding more than 100: a PUT to a name that exists answers 200 and
// updates it, while a PUT to a new name answers 400 "Cannot create more than 100
// environment variables per context". So an over-capacity context is not frozen —
// its existing variables can still be rotated, which is why a truncated list is
// handled rather than treated as a dead end.
func (c *Client) UpsertContextEnvironmentVariable(
	ctx context.Context,
	contextID, name, value string,
) (*ContextEnvironmentVariable, error) {
	if err := checkContextEnvVarName(name); err != nil {
		return nil, err
	}

	var updated ContextEnvironmentVariable
	body := ContextEnvironmentVariableInput{Value: value}
	if err := c.PutV2(ctx, contextEnvVarRoute, body, &updated, RouteParams(contextID, name)); err != nil {
		return nil, err
	}

	return &updated, nil
}

// DeleteContextEnvironmentVariable removes an environment variable from a
// context.
//
// This route sits behind the same middleware as GetContext (see
// context.go), so a context that no longer exists answers 403 rather than 404.
func (c *Client) DeleteContextEnvironmentVariable(ctx context.Context, contextID, name string) error {
	if err := checkContextEnvVarName(name); err != nil {
		return err
	}

	return c.DeleteV2(ctx, contextEnvVarRoute, RouteParams(contextID, name))
}
