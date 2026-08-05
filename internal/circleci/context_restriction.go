// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Context restriction routes. Restrictions are served by v2 on every deployment.
//
// Shapes and semantics here follow the API's own handlers and
// github.com/CircleCI-Public/circleci-cli's internal/apiclient/context.go
// (MIT), which agree on routes and field names.
const (
	contextRestrictionsRoute = "/context/%s/restrictions"
	contextRestrictionRoute  = "/context/%s/restrictions/%s"
)

// The kinds of restriction the API accepts.
const (
	// ContextRestrictionTypeProject restricts a context to one project.
	ContextRestrictionTypeProject = "project"
	// ContextRestrictionTypeGroup restricts a context to a group.
	ContextRestrictionTypeGroup = "group"
	// ContextRestrictionTypeExpression restricts a context with an expression.
	ContextRestrictionTypeExpression = "expression"
)

// ContextRestriction limits which projects, groups or pipeline conditions may
// use a context.
//
// ProjectID is only populated for ContextRestrictionTypeProject restrictions —
// the API sets it to a copy of RestrictionValue in that case and omits the field
// entirely otherwise, so it reads back as "" for group and expression
// restrictions.
type ContextRestriction struct {
	ID        string `json:"id"`
	ContextID string `json:"context_id"`
	// Name is the human-readable name of whatever the restriction points at, for
	// example a group's name. It is "" for expression restrictions.
	Name             string `json:"name"`
	RestrictionType  string `json:"restriction_type"`
	RestrictionValue string `json:"restriction_value"`
	ProjectID        string `json:"project_id"`
}

// ListContextRestrictions returns every restriction on a context.
//
// The response carries no next_page_token: this endpoint returns the whole set
// in one body, so there is nothing to drain. The result is nil when the context
// is unrestricted.
func (c *Client) ListContextRestrictions(ctx context.Context, contextID string) ([]ContextRestriction, error) {
	var response itemsResponse[ContextRestriction]
	if err := c.GetV2(ctx, contextRestrictionsRoute, &response, RouteParams(contextID)); err != nil {
		return nil, err
	}

	return response.Items, nil
}

// CreateContextRestrictionRequest is the create body.
type CreateContextRestrictionRequest struct {
	RestrictionType  string `json:"restriction_type"`
	RestrictionValue string `json:"restriction_value"`
}

// CreateContextRestriction adds a restriction to a context and returns it as
// stored.
//
// The response never carries a "name": the create route's response has no
// such field — the human-readable name of whatever the restriction points at
// (e.g. a project's slug) is learned out of band and only reported by a later
// list/read. Callers
// must not treat the returned Name as meaningful; it decodes to "".
func (c *Client) CreateContextRestriction(
	ctx context.Context,
	contextID string,
	req CreateContextRestrictionRequest,
) (*ContextRestriction, error) {
	var created ContextRestriction
	if err := c.PostV2(ctx, contextRestrictionsRoute, req, &created, RouteParams(contextID)); err != nil {
		return nil, err
	}

	return &created, nil
}

// DeleteContextRestriction removes a restriction from a context.
//
// This route sits behind the same middleware as GetContext and
// DeleteContext (see context.go), so a context that no longer exists — and
// therefore a restriction that no longer exists along with it — answers 403
// rather than 404.
func (c *Client) DeleteContextRestriction(ctx context.Context, contextID, restrictionID string) error {
	return c.DeleteV2(ctx, contextRestrictionRoute, RouteParams(contextID, restrictionID))
}
