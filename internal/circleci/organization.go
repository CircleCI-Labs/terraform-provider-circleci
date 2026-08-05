// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Organization routes.
//
// Organizations are served by v2 on every deployment. The route segment is
// documented as "org-slug-or-id": it accepts either a UUID or a VCS slug such
// as "gh/acme" or "circleci/<uuid>".
//
// Unlike the project, checkout-key and environment-variable routes, this one
// is deliberately built to also accept a percent-encoded slug separator: the
// API's own route-matching regex accepts "/", "%2f" or "%2F" as the
// separator, specifically so a slug can be interpolated as a single path
// segment. That makes RouteParams's percent-escaping safe here, where it would
// break the routes above.
const (
	organizationsRoute = "/organization"
	organizationRoute  = "/organization/%s"
)

// Organization is a CircleCI organization, as returned by
// POST /api/v2/organization and GET /api/v2/organization/{org-slug-or-id}.
//
// The create and get routes are both reverse-proxied straight through to the
// core v2 API backend: both answer with exactly {id, name, slug, vcs_type},
// matching circleci-cli's (github.com/CircleCI-Public/circleci-cli, MIT)
// internal/apiclient.OrgInfo.
type Organization struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	VCSType string `json:"vcs_type"`
}

// OrganizationInput is the create body for an organization: {name, vcs_type}.
//
// POST /api/v2/organization is a find-or-create for a VCS-backed organization
// (vcs_type "github" or "bitbucket"): the create route only verifies the
// caller has admin access on the VCS and syncs CircleCI's record of an
// organization that already exists there. Only vcs_type "circleci" genuinely
// creates a new organization. See organizationIsStandalone in
// internal/provider/organization_resource.go and
// "Destroy must mirror what create actually did" in DESIGN.md: Delete must
// not be called for an adopted organization, because it deletes every project
// in it and all of their build history.
type OrganizationInput struct {
	Name    string `json:"name"`
	VCSType string `json:"vcs_type"`
}

// The three VCS types the create route accepts for an organization.
//
// These are the only values the create route's input validation permits —
// it is an exact set, checked before the handler body runs, and it rejects
// anything else again by name ("Invalid VCS type … Must be one of:
// github, bitbucket, circleci"). In particular the slug abbreviations "gh" and
// "bb" are *not* accepted here, even though the /organization/{org-slug-or-id}
// route's own regex takes them in a slug. Callers should reject an unlisted
// value before making the request rather than surface a 400.
const (
	// OrganizationVCSTypeGitHub is a GitHub-backed organization. Creating one is
	// a find-or-create: see OrganizationInput.
	OrganizationVCSTypeGitHub = "github"
	// OrganizationVCSTypeBitbucket is a Bitbucket-backed organization, with the
	// same find-or-create behaviour as GitHub.
	OrganizationVCSTypeBitbucket = "bitbucket"
	// OrganizationVCSTypeStandalone is a CircleCI-native (standalone)
	// organization. This is the only type a create genuinely creates, and so the
	// only type a destroy may delete.
	OrganizationVCSTypeStandalone = "circleci"
)

// OrganizationVCSTypes returns every VCS type an organization can be created
// with, for validating configuration.
func OrganizationVCSTypes() []string {
	return []string{
		OrganizationVCSTypeGitHub,
		OrganizationVCSTypeBitbucket,
		OrganizationVCSTypeStandalone,
	}
}

// CreateOrganization creates — or, for a VCS-backed organization, adopts — an
// organization and returns it as the API reports it.
func (c *Client) CreateOrganization(ctx context.Context, input OrganizationInput) (*Organization, error) {
	var created Organization
	if err := c.PostV2(ctx, organizationsRoute, input, &created); err != nil {
		return nil, err
	}

	return &created, nil
}

// GetOrganization returns one organization by slug or id.
//
// A missing organization and one the caller cannot view both answer 404 with
// "Org not found.": the route answers the same not-found response whether
// the lookup found nothing or the caller lacks permission to view it, so
// this is anti-enumeration by design — the opposite of the 403-for-missing
// behaviour DESIGN.md notes for groups. Either way it is reported as an
// error satisfying IsNotFound.
func (c *Client) GetOrganization(ctx context.Context, slugOrID string) (*Organization, error) {
	var found Organization
	if err := c.GetV2(ctx, organizationRoute, &found, RouteParams(slugOrID)); err != nil {
		return nil, err
	}

	return &found, nil
}

// DeleteOrganization deletes an organization by slug or id.
//
// The delete route answers 202 Accepted. It tears down the organization's
// VCS connections and — per its own documented description — "will delete
// all projects including all build data for the organization." Callers must
// only invoke this for an organization the provider genuinely created rather
// than adopted; see OrganizationInput and organizationIsStandalone.
//
// It can also fail with 403 if the organization is covered by a paid plan
// ("Cannot delete a paid organization. Please downgrade your plan to a Free
// plan in order to delete this organization.") or if the caller can view the
// organization but not manage it.
func (c *Client) DeleteOrganization(ctx context.Context, slugOrID string) error {
	return c.DeleteV2(ctx, organizationRoute, RouteParams(slugOrID))
}
