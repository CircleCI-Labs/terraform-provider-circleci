// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
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
// of the value on its own, with no mask prefix: the API takes the last four
// characters, or the last floor(len/2) when the value is
// eight characters or shorter. So "FOOBARBAZ" reports "RBAZ", "FOOBAR" reports
// "BAR", "FOO" reports "O", and an empty value reports "". A project
// environment variable's masked value, by contrast, prefixes the same tail with
// "xxxx" — the two conventions look alike and are not, so do not reuse one
// helper for both.
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
type ContextEnvironmentVariableInput struct {
	Value string `json:"value"`
}

// contextEnvVarPageSize is the page size the API asks its own backend for. It
// is fixed there, not a parameter this client can influence, and it is only
// recorded here to explain the guard below.
const contextEnvVarPageSize = 100

// ListContextEnvironmentVariables returns every environment variable set on a
// context, following pagination to the last page. Values are truncated; see
// ContextEnvironmentVariable. The result is nil when the context has none.
//
// Like GetContext, this route sits behind the same middleware, so a context
// that no longer exists answers 403 rather than 404 — see context.go's
// GetContext comment.
//
// This route's pagination is broken server-side and the loop below is guarded
// against it. The handler reads the page token from the request's *path*
// parameters rather than its query string, and the route has no such path
// parameter, so the `page-token` this client sends is never seen: every request
// is answered with the first page and with the same next_page_token. Draining
// naively would re-request page one for ever, accumulating duplicates until the
// provider ran out of memory — the one failure mode worse than a wrong answer.
//
// So a repeated token is treated as a hard error rather than as a quiet stop. A
// context with more than contextEnvVarPageSize variables cannot be listed
// completely through this route at all, and silently returning the first page
// would make a data source under-report and could make a resource conclude one
// of its variables had been deleted.
func (c *Client) ListContextEnvironmentVariables(ctx context.Context, contextID string) ([]ContextEnvironmentVariable, error) {
	return DrainV2(ctx, func(ctx context.Context, pageToken string) (PaginatedResponse[ContextEnvironmentVariable], error) {
		var page PaginatedResponse[ContextEnvironmentVariable]

		err := c.GetV2(ctx, contextEnvVarsRoute, &page, RouteParams(contextID), PageToken(pageToken))
		if err != nil {
			return page, err
		}

		// The token just sent came back unchanged, so the next request would
		// repeat this one.
		if pageToken != "" && page.NextPageToken == pageToken {
			return PaginatedResponse[ContextEnvironmentVariable]{}, fmt.Errorf(
				"listing environment variables of context %s: CircleCI answered with the same page token it "+
					"was given, so the collection cannot be paged through and this list is incomplete. A "+
					"context holding more than %d environment variables hits this, because the route ignores "+
					"the page token it advertises. Split the variables across more than one context, or read "+
					"them individually by name",
				contextID, contextEnvVarPageSize,
			)
		}

		return page, nil
	})
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
