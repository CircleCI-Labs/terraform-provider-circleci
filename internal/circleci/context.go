// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"fmt"
)

// Context routes.
//
// Contexts are addressed through v2 on every deployment. The collection is
// scoped by owner-id and owner-type *query* parameters rather than a path
// segment, which is why there is no owner in the route.
const contextsRoute = "/context"

// ContextOwnerTypeOrganization is the only owner-type the contexts endpoint
// accepts. The API documents "account" as well, but the handler rejects it with
// HTTP 400 ("only organization is supported at present"), so the provider always
// sends this value rather than exposing a choice that cannot work.
const ContextOwnerTypeOrganization = "organization"

// Context is a CircleCI context: a named collection of environment variables
// that jobs can be granted access to.
//
// CreatedAt is kept as the string the API sent (an RFC 3339 timestamp) so that
// it round-trips into Terraform state exactly as received.
//
// The list endpoint can also embed each context's environment variables, behind
// an include-env-vars query parameter. That is deliberately not requested here:
// the values come back truncated, and a dedicated data source already reads them.
type Context struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

// ListContexts returns every context owned by an organization, following
// pagination to the last page. The result is nil when the organization has no
// contexts.
func (c *Client) ListContexts(ctx context.Context, organizationID string) ([]Context, error) {
	return DrainV2(ctx, func(ctx context.Context, pageToken string) (PaginatedResponse[Context], error) {
		var page PaginatedResponse[Context]
		err := c.GetV2(ctx, contextsRoute, &page,
			Query("owner-id", organizationID),
			Query("owner-type", ContextOwnerTypeOrganization),
			PageToken(pageToken),
		)

		return page, err
	})
}

// GetContext returns one context by id.
func (c *Client) GetContext(ctx context.Context, contextID string) (*Context, error) {
	var found Context
	if err := c.GetV2(ctx, "/context/%s", &found, RouteParams(contextID)); err != nil {
		return nil, err
	}

	return &found, nil
}

// FindContextByName returns the context with the given name in an organization.
//
// The API has no lookup-by-name route, so this lists and filters. Names are unique
// within an organization, which is what makes the search unambiguous — on CircleCI
// Server they are unique across every organization in the account.
//
// Matching is exact and case-sensitive, because the API treats names that way.
func (c *Client) FindContextByName(ctx context.Context, organizationID, name string) (*Context, error) {
	contexts, err := c.ListContexts(ctx, organizationID)
	if err != nil {
		return nil, err
	}

	for i := range contexts {
		if contexts[i].Name == name {
			return &contexts[i], nil
		}
	}

	return nil, fmt.Errorf("context named %q in organization %s: %w", name, organizationID, ErrNotFound)
}
