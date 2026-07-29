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
//
// Shapes and semantics here follow the API's the CircleCI API
// (the API, the API, the API, the API), which
// is the service that actually owns these routes, and
// github.com/CircleCI-Public/circleci-cli's internal/apiclient/context.go (MIT),
// which independently arrives at the same field names and routes.
const (
	contextsRoute = "/context"
	contextRoute  = "/context/%s"
)

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
//
// A context that does not exist, belongs to another organization, or is
// inaccessible to the configured token all answer HTTP 403, not 404: the route
// sits behind the API's context-resolution step
// (the CircleCI API), which resolves the id to its owning
// organization through a separate lookup and maps every failure of that lookup
// to StatusForbidden before the handler that would otherwise 404 ever runs.
// the API.ErrNotFound (mapped to a real 404 by api.go's
// the API) only fires on the rare race where that lookup succeeds and
// the read then fails, so callers must treat 403 as "possibly gone, possibly
// just unauthorized" rather than as IsNotFound. See context_resource.go's Read
// for how the ambiguity is surfaced rather than guessed at.
func (c *Client) GetContext(ctx context.Context, contextID string) (*Context, error) {
	var found Context
	if err := c.GetV2(ctx, contextRoute, &found, RouteParams(contextID)); err != nil {
		return nil, err
	}

	return &found, nil
}

// createContextOwner is the create body's nested owner object. Only ID and
// Type are ever sent: the API also accepts a Slug instead of an ID, but this
// provider always holds an organization UUID, never a slug.
type createContextOwner struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// createContextRequest is the create body.
type createContextRequest struct {
	Name  string             `json:"name"`
	Owner createContextOwner `json:"owner"`
}

// CreateContext creates a context owned by an organization and returns it as
// stored.
//
// The response is deliberately narrow: the API's postOrgContext
// (the API) builds an explicit anonymous response struct with only
// id/name/created_at, dropping the owner it was just given. There is nothing
// else to map back onto Context.
func (c *Client) CreateContext(ctx context.Context, organizationID, name string) (*Context, error) {
	var created Context
	body := createContextRequest{
		Name: name,
		Owner: createContextOwner{
			ID:   organizationID,
			Type: ContextOwnerTypeOrganization,
		},
	}
	if err := c.PostV2(ctx, contextsRoute, body, &created); err != nil {
		return nil, err
	}

	return &created, nil
}

// DeleteContext deletes a context by id.
//
// Like GetContext, this route sits behind the context-resolution step, so a context that no
// longer exists answers 403 rather than 404 — see GetContext's comment. That
// makes 403 the practical "already gone" signal for Delete: callers should
// treat it as success, mirroring how a missing CircleCI group answers 403 (see
// group.go).
func (c *Client) DeleteContext(ctx context.Context, contextID string) error {
	return c.DeleteV2(ctx, contextRoute, RouteParams(contextID))
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
