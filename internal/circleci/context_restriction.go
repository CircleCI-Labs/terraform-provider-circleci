// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Context restriction routes. Restrictions are served by v2 on every deployment.
const contextRestrictionsRoute = "/context/%s/restrictions"

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
