// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Checkout key types.
//
// The type accepted when creating a key and the type reported afterwards are not
// the same vocabulary: a key created as "user-key" is reported as
// "github-user-key". CheckoutKey.InputType maps the reported value back.
const (
	// CheckoutKeyTypeDeployKey is a key scoped to a single repository.
	CheckoutKeyTypeDeployKey = "deploy-key"
	// CheckoutKeyTypeUserKey is a key that carries the permissions of the user
	// who created it. Creating one requires a user API token.
	CheckoutKeyTypeUserKey = "user-key"
	// CheckoutKeyTypeGitHubUserKey is what the API reports for a key created as
	// CheckoutKeyTypeUserKey.
	CheckoutKeyTypeGitHubUserKey = "github-user-key"
)

// Checkout key fingerprint digests, as accepted by the digest query parameter
// when listing keys.
const (
	// CheckoutKeyDigestMD5 requests MD5 fingerprints, which is the API default.
	CheckoutKeyDigestMD5 = "md5"
	// CheckoutKeyDigestSHA256 requests SHA256 fingerprints.
	CheckoutKeyDigestSHA256 = "sha256"
)

// CheckoutKey is an SSH key CircleCI uses to check out a project's source.
//
// The JSON field names are hyphenated, unlike the rest of v2. CreatedAt is kept
// as the string the API sent (an RFC 3339 timestamp) so that it round-trips into
// Terraform state exactly as received.
type CheckoutKey struct {
	PublicKey   string `json:"public_key"`
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
	Preferred   bool   `json:"preferred"`
	CreatedAt   string `json:"created_at"`
}

// InputType returns the key's type in the vocabulary accepted when creating a
// key, so that a key read back from the API can be compared with what was
// configured. The API reports a user key as "github-user-key" but only accepts
// "user-key" on create.
func (k CheckoutKey) InputType() string {
	// The API rewrites a requested "user-key" to "<vcs-type>-user-key", so a
	// GitHub project reports "github-user-key" and a Bitbucket one reports
	// "bitbucket-user-key". Mapping only the GitHub form left the type
	// round-tripping wrong everywhere else, which shows up as a permanent diff.
	if strings.HasSuffix(k.Type, "-"+CheckoutKeyTypeUserKey) {
		return CheckoutKeyTypeUserKey
	}

	return k.Type
}

// checkoutKeyInput is the create request body.
type checkoutKeyInput struct {
	Type string `json:"type"`
}

// checkoutKeyRoute renders the checkout-key route for a project, addressing a
// single key when fingerprint is non-empty.
//
// The project slug is escaped and interpolated here rather than passed as a
// RouteParams value. RouteParams percent-escapes each value as one path segment,
// which turns the slug's separators into %2F; the API documents that as merely
// "may be URL-escaped", but encoded separators are rejected by intermediate
// proxies and do not match the route on CircleCI Server. Escaping each segment
// individually keeps the separators literal while still encoding a segment that
// contains a reserved character.
//
// Because the returned route already contains percent escapes, it must not be
// passed through fmt.Sprintf afterwards (a %2F would be read as a verb), so the
// fingerprint is escaped and appended here too and callers pass no RouteParams.
func checkoutKeyRoute(projectSlug, fingerprint string) (string, error) {
	slug, err := checkoutKeyProjectPath(projectSlug)
	if err != nil {
		return "", err
	}

	route := "/project/" + slug + "/checkout-key"
	if fingerprint != "" {
		// A SHA256 fingerprint contains "/" and "+", which the API expects
		// URL-encoded; PathEscape handles both.
		route += "/" + url.PathEscape(fingerprint)
	}

	return route, nil
}

// checkoutKeyProjectPath percent-escapes each segment of a project slug for use
// in a request path, leaving the separators literal. It rejects a slug that is
// not of the documented vcs-slug/org-name/repo-name shape, because the resulting
// request would otherwise fail with a confusing HTTP 404.
func checkoutKeyProjectPath(projectSlug string) (string, error) {
	if projectSlug == "" {
		return "", errors.New("circleci: project slug is empty, want the form vcs-slug/org-name/repo-name")
	}

	segments := strings.Split(projectSlug, "/")
	if len(segments) != 3 {
		return "", fmt.Errorf(
			"circleci: project slug %q has %d segments, want 3 of the form vcs-slug/org-name/repo-name",
			projectSlug, len(segments),
		)
	}

	for i, segment := range segments {
		if segment == "" {
			return "", fmt.Errorf(
				"circleci: project slug %q has an empty segment, want the form vcs-slug/org-name/repo-name",
				projectSlug,
			)
		}
		segments[i] = url.PathEscape(segment)
	}

	return strings.Join(segments, "/"), nil
}

// GetCheckoutKey fetches one checkout key by fingerprint. A missing key yields
// an error satisfying IsNotFound.
func (c *Client) GetCheckoutKey(ctx context.Context, projectSlug, fingerprint string) (*CheckoutKey, error) {
	route, err := checkoutKeyRoute(projectSlug, fingerprint)
	if err != nil {
		return nil, err
	}

	var key CheckoutKey
	if err := c.GetV2(ctx, route, &key); err != nil {
		return nil, err
	}

	return &key, nil
}

// ListCheckoutKeys returns every checkout key for a project, following v2
// pagination. digest selects the fingerprint digest (CheckoutKeyDigestMD5 or
// CheckoutKeyDigestSHA256) and may be empty to take the API default of MD5.
func (c *Client) ListCheckoutKeys(ctx context.Context, projectSlug, digest string) ([]CheckoutKey, error) {
	route, err := checkoutKeyRoute(projectSlug, "")
	if err != nil {
		return nil, err
	}

	return DrainV2(ctx, func(ctx context.Context, pageToken string) (PaginatedResponse[CheckoutKey], error) {
		var page PaginatedResponse[CheckoutKey]
		err := c.GetV2(ctx, route, &page, OptionalQuery("digest", digest), PageToken(pageToken))

		return page, err
	})
}

// CreateCheckoutKey creates a checkout key of keyType
// (CheckoutKeyTypeDeployKey or CheckoutKeyTypeUserKey) and returns it, including
// the fingerprint that identifies it from then on.
//
// Creating a user key requires a user API token; a project token is rejected.
func (c *Client) CreateCheckoutKey(ctx context.Context, projectSlug, keyType string) (*CheckoutKey, error) {
	route, err := checkoutKeyRoute(projectSlug, "")
	if err != nil {
		return nil, err
	}

	var key CheckoutKey
	if err := c.PostV2(ctx, route, checkoutKeyInput{Type: keyType}, &key); err != nil {
		return nil, err
	}

	return &key, nil
}

// DeleteCheckoutKey deletes a checkout key by fingerprint.
func (c *Client) DeleteCheckoutKey(ctx context.Context, projectSlug, fingerprint string) error {
	route, err := checkoutKeyRoute(projectSlug, fingerprint)
	if err != nil {
		return err
	}

	return c.DeleteV2(ctx, route)
}
