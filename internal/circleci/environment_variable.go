// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// ProjectEnvironmentVariable is an environment variable set on a project, as
// returned by GET /api/v2/project/{project-slug}/envvar.
//
// Value is always masked: the API answers with four "x" characters followed by
// the last four characters of the real value, matching what the CircleCI web UI
// displays. The configured value can never be read back, so this is only useful
// for confirming which variable is set, not what it is set to.
//
// CreatedAt is kept as the string the API sent (an RFC 3339 timestamp) so that
// it round-trips into Terraform state exactly as received. It is null for
// variables created before CircleCI began recording the timestamp.
type ProjectEnvironmentVariable struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	CreatedAt string `json:"created_at"`
}

// projectEnvVarRoute renders the envvar collection route for a project.
//
// It reuses checkoutKeyProjectPath, the slug escaper written for the
// checkout-key routes, because the constraint is identical: RouteParams
// percent-escapes each value as a single path segment, which would turn the
// slug's separators into %2F, and encoded separators are rejected by
// intermediate proxies and do not match the route on CircleCI Server. Escaping
// each segment individually keeps the separators literal.
//
// Because the returned route already contains percent escapes it must not be
// passed through fmt.Sprintf afterwards, so callers pass no RouteParams.
func projectEnvVarRoute(projectSlug string) (string, error) {
	slug, err := checkoutKeyProjectPath(projectSlug)
	if err != nil {
		return "", err
	}

	return "/project/" + slug + "/envvar", nil
}

// ListProjectEnvironmentVariables returns every environment variable set on a
// project, following pagination to the last page. Values are masked; see
// ProjectEnvironmentVariable.
//
// The result is nil when the project has no environment variables.
func (c *Client) ListProjectEnvironmentVariables(ctx context.Context, projectSlug string) ([]ProjectEnvironmentVariable, error) {
	route, err := projectEnvVarRoute(projectSlug)
	if err != nil {
		return nil, err
	}

	return DrainV2(ctx, func(ctx context.Context, pageToken string) (PaginatedResponse[ProjectEnvironmentVariable], error) {
		var page PaginatedResponse[ProjectEnvironmentVariable]
		err := c.GetV2(ctx, route, &page, PageToken(pageToken))

		return page, err
	})
}
