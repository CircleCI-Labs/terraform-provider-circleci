// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// Project is a CircleCI project.
type Project struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Slug             string     `json:"slug"`
	OrganizationName string     `json:"organization_name"`
	OrganizationSlug string     `json:"organization_slug"`
	OrganizationID   string     `json:"organization_id"`
	VCSInfo          ProjectVCS `json:"vcs_info"`
}

// ProjectVCS describes where a project's code lives.
type ProjectVCS struct {
	VCSURL        string `json:"vcs_url"`
	Provider      string `json:"provider"`
	DefaultBranch string `json:"default_branch"`
}

// projectSlugSegments is the number of segments in a project slug,
// "vcs-type/org/project".
const projectSlugSegments = 3

// GetProject returns the project identified by slug, e.g. "gh/acme/repo" or
// "circleci/<orgUUID>/<projectUUID>".
//
// The route itself also accepts a bare project UUID in place of the slug — the
// handler matches the slug pattern first and falls back to a lookup by id — but
// this client requires the three-segment form, because every caller holds a slug
// and a single-segment argument would be ambiguous with a malformed slug. If a
// project ever needs addressing by id alone, that is a new method rather than a
// loosening of projectSlugPath.
func (c *Client) GetProject(ctx context.Context, slug string) (*Project, error) {
	path, err := projectSlugPath(slug)
	if err != nil {
		return nil, err
	}

	var project Project
	if err := c.GetV2(ctx, "/project/"+path, &project); err != nil {
		return nil, err
	}

	return &project, nil
}

// CreateProject creates a project in an organization.
//
// This replaces circleci-sdk-go's project.Create for two reasons. The SDK sent the
// follow request below to a hardcoded https://circleci.com, ignoring the
// configured host, which made project creation impossible against CircleCI Server.
// And the SDK's settings struct has no field for build_prs_only, so that setting
// was unreachable.
func (c *Client) CreateProject(ctx context.Context, organizationID, name string) (*Project, error) {
	var project Project

	err := c.PostV2(ctx, "/organization/%s/project", map[string]string{"name": name}, &project,
		RouteParams(organizationID),
	)
	if err != nil {
		return nil, err
	}

	if err := c.followProject(ctx, &project); err != nil {
		return nil, err
	}

	return &project, nil
}

// followProject follows a newly created project when the organization requires it.
//
// A standalone (`circleci/<uuid>`) organization follows the project as part of
// creating it. A classic GitHub or Bitbucket organization does not, and an
// unfollowed project never runs. The only route that follows a project is v1.1, so
// this is the provider's one remaining v1.1 dependency.
//
// The condition mirrors the API's own: the slug's middle segment is the
// organization *name* for a classic organization, and a UUID for a standalone one,
// so comparing it to OrganizationName distinguishes them without a second lookup.
func (c *Client) followProject(ctx context.Context, project *Project) error {
	segments := strings.Split(project.Slug, "/")
	if len(segments) != projectSlugSegments || segments[1] != project.OrganizationName {
		return nil
	}

	// Unlike the SDK, this goes to the configured host, so it works against
	// CircleCI Server.
	err := c.PostV1(ctx, "/project/%s/%s/%s/follow", nil, nil,
		RouteParams(strings.ToLower(project.VCSInfo.Provider), segments[1], project.Name),
	)
	if err != nil {
		return fmt.Errorf("following project %q after creating it: %w", project.Slug, err)
	}

	return nil
}

// DeleteProject deletes the project identified by slug.
func (c *Client) DeleteProject(ctx context.Context, slug string) error {
	path, err := projectSlugPath(slug)
	if err != nil {
		return err
	}

	return c.DeleteV2(ctx, "/project/"+path)
}

// projectSlugPath escapes a project slug for use in a route.
//
// Each segment is escaped separately and joined with literal slashes: escaping the
// slug as a single route parameter would turn its separators into %2F and never
// match the route. The result is interpolated into the path directly rather than
// through a format string, since it may now contain % sequences.
//
// A segment that is exactly "." or ".." is rejected outright: url.PathEscape does
// not touch dots, and url.Parse does not clean dot-segments out of a path, so
// either one would put a literal "./" or "../" into the request path and
// retarget it at a different route. This is defence in depth rather than a fix
// for a reachable bug — a slug comes from Terraform configuration or from
// CircleCI's own API responses, never from a third party — but rejecting it here
// costs nothing and keeps the escaping's guarantee honest.
func projectSlugPath(slug string) (string, error) {
	segments := strings.Split(slug, "/")
	if len(segments) != projectSlugSegments {
		return "", fmt.Errorf(
			"invalid project slug %q: expected three segments, \"vcs-type/org-name/project-name\"",
			slug,
		)
	}

	escaped := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment == "" {
			return "", fmt.Errorf("invalid project slug %q: it has an empty segment", slug)
		}
		if segment == "." || segment == ".." {
			return "", fmt.Errorf("invalid project slug %q: segment %q is not allowed", slug, segment)
		}

		escaped = append(escaped, url.PathEscape(segment))
	}

	return strings.Join(escaped, "/"), nil
}
