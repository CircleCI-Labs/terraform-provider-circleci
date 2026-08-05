// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// GitHub App routes.
//
// These are served by a backend service behind a proxy. They are declared in
// that backend's own OpenAPI definitions purely so the request validation
// middleware accepts them, under a comment marking them "Internal / CLI-only [...]
// intentionally NOT customer-facing: they are excluded from the public docs
// bundles". They are therefore absent from the published OpenAPI spec and from
// the CircleCI API reference, and may change or disappear without notice.
//
// The provider reads them anyway because there is no published alternative: both
// circleci_pipeline and circleci_trigger require a repository's numeric GitHub
// id, and without this route the only way to obtain one is to copy it out of the
// GitHub UI by hand.
const githubAppRepositoriesRoute = "/github-app/organization/%s/repositories"

// githubAppInstallationRoute reports whether, and how, the CircleCI GitHub App
// is installed for an organization. It sits under the same "Internal /
// CLI-only [...] intentionally NOT customer-facing" comment block as
// githubAppRepositoriesRoute in the backend's own OpenAPI definitions, so the
// same unpublished-API caveat applies.
const githubAppInstallationRoute = "/github-app/organization/%s/installation"

// githubAppRepositoryPageLimit is the page size requested when listing
// repositories. The route documents 100 as its maximum and also as its default,
// so this asks for the largest page the API will serve.
const githubAppRepositoryPageLimit = 100

// githubAppRepositoryPageCap bounds the number of pages a single list will
// fetch. The route paginates by 1-based page number rather than by an opaque
// cursor, so there is no server-supplied "no more pages" signal that cannot be
// forged by a misbehaving proxy echoing the same page forever. The cap turns
// that into a bounded error instead of a hung apply. At the maximum page size it
// allows 100,000 repositories, far beyond any real GitHub organization.
const githubAppRepositoryPageCap = 1000

// GitHubAppRepository is a repository the CircleCI GitHub App can access.
//
// ID is the repository's numeric id on GitHub. That is the value
// circleci_pipeline's config_source_repo_external_id and
// checkout_source_repo_external_id, and circleci_trigger's
// event_source_repo_external_id, all expect.
//
// Note that the full name is sent as repo_full_name, not full_name: the field
// names here are taken directly from the backend's response shape and its own
// schema, not guessed from the shape of neighbouring routes.
type GitHubAppRepository struct {
	// ID is the numeric GitHub repository id, used as the repository external id
	// in pipeline definitions and triggers.
	ID int64 `json:"id"`
	// FullName is the fully-qualified "owner/repo" name.
	FullName string `json:"repo_full_name"`
	// Name is the repository name without its owner.
	Name string `json:"repo_name"`
	// Owner is the GitHub account that owns the repository.
	Owner string `json:"owner"`
	// DefaultBranch is the repository's default branch.
	DefaultBranch string `json:"default_branch"`
	// Private reports whether the repository is private on GitHub.
	Private bool `json:"private"`
}

// ExternalID returns the repository id in the string form the pipeline and
// trigger resources take, so callers do not each repeat the conversion.
func (r GitHubAppRepository) ExternalID() string {
	return strconv.FormatInt(r.ID, 10)
}

// githubAppRepositoryPage is one page of the repositories response:
//
//	{"items": [...], "total_count": N}
//
// This is not the v2 PaginatedResponse envelope. The route paginates with page
// and limit query parameters and reports a total rather than a
// next_page_token, so PaginatedResponse would decode items and then silently
// stop after the first page.
type githubAppRepositoryPage struct {
	Items      []GitHubAppRepository `json:"items"`
	TotalCount int                   `json:"total_count"`
}

// GitHubAppInstallation is the CircleCI GitHub App installation for an
// organization, as served by GET
// /api/v2/github-app/organization/{org_id}/installation.
//
// Field names are taken from the backend's own "installation" schema and
// confirmed against its test fixture, not guessed.
type GitHubAppInstallation struct {
	// ID is the GitHub App installation's own numeric id (distinct from any
	// repository id).
	ID int64 `json:"id"`
	// TargetType is the kind of GitHub account the app is installed on:
	// "Organization" or "User".
	TargetType string `json:"target_type"`
	// Login is the GitHub account login (organization or user) the installation
	// belongs to.
	Login string `json:"login"`
	// RepositorySelection is "all" when the installation can reach every
	// repository the account owns, or "selected" when it is limited to a chosen
	// subset. circleci_github_app_repository and _repositories only ever report
	// the subset actually granted either way.
	RepositorySelection string `json:"repository_selection"`
}

