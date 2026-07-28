// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Group membership routes.
//
// Membership hangs off the group routes, which are among the few paths a
// CircleCI Server installation forwards to the public API service, so there is
// no v3 variant and no UseV3 branch here. See group.go.
//
// Note the asymmetry: there is no DELETE verb for a member. Removal is a POST to
// a distinct /remove_users action, which is why Add and Remove take the same
// body shape but different routes.
const (
	groupMembersRoute       = "/organizations/%s/groups/%s/users"
	groupRemoveMembersRoute = "/organizations/%s/groups/%s/remove_users"
)

// GroupMember is one user in a CircleCI group.
//
// Members are identified by user UUID, not by login or email: both the add and
// the remove payloads take a "user_ids" array of UUIDs. Username and Email are
// returned for display only and cannot be used to address a member.
type GroupMember struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
	Email     string `json:"email"`
	GroupID   string `json:"group_id"`
}

// groupMembersRequest is the body of both the add and the remove call. The two
// actions differ only in the route they post to.
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
//
// This deliberately issues a single request rather than draining pages. The
// response carries a next_page_token, but the endpoint does not forward a
// page-token to the service behind it, so requesting a later page returns the
// first page again. Looping on that token would accumulate duplicates forever
// rather than terminating, so the first page is all that can be read.
func (s *GroupMembershipService) List(ctx context.Context, orgID, groupID string) ([]GroupMember, error) {
	var page PaginatedResponse[GroupMember]
	if err := s.client.GetV2(ctx, groupMembersRoute, &page, RouteParams(orgID, groupID)); err != nil {
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

	// The response is a {"message": ...} acknowledgement carrying nothing worth
	// recording, so it is not decoded.
	return s.client.PostV2(ctx, groupMembersRoute, groupMembersRequest{UserIDs: userIDs}, nil,
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

	return s.client.PostV2(ctx, groupRemoveMembersRoute, groupMembersRequest{UserIDs: userIDs}, nil,
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
