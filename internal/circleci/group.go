// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Group routes.
//
// Groups are addressed through v2 on every deployment: /api/v2/organizations
// /{org_id}/groups is one of the few routes CircleCI Server exposes to the
// public API service, so there is deliberately no v3 path here and no UseV3
// branch in this service.
const (
	groupsRoute = "/organizations/%s/groups"
	groupRoute  = "/organizations/%s/groups/%s"
)

// Group is a CircleCI group: a named collection of organization members that
// project and context permissions can be granted to.
//
// The wire format is flat, with the organization supplied in the path rather
// than the body, which is why there is no organization field here.
type Group struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// CreateGroupRequest is the body of a group create. It is a distinct type from
// Group so that a create never sends an id the server would have to ignore.
type CreateGroupRequest struct {
	Name string `json:"name"`
	// Description is omitted when empty, so that a group created without one
	// keeps whatever default the server applies.
	Description string `json:"description,omitempty"`
}

// GroupService reads and writes CircleCI groups.
//
// The API has no update endpoint: a group's name and description are fixed once
// it is created, so callers that need to change either must delete and recreate.
type GroupService struct {
	client *Client
}

// Groups returns the group service for this client.
func (c *Client) Groups() *GroupService {
	return &GroupService{client: c}
}

// Get fetches a single group by id within an organization. A missing group is
// reported as an error satisfying IsNotFound.
func (s *GroupService) Get(ctx context.Context, orgID, groupID string) (*Group, error) {
	var group Group
	if err := s.client.GetV2(ctx, groupRoute, &group, RouteParams(orgID, groupID)); err != nil {
		return nil, err
	}

	// A 2xx body carrying no id is not a group. Treating it as missing keeps an
	// empty object from being written into Terraform state as if it were real.
	if group.ID == "" {
		return nil, ErrNotFound
	}

	return &group, nil
}

// List fetches every group in an organization, following pagination to the last
// page. The result is nil when the organization has no groups.
//
// This route advertises pagination it does not implement: it answers with a
// next_page_token, and the published schema gives it limit and page-token
// parameters, but the handler reads no inbound token and the service behind it
// answers {"items": [...]} with no token of its own, so next_page_token is
// always null. Draining is kept here anyway, because a token that never arrives
// costs one request either way, and DrainV2's ErrPaginationDidNotAdvance guard
// means that if the route ever starts returning a token without honouring the
// one it is given, this fails loudly instead of looping.
//
// [NET, 2026-08-22] The "always null" claim above was previously only ever
// checked against fixtures of four groups or fewer — one page's worth on any
// plausible page size, so it never actually exercised draining past a first
// page. Live-tested by creating groups one at a time on a standalone
// organization (gh-app-cci-1) up through 30, then 100: at every count the
// response stayed a single {"items": [...30 or 100 groups...],
// "next_page_token": null} with no page boundary ever appearing, including
// with an explicit ?limit=2 query parameter added to the request (silently
// ignored, same as the page-token). The 101st create answered
// 409 "Exceeded max number of groups in org: 100." — an organization-wide cap,
// not a page size — so this route cannot be observed to paginate at any group
// count the API will accept in the first place. All 100 groups were deleted
// after the probe.
//
// The membership list (formerly group_membership.go, removed — see
// CHANGELOG.md) had the same advertised-but-unimplemented pagination, but for a
// different reason it could not stay in this package: those routes 404 on the
// public host entirely, unlike this one.
func (s *GroupService) List(ctx context.Context, orgID string) ([]Group, error) {
	return DrainV2(ctx, func(ctx context.Context, pageToken string) (PaginatedResponse[Group], error) {
		var page PaginatedResponse[Group]
		err := s.client.GetV2(ctx, groupsRoute, &page,
			RouteParams(orgID),
			PageToken(pageToken),
		)

		return page, err
	})
}

// Create creates a group in an organization and returns it as stored, so that
// callers record the server's values rather than their own request.
func (s *GroupService) Create(ctx context.Context, orgID string, req CreateGroupRequest) (*Group, error) {
	var group Group
	if err := s.client.PostV2(ctx, groupsRoute, req, &group, RouteParams(orgID)); err != nil {
		return nil, err
	}

	return &group, nil
}

// Delete removes a group from an organization.
func (s *GroupService) Delete(ctx context.Context, orgID, groupID string) error {
	return s.client.DeleteV2(ctx, groupRoute, RouteParams(orgID, groupID))
}
