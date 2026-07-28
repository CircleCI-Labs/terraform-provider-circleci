// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"fmt"
)

// Routes for the URL orb allow list. These are v2, so they work on CircleCI
// Server as well as Cloud.
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
// not contain the id yields ErrNotFound, which is what lets callers use
// IsNotFound uniformly for drift detection instead of inspecting status codes.
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
// The API answers with only {"id", "message"}, so the returned entry combines
// that id with the requested fields.
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
func (c *Client) DeleteURLOrbAllowListEntry(ctx context.Context, org, entryID string) error {
	return c.DeleteV2(ctx, urlOrbAllowListEntryRoute, RouteParams(org, entryID))
}
