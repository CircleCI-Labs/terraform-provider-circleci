// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"fmt"
)

// Routes for the URL orb allow list.
//
// Unlike the orb registry routes, these three are not v3 and are not served by
// any orb service: they are v2 organization routes, and the allow list lives on
// the organization document itself. Being v2 is not proof of Server support on
// its own — compare pipelineDefinitionsRoute, also v2, which Server does not
// route, and otelExportersRoute, v2 but owned by a service Server does not
// deploy. Some v2 routes are served on CircleCI Server and others are not, so
// the API version alone does not settle availability; these three are served on
// both Cloud and Server, so nothing here needs gating. (Whether a given Server
// installation has URL orbs turned on is a separate question — the allow list
// routes work regardless, and deletion is explicitly allowed even when the
// feature is off, so that an admin can trim the list.)
//
// Server-side rules worth knowing, none of which the routes advertise:
//
//   - An organization's allow list is capped at 5 entries. The sixth create
//     fails with a 400 naming the limit.
//   - A prefix must parse as a URL, use the https scheme, and have a path that
//     ends in "/". "https://example.com" is rejected; "https://example.com/" is
//     accepted.
//   - A name must not be blank.
//   - Duplicate prefixes are allowed on purpose, so that one prefix can be
//     listed under more than one auth method.
const (
	urlOrbAllowListRoute      = "/organization/%s/url-orb-allow-list"
	urlOrbAllowListEntryRoute = "/organization/%s/url-orb-allow-list/%s"
)

// Authentication methods a URL orb allow list entry may use when fetching orb
// source from its prefix.
const (
	// URLOrbAllowListAuthNone fetches the URL with no credentials.
	URLOrbAllowListAuthNone = "none"
	// URLOrbAllowListAuthGitHubOAuth uses the organization's GitHub OAuth
	// credentials.
	URLOrbAllowListAuthGitHubOAuth = "github-oauth"
	// URLOrbAllowListAuthGitHubApp uses the organization's GitHub App
	// installation.
	URLOrbAllowListAuthGitHubApp = "github-app"
	// URLOrbAllowListAuthBitbucketOAuth uses the organization's Bitbucket OAuth
	// credentials.
	URLOrbAllowListAuthBitbucketOAuth = "bitbucket-oauth"
)

// URLOrbAllowListAuthValues is every accepted auth value, in the order the API
// documents them. It exists so the provider's schema validator and the docs
// cannot drift from the client.
var URLOrbAllowListAuthValues = []string{
	URLOrbAllowListAuthBitbucketOAuth,
	URLOrbAllowListAuthGitHubApp,
	URLOrbAllowListAuthGitHubOAuth,
	URLOrbAllowListAuthNone,
}

// URLOrbAllowListEntry is one entry in an organization's URL orb allow list. A
// URL orb reference is permitted when it starts with Prefix.
type URLOrbAllowListEntry struct {
	// ID is the entry's UUID.
	ID string `json:"id"`
	// Name is a human-readable label for the entry.
	Name string `json:"name"`
	// Prefix is the URL prefix the entry permits.
	Prefix string `json:"prefix"`
	// Auth is the authentication method used when fetching a matching URL, one
	// of the URLOrbAllowListAuth constants.
	Auth string `json:"auth"`
}

// CreateURLOrbAllowListEntryRequest is the body of a create. All three fields
// are required by the API, so none is omitempty.
type CreateURLOrbAllowListEntryRequest struct {
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
	Auth   string `json:"auth"`
}

// ListURLOrbAllowList returns every entry in an organization's URL orb allow
// list. org may be an organization UUID or a slug such as "gh/acme".
//
// The endpoint returns the v2 {"items": [...]} envelope. It does not currently
// paginate, but draining is used anyway so that adding a next_page_token later
// does not silently truncate the result.
//
// An organization that does not exist and one the token may not view both answer
// HTTP 404 — the handler throws not-found for a failed view-permission check.
// That 404 satisfies IsNotFound, so a caller using this for drift detection must
// not read every IsNotFound as "the entry is gone": see GetURLOrbAllowListEntry.
func (c *Client) ListURLOrbAllowList(ctx context.Context, org string) ([]URLOrbAllowListEntry, error) {
	return DrainV2(ctx, func(ctx context.Context, pageToken string) (PaginatedResponse[URLOrbAllowListEntry], error) {
		var page PaginatedResponse[URLOrbAllowListEntry]

		err := c.GetV2(ctx, urlOrbAllowListRoute, &page, RouteParams(org), PageToken(pageToken))
		if err != nil {
			return PaginatedResponse[URLOrbAllowListEntry]{}, err
		}

		return page, nil
	})
}

// GetURLOrbAllowListEntry returns a single entry by id.
//
// There is no read-one route, so this filters the listing. A listing that does
// not contain the id yields the ErrNotFound *sentinel*.
//
// Only that sentinel means "this entry no longer exists". An HTTP 404 from the
// listing means the organization is missing or unviewable, and both satisfy
// IsNotFound, so a caller that drops state on either would recreate an entry
// against a limit of five whenever a token lost its view permission. Test with
// errors.Is(err, ErrNotFound) where the difference matters.
func (c *Client) GetURLOrbAllowListEntry(ctx context.Context, org, entryID string) (*URLOrbAllowListEntry, error) {
	entries, err := c.ListURLOrbAllowList(ctx, org)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.ID == entryID {
			found := entry

			return &found, nil
		}
	}

	return nil, fmt.Errorf("url orb allow list entry %q in organization %q: %w", entryID, org, ErrNotFound)
}

// CreateURLOrbAllowListEntry adds an entry to an organization's URL orb allow
// list.
//
// The API answers 201 with only {"id", "message"}, so the returned entry
// combines that id with the requested fields. There is no read-one route to
// confirm them against, and no update route at all.
func (c *Client) CreateURLOrbAllowListEntry(ctx context.Context, org string, req CreateURLOrbAllowListEntryRequest) (*URLOrbAllowListEntry, error) {
	var created struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	}

	err := c.PostV2(ctx, urlOrbAllowListRoute, req, &created, RouteParams(org))
	if err != nil {
		return nil, err
	}

	return &URLOrbAllowListEntry{
		ID:     created.ID,
		Name:   req.Name,
		Prefix: req.Prefix,
		Auth:   req.Auth,
	}, nil
}

// DeleteURLOrbAllowListEntry removes an entry from an organization's URL orb
// allow list.
//
// The handler pulls the entry out of the organization document by id and answers
// 200 with {"id", "message"} whether or not it was there, so deleting an id that
// no longer exists succeeds rather than 404ing. A 404 from this route means the
// organization itself could not be resolved or viewed.
func (c *Client) DeleteURLOrbAllowListEntry(ctx context.Context, org, entryID string) error {
	return c.DeleteV2(ctx, urlOrbAllowListEntryRoute, RouteParams(org, entryID))
}