// GitHubAppService reads the CircleCI GitHub App's view of an organization.
//
// There is deliberately no create, update or delete here, and no resource built
// on top of this service: a GitHub App installation and the repositories it can
// reach are configured on GitHub, not in CircleCI. CircleCI's own install route
// only hands back a redirect URL for a human to open, which is not something
// Terraform can converge on.
type GitHubAppService struct {
	client *Client
}

// GitHubApp returns the GitHub App service for this client.
func (c *Client) GitHubApp() *GitHubAppService {
	return &GitHubAppService{client: c}
}

// GetInstallation returns the CircleCI GitHub App installation for an
// organization. It answers ErrNotFound (via an ordinary HTTP 404, confirmed
// against the backend's own "GitHub App is not installed" test case) when no
// installation exists — not the 403 anti-enumeration pattern circleci_group
// uses, so IsNotFound alone is sufficient here.
func (s *GitHubAppService) GetInstallation(ctx context.Context, orgID string) (*GitHubAppInstallation, error) {
	var installation GitHubAppInstallation
	if err := s.client.GetV2(ctx, githubAppInstallationRoute, &installation, RouteParams(orgID)); err != nil {
		return nil, err
	}

	return &installation, nil
}

// ListRepositories returns every repository the CircleCI GitHub App can access
// for an organization, following pagination to the last page.
//
// The result is nil when the app has no repositories, or when no installation
// exists at all — the route answers 200 with an empty items array in the latter
// case rather than 404, so callers that need to distinguish "no installation"
// from "installation with no repositories" cannot do it from here.
func (s *GitHubAppService) ListRepositories(ctx context.Context, orgID string) ([]GitHubAppRepository, error) {
	var all []GitHubAppRepository

	for page := 1; page <= githubAppRepositoryPageCap; page++ {
		var body githubAppRepositoryPage

		err := s.client.GetV2(ctx, githubAppRepositoriesRoute, &body,
			RouteParams(orgID),
			Query("page", strconv.Itoa(page)),
			Query("limit", strconv.Itoa(githubAppRepositoryPageLimit)),
		)
		if err != nil {
			return nil, err
		}

		all = append(all, body.Items...)

		// An empty page always terminates: no progress was made, so another request
		// would ask the same question again. This is what stops a server that ignores
		// the page parameter from looping forever.
		if len(body.Items) == 0 {
			return all, nil
		}

		// total_count is the real terminator. Deciding instead that "a page shorter
		// than the requested limit is the last page" looks equivalent but is not: a
		// server free to serve a smaller page than asked for would end the drain after
		// one request and silently return a truncated list, which is worse than any
		// error because nothing reports it.
		if body.TotalCount > 0 {
			if len(all) >= body.TotalCount {
				return all, nil
			}

			continue
		}

		// With no total to go by, a short page is the only available signal.
		if len(body.Items) < githubAppRepositoryPageLimit {
			return all, nil
		}
	}

	return nil, fmt.Errorf(
		"circleci: GitHub App repositories for organization %s did not end after %d pages of %d",
		orgID, githubAppRepositoryPageCap, githubAppRepositoryPageLimit,
	)
}

// FindRepository returns the repository whose full name is fullName, in
// "owner/repo" form. An unmatched name yields an error satisfying IsNotFound.
//
// The comparison is case-insensitive because GitHub treats owner and repository
// names that way, and repo_full_name preserves whatever casing the repository was
// created with. Requiring an exact match would make the lookup fail for a
// configuration that spells the name in a different case than GitHub stored it,
// which is the sort of difference practitioners cannot see from Terraform.
//
// The route has no filter parameter, so this lists and matches client-side.
func (s *GitHubAppService) FindRepository(ctx context.Context, orgID, fullName string) (*GitHubAppRepository, error) {
	repositories, err := s.ListRepositories(ctx, orgID)
	if err != nil {
		return nil, err
	}

	for _, repository := range repositories {
		if strings.EqualFold(repository.FullName, fullName) {
			return &repository, nil
		}
	}

	return nil, ErrNotFound
}
