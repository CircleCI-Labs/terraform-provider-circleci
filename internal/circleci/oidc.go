// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"context"
	"net/http"
	"regexp"
	"strings"
)

// Routes for OIDC custom claims.
//
// The org-level and project-level routes differ only by the extra /project/{id}
// segment and are otherwise identical: same verbs, same request body, same
// response schema. oidcCustomClaimsRoute picks between them.
const (
	oidcOrgCustomClaimsRoute     = "/org/%s/oidc-custom-claims"
	oidcProjectCustomClaimsRoute = "/org/%s/project/%s/oidc-custom-claims"
)

// The names the API uses for individual claims. They are the only values the
// DELETE route's required "claims" query parameter accepts.
const (
	// OIDCClaimAudience is the "aud" claim of the identity token.
	OIDCClaimAudience = "audience"
	// OIDCClaimTTL is the lifetime of the identity token.
	OIDCClaimTTL = "ttl"
)

// OIDCTTLPattern matches the ttl values the OIDC claims API actually accepts:
// one or more unit-suffixed numbers with no separator, for example "1h", "90m"
// or "1h30m".
//
// This is deliberately *not* the pattern the published OpenAPI document
// advertises for its JSONDuration schema, which is
// `^([0-9]+(ms|s|m|h|d|w)){1,7}$`. That pattern is not enforced anywhere and
// does not describe what the API actually accepts: it takes exactly the
// durations Go's time.ParseDuration takes, which has no "d" or "w" unit at all. So a documented-looking "7d" or "1w" is accepted by the schema,
// accepted by a validator derived from the schema, and then rejected by the API
// with 400 at apply time — the plan passes and the apply fails, which is the
// wrong end of the pipeline for infrastructure as code.
//
// The units are therefore ParseDuration's: ns, us (or µs/μs), ms, s, m and h. A
// fractional value such as "1.5h" is accepted for the same reason. Values Go
// permits but that make no sense for a token lifetime are still excluded here —
// a negative or unsigned-plus duration, and the digitless forms ".5h" and "1.h".
var OIDCTTLPattern = regexp.MustCompile(`^([0-9]+(\.[0-9]+)?(ns|us|µs|μs|ms|s|m|h))+$`)

// OIDCCustomClaims is the claim customization served by
// GET /api/v2/org/{orgID}[/project/{projectID}]/oidc-custom-claims.
//
// Every field is optional in the response except OrgID: a scope with no
// customization answers 200 with only org_id (and project_id) set, rather than
// 404. Callers therefore cannot use IsNotFound to decide whether claims are
// configured; check IsZero instead.
type OIDCCustomClaims struct {
	// OrgID is the organization the claims apply to.
	OrgID string `json:"org_id"`
	// ProjectID is set only on the project-level response.
	ProjectID string `json:"project_id"`
	// Audience is the list of values placed in the token's "aud" claim.
	Audience []string `json:"audience"`
	// AudienceUpdatedAt is when the audience claim was last written.
	//
	// [NET, measured 2026-08-21] This is a tombstone, not a companion to
	// Audience: once a scope's audience has ever been written (customized, or
	// even just reset back to default), this timestamp stays populated forever
	// — including in a response where Audience itself is absent because the
	// claim is back at its default, and including in a response to a PATCH
	// that set only ttl and never mentioned audience at all. IsZero ignores
	// this field for exactly that reason: it is not a signal that the audience
	// claim is customized, only that it has a history.
	AudienceUpdatedAt string `json:"audience_updated_at"`
	// TTL is the token lifetime as a duration string, e.g. "1h30m".
	TTL string `json:"ttl"`
	// TTLUpdatedAt is when the ttl claim was last written. See
	// AudienceUpdatedAt: the same tombstone behavior applies to this field for
	// ttl.
	TTLUpdatedAt string `json:"ttl_updated_at"`
}

