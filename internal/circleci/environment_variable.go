// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"net/url"
)

// ProjectEnvironmentVariable is an environment variable set on a project, as
// returned by GET /api/v2/project/{project-slug}/envvar.
//
// Value is always masked: the API answers with four "x" characters followed by
// the last four characters of the real value, matching what the CircleCI web UI
// displays. The configured value can never be read back, so this is only useful
// for confirming which variable is set, not what it is set to.
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
func projectEnvVarNameRoute(projectSlug, name string) (string, error) {
	route, err := projectEnvVarRoute(projectSlug)
	if err != nil {
		return "", err
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
// This mirrors circle.http.api.v2.project's create-env-var-response (the
// v2 API behind the v2 API): even the create response's Value comes back
// through env-var-read-api, i.e. already masked. The literal value configured
// is never echoed back by any route, not even the one that just set it.
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
// Shapes and semantics here follow the API's the CircleCI API
// (context_env_vars_get.go, context_env_var_put.go, context_env_var_delete.go)
// and github.com/CircleCI-Public/circleci-cli's internal/apiclient/context.go
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
// TruncatedValue is the only trace of the value the API ever discloses: four
// "x" characters followed by the value's last four characters, the same
// convention as ProjectEnvironmentVariable.Value. It is empty on the value
// returned by UpsertContextEnvironmentVariable, because the PUT route's own
// response struct (the API's EnvVarResponse) carries no such field at
// all — only the list route does.
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

// ListContextEnvironmentVariables returns every environment variable set on a
// context, following pagination to the last page. Values are truncated; see
// ContextEnvironmentVariable. The result is nil when the context has none.
//
// Like GetContext, this route sits behind the API's the context-resolution step
// middleware, so a context that no longer exists answers 403 rather than 404 —
// see context.go's GetContext comment.
func (c *Client) ListContextEnvironmentVariables(ctx context.Context, contextID string) ([]ContextEnvironmentVariable, error) {
	return DrainV2(ctx, func(ctx context.Context, pageToken string) (PaginatedResponse[ContextEnvironmentVariable], error) {
		var page PaginatedResponse[ContextEnvironmentVariable]
		err := c.GetV2(ctx, contextEnvVarsRoute, &page, RouteParams(contextID), PageToken(pageToken))

		return page, err
	})
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
// This route sits behind the same context-resolution step as GetContext (see
// context.go), so a context that no longer exists answers 403 rather than 404.
func (c *Client) DeleteContextEnvironmentVariable(ctx context.Context, contextID, name string) error {
	return c.DeleteV2(ctx, contextEnvVarRoute, RouteParams(contextID, name))
}
