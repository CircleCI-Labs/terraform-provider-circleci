// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Project group routes.
//
// Unlike the group routes in group.go, these sit under /organizations/{org_id}
// /projects/{project_id}/, which a CircleCI Server installation does not forward
// to the public API service. They are therefore Cloud-only in practice; see the
// Availability section of the docs for circleci_project_group.
const (
	projectGroupsRoute    = "/organizations/%s/projects/%s/groups"
	projectGroupRoleRoute = "/organizations/%s/projects/%s/groups/%s/update-role"
)

// Project-level roles a group can hold on a project.
//
// These are the only three values the API accepts for a project grant. The
// org-level roles (org-admin, org-contributor, org-viewer) are not valid here
// even though some API examples show one.
const (
	// ProjectRoleAdmin grants read and write access to the project and all its
	// settings, including managing other subjects' access.
	ProjectRoleAdmin = "project-admin"
	// ProjectRoleContributor grants read and write access to the project and
	// some of its settings.
	ProjectRoleContributor = "project-contributor"
	// ProjectRoleViewer grants read-only access to the project and some of its
	// settings.
	ProjectRoleViewer = "project-viewer"
)

// ProjectRoles returns every valid project role, for validating configuration.
func ProjectRoles() []string {
	return []string{ProjectRoleAdmin, ProjectRoleContributor, ProjectRoleViewer}
}

// ProjectGroup is a group granted a role on a project. Every member of the group
// holds that role on the project.
type ProjectGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// assignProjectGroupsRequest is the body of a project group assignment. The role
// applies to every group in GroupIDs.
type assignProjectGroupsRequest struct {
	Role     string   `json:"role"`
	GroupIDs []string `json:"group_ids"`
}

// updateProjectGroupRoleRequest is the body of a role change on an existing
// project grant.
type updateProjectGroupRoleRequest struct {
	Role string `json:"role"`
}

// ProjectGroupService reads and writes the groups granted access to a project.
//
// There is no endpoint for revoking a grant. The public API routes only a list,
// an assign and a role update; a delete is described in the API's OpenAPI
// definition but is not served, so removing a group from a project is only
// possible in the CircleCI web UI. Callers must surface that rather than
// silently appearing to revoke.
type ProjectGroupService struct {
	client *Client
}

// ProjectGroups returns the project group service for this client.
func (c *Client) ProjectGroups() *ProjectGroupService {
	return &ProjectGroupService{client: c}
}

// List fetches every group granted a role on a project, following pagination to
// the last page. The result is nil when no groups are assigned.
func (s *ProjectGroupService) List(ctx context.Context, orgID, projectID string) ([]ProjectGroup, error) {
	return DrainV2(ctx, func(ctx context.Context, pageToken string) (PaginatedResponse[ProjectGroup], error) {
		var page PaginatedResponse[ProjectGroup]
		err := s.client.GetV2(ctx, projectGroupsRoute, &page,
			RouteParams(orgID, projectID),
			PageToken(pageToken),
		)

		return page, err
	})
}

// Get fetches a single group's grant on a project.
//
// There is no route for one project group, so this filters the list. A group
// with no grant on the project is reported as an error satisfying IsNotFound,
// which is what lets a revoked grant be detected as drift.
func (s *ProjectGroupService) Get(ctx context.Context, orgID, projectID, groupID string) (*ProjectGroup, error) {
	groups, err := s.List(ctx, orgID, projectID)
	if err != nil {
		return nil, err
	}

	for _, group := range groups {
		if group.ID == groupID {
			return &group, nil
		}
	}

	return nil, ErrNotFound
}

// Assign grants role on a project to every group in groupIDs.
//
// The call is an upsert: a group that already has a grant has its role replaced,
// so this doubles as a way to set the role at creation time. An empty groupIDs
// makes no request, because the endpoint rejects an empty array.
func (s *ProjectGroupService) Assign(ctx context.Context, orgID, projectID, role string, groupIDs []string) error {
	if len(groupIDs) == 0 {
		return nil
	}

	// The response is a {"message": ...} acknowledgement rather than the stored
	// grant, so it is not decoded; callers re-read to learn the group's name.
	return s.client.PostV2(ctx, projectGroupsRoute,
		assignProjectGroupsRequest{Role: role, GroupIDs: groupIDs}, nil,
		RouteParams(orgID, projectID),
	)
}

// UpdateRole changes the role a single group holds on a project.
//
// This is a distinct action route rather than a PUT or PATCH, following the same
// /update-style convention the v3 API uses for partial updates.
func (s *ProjectGroupService) UpdateRole(ctx context.Context, orgID, projectID, groupID, role string) error {
	return s.client.PostV2(ctx, projectGroupRoleRoute,
		updateProjectGroupRoleRequest{Role: role}, nil,
		RouteParams(orgID, projectID, groupID),
	)
}