// IsZero reports whether no claim is customized in this scope, which is how the
// API represents "reset to defaults". It is the drift signal for a resource that
// manages these claims, because a reset answers 200 rather than 404.
//
// Deliberately excludes AudienceUpdatedAt and TTLUpdatedAt: those persist
// indefinitely once a scope has any history, so a scope that is genuinely back
// at its defaults can still carry non-empty timestamps (see
// OIDCCustomClaims.AudienceUpdatedAt). Checking them here would make IsZero
// report "still customized" forever after the first customization, which
// defeats its entire purpose as a drift signal.
func (c OIDCCustomClaims) IsZero() bool {
	return len(c.Audience) == 0 && c.TTL == ""
}

// OIDCCustomClaimsUpdate is the PATCH body. The route is a partial update, so a
// field that is nil is omitted and the corresponding claim is left untouched.
//
// Audience is a *[]string rather than a []string so that an explicitly empty
// audience can be sent: with a plain slice, omitempty would drop `[]` and the
// request would silently mean "leave the audience alone" instead of "make it
// empty".
type OIDCCustomClaimsUpdate struct {
	Audience *[]string `json:"audience,omitempty"`
	TTL      *string   `json:"ttl,omitempty"`
}

// IsEmpty reports whether the update carries no claim, meaning the request would
// change nothing. Callers skip the PATCH in that case.
func (u OIDCCustomClaimsUpdate) IsEmpty() bool {
	return u.Audience == nil && u.TTL == nil
}

// oidcCustomClaimsRoute returns the route and its parameters for a scope. An
// empty projectID selects the organization-level route.
func oidcCustomClaimsRoute(orgID, projectID string) (string, RequestOption) {
	if projectID == "" {
		return oidcOrgCustomClaimsRoute, RouteParams(orgID)
	}

	return oidcProjectCustomClaimsRoute, RouteParams(orgID, projectID)
}

// GetOIDCCustomClaims reads the custom claims for an organization, or for a
// project within it when projectID is non-empty.
func (c *Client) GetOIDCCustomClaims(ctx context.Context, orgID, projectID string) (*OIDCCustomClaims, error) {
	route, params := oidcCustomClaimsRoute(orgID, projectID)

	var claims OIDCCustomClaims
	if err := c.GetV2(ctx, route, &claims, params); err != nil {
		return nil, err
	}

	return &claims, nil
}

// UpdateOIDCCustomClaims applies a partial update to the custom claims of a
// scope and returns the claims as the API reports them afterwards.
//
// The API spells create and update the same way: PATCH creates the
// customization when none exists, so there is no separate POST.
func (c *Client) UpdateOIDCCustomClaims(
	ctx context.Context, orgID, projectID string, update OIDCCustomClaimsUpdate,
) (*OIDCCustomClaims, error) {
	route, params := oidcCustomClaimsRoute(orgID, projectID)

	var claims OIDCCustomClaims
	if err := c.PatchV2(ctx, route, update, &claims, params); err != nil {
		return nil, err
	}

	return &claims, nil
}

// DeleteOIDCCustomClaims resets the named claims of a scope to their defaults
// and returns whatever customization remains.
//
// claims must name at least one of OIDCClaimAudience and OIDCClaimTTL: the
// "claims" query parameter is required and the API rejects a request without it.
// Deleting is per-claim rather than all-or-nothing so that a caller managing
// only the audience does not also wipe a ttl it never set.
func (c *Client) DeleteOIDCCustomClaims(
	ctx context.Context, orgID, projectID string, claims []string,
) (*OIDCCustomClaims, error) {
	route, params := oidcCustomClaimsRoute(orgID, projectID)

	// DELETE carries a response body here, so it cannot go through DeleteV2,
	// which discards it.
	var remaining OIDCCustomClaims
	err := c.call(ctx, http.MethodDelete, "/api/v2"+route, nil, &remaining, []RequestOption{
		params,
		Query("claims", strings.Join(claims, ",")),
	})
	if err != nil {
		return nil, err
	}

	return &remaining, nil
}
