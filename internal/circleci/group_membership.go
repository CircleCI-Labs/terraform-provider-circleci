// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Group membership routes.
//
// These are NOT the public /api/v2 group routes documented in group.go. Group
// membership was briefly implemented against
// "/organizations/{org}/groups/{id}/users" and "/remove_users" and then
// removed: live-verified, those answer HTTP 404 for a group that demonstrably
// exists, and the published spec confirms it with a per-route `servers:`
// override pointing at a host reserved for internal traffic. See
// API-COVERAGE.md.
//
// The routes that actually work are on the private origin (see private.go),
// confirmed two ways: the org-migration CLI calls
// POST /private/ciam/orgs/{orgID}/groups/{groupID}/add-users in production,
// and the CircleCI web app's own "add members" / "remove members" buttons
// call the sibling GET .../users (list) and POST .../delete-users (remove) on
// the same family, pinning the request/response shapes below field for
// field. This client targets the same paths on Client.PrivateHost(), the
// origin the migration CLI verified against, rather than the web app's own,
// separate edge.
//
// Every caller must gate on Client.IsCloud(); see requireStandaloneCapable in
// the provider package, which also covers the narrower "circleci type
// organization" requirement groups have on top of that.
const (
	groupMembersRoute       = "/private/ciam/orgs/%s/groups/%s/users"
	groupAddMembersRoute    = "/private/ciam/orgs/%s/groups/%s/add-users"
	groupDeleteMembersRoute = "/private/ciam/orgs/%s/groups/%s/delete-users"
)

// GroupMember is one user in a CircleCI group.
//
// Members are identified by user UUID, not by login or email: both the
// add-users and delete-users payloads take a "user_ids" array of UUIDs.
// Username, Email, AvatarURL and CreatedAt are returned for display only and
// cannot be used to address a member. CreatedAt is kept as the string the API
// sends (an RFC 3339 timestamp with microsecond precision, e.g.
// "2023-12-13T10:10:37.951356Z") rather than parsed, the same choice
// checkout_key.go makes and for the same reason: nothing here needs to do
// arithmetic on it, so parsing would only add a failure mode.
type GroupMember struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
	Email     string `json:"email"`
	GroupID   string `json:"group_id"`
	CreatedAt string `json:"created_at"`
}

// groupMembersResponse mirrors GET .../groups/{groupID}/users. Unlike the
// public route this replaced, there is no next_page_token in this response at
// all — it carries only items and count — so there is no pagination trap to
// guard against here: a single request is not a shortcut, it is the entire
// contract.
type groupMembersResponse struct {
	Items []GroupMember `json:"items"`
	Count int           `json:"count"`
}

// groupMembersRequest is the body of both the add-users and the delete-users
// call. The two actions differ only in the route they post to.
type groupMembersRequest struct {
	UserIDs []string `json:"user_ids"`
}

// GroupMembershipService reads and writes the membership of CircleCI groups.
//
// There is no endpoint that replaces a group's membership wholesale: callers
// converge on a desired set by adding and removing the difference. MemberDelta
// computes that difference.
type GroupMembershipService struct {
	client *Client
}

// GroupMembership returns the group membership service for this client.
func (c *Client) GroupMembership() *GroupMembershipService {
	return &GroupMembershipService{client: c}
}

// List fetches the members of a group. The result is nil when the group has no
// members.
func (s *GroupMembershipService) List(ctx context.Context, orgID, groupID string) ([]GroupMember, error) {
	var page groupMembersResponse
	if err := s.client.GetPrivate(ctx, groupMembersRoute, &page, RouteParams(orgID, groupID)); err != nil {
		return nil, err
	}

	return page.Items, nil
}

// Add adds users to a group. Users already in the group are unaffected.
//
// An empty userIDs makes no request: the endpoint rejects an empty array, and
// "add nothing" is already satisfied.
func (s *GroupMembershipService) Add(ctx context.Context, orgID, groupID string, userIDs []string) error {
	if len(userIDs) == 0 {
		return nil
	}

	// The response carries nothing worth recording, so it is not decoded.
	return s.client.PostPrivate(ctx, groupAddMembersRoute, groupMembersRequest{UserIDs: userIDs}, nil,
		RouteParams(orgID, groupID),
	)
}

// Remove removes users from a group. Users that are not members are unaffected.
//
// An empty userIDs makes no request, for the same reason as Add.
func (s *GroupMembershipService) Remove(ctx context.Context, orgID, groupID string, userIDs []string) error {
	if len(userIDs) == 0 {
		return nil
	}

	return s.client.PostPrivate(ctx, groupDeleteMembersRoute, groupMembersRequest{UserIDs: userIDs}, nil,
		RouteParams(orgID, groupID),
	)
}

// MemberDelta reports which user ids must be added to, and removed from, actual
// in order to reach desired.
//
// It exists because the API has no "replace the membership" call: converging on
// a desired set means posting the additions and the removals separately. Both
// results are deduplicated, and ordered by first appearance in the slice they
// came from, so that a given pair of inputs always produces the same two
// requests.
func MemberDelta(desired, actual []string) (toAdd, toRemove []string) {
	desiredSet := make(map[string]struct{}, len(desired))
	for _, id := range desired {
		desiredSet[id] = struct{}{}
	}

	actualSet := make(map[string]struct{}, len(actual))
	for _, id := range actual {
		actualSet[id] = struct{}{}
	}

	seen := make(map[string]struct{}, len(desired))
	for _, id := range desired {
		if _, member := actualSet[id]; member {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		toAdd = append(toAdd, id)
	}

	seen = make(map[string]struct{}, len(actual))
	for _, id := range actual {
		if _, wanted := desiredSet[id]; wanted {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		toRemove = append(toRemove, id)
	}

	return toAdd, toRemove
}
