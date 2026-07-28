// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// User routes.
//
// These are v2 on every deployment. /me answers for whoever the token belongs to;
// /user/{id} looks up another user by their CircleCI id and is what the /me
// response, workflow "started by" fields and audit records all refer to.
const (
	currentUserRoute        = "/me"
	userRoute               = "/user/%s"
	userCollaborationsRoute = "/me/collaborations"
)

// User is a CircleCI user account.
//
// The response is flat, with no envelope. ID is the user's CircleCI id (the
// "analytics id" internally), not a VCS id, and it is the value /user/{id} takes.
type User struct {
	// ID is the user's CircleCI id (a UUID).
	ID string `json:"id"`
	// Login is the user's VCS login.
	Login string `json:"login"`
	// Name is the user's display name.
	Name string `json:"name"`
	// AvatarURL is the user's avatar on their VCS.
	AvatarURL string `json:"avatar_url"`
}

// Collaboration is an organization the authenticated user belongs to or can
// collaborate on.
//
// ID is a pointer because it is genuinely nullable: collaborations are assembled
// partly from the VCS rather than from CircleCI's own records, and an
// organization that exists on GitHub or Bitbucket but has never been used on
// CircleCI has no CircleCI id yet. Decoding it as a plain string would turn that
// null into "" and make an unknown organization indistinguishable from one whose
// id the server declined to send.
type Collaboration struct {
	// ID is the organization's CircleCI id (a UUID), or nil when CircleCI does
	// not know the organization yet.
	ID *string `json:"id"`
	// VCSType is the VCS backing the organization, for example "github",
	// "bitbucket" or "circleci".
	//
	// The field is vcs_type, not vcs-type: the v2 API declares this key in
	// kebab-case internally and converts it to snake_case on the way out.
	VCSType string `json:"vcs_type"`
	// Name is the organization name.
	Name string `json:"name"`
	// Slug is the organization slug, for example "gh/acme" or
	// "circleci/<org-uuid>".
	Slug string `json:"slug"`
	// AvatarURL is the organization's avatar on its VCS.
	AvatarURL string `json:"avatar_url"`
}

// UserService reads CircleCI user accounts.
//
// Read-only by design: CircleCI has no API for creating, updating or deleting a
// user, and organization membership is managed in the web UI.
type UserService struct {
	client *Client
}

// Users returns the user service for this client.
func (c *Client) Users() *UserService {
	return &UserService{client: c}
}

// Current fetches the user the configured API token authenticates as.
//
// The route answers 403 rather than 401 for a token that is not a user token, so
// a project or organization token surfaces as IsUnauthorized rather than as a
// missing user.
func (s *UserService) Current(ctx context.Context) (*User, error) {
	var user User
	if err := s.client.GetV2(ctx, currentUserRoute, &user); err != nil {
		return nil, err
	}

	// A 2xx body carrying no id is not a user. Treating it as missing keeps an
	// empty object out of Terraform state.
	if user.ID == "" {
		return nil, ErrNotFound
	}

	return &user, nil
}

// Get fetches a user by their CircleCI id. A missing user is reported as an error
// satisfying IsNotFound.
//
// Calling this still requires a user token: the route authorizes the caller, not
// the subject, so it cannot be used to enumerate users with a project token.
func (s *UserService) Get(ctx context.Context, userID string) (*User, error) {
	var user User
	if err := s.client.GetV2(ctx, userRoute, &user, RouteParams(userID)); err != nil {
		return nil, err
	}

	if user.ID == "" {
		return nil, ErrNotFound
	}

	return &user, nil
}

// ListCollaborations returns every organization the authenticated user is a
// member of or can collaborate on.
//
// The response is a bare JSON array, not the v2 items/next_page_token envelope,
// and the route does not paginate: the v2 API builds the whole set per request
// by fanning out to each connected VCS. So there is no DrainV2 here, and none is
// needed.
//
// The set is broader than "organizations I am a member of". It also includes the
// owner of any repository the user is a collaborator on without belonging to the
// organization, and the user's own personal account, which is why an entry may
// carry a nil id.
func (s *UserService) ListCollaborations(ctx context.Context) ([]Collaboration, error) {
	var collaborations []Collaboration
	if err := s.client.GetV2(ctx, userCollaborationsRoute, &collaborations); err != nil {
		return nil, err
	}

	return collaborations, nil
}
