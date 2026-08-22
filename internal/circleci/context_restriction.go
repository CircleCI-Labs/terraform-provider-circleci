// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Context restriction routes. Restrictions are served by v2 on every deployment.
//
// Shapes and semantics here follow the API's own handlers and
// github.com/CircleCI-Public/circleci-cli's internal/apiclient/context.go
// (MIT), which agree on routes and field names.
const (
	contextRestrictionsRoute = "/context/%s/restrictions"
	contextRestrictionRoute  = "/context/%s/restrictions/%s"
)

// The kinds of restriction the API accepts.
const (
	// ContextRestrictionTypeProject restricts a context to one project.
	ContextRestrictionTypeProject = "project"
	// ContextRestrictionTypeGroup restricts a context to a group.
	//
	// [NET, measured against a GitHub App, a GitLab and two GitHub OAuth
	// organizations on 2026-08-21]: despite the name, this does not reach either
	// of CircleCI's two "group" concepts (a VCS security group or a
	// circleci_group RBAC group). The API accepts exactly one value — the
	// restricted context's own organization UUID — and only on an OAuth-backed
	// organization (a classic gh/<org> or bitbucket/<org> slug):
	//
	//   - On a standalone (circleci/<uuid>) organization, every create with this
	//     type answers 400 "This is only supported for OAuth orgs.", regardless
	//     of value.
	//   - On an OAuth-backed organization, a value other than the organization's
	//     own UUID answers 400 "Invalid restriction."
	//   - A value equal to the organization's own UUID succeeds (201), but is not
	//     a new, independent restriction: CircleCI already applies exactly that
	//     restriction, named "All members", to every context by default (visible
	//     on read even when no one has ever created one through this route), and
	//     the create response's id is the organization's UUID rather than a fresh
	//     one. Creating it again is an idempotent no-op, and the list never grows
	//     past one entry no matter how many times it is created.
	//
	// What we measured is narrow: through this create route, on an OAuth-backed
	// organization, the only value the API would accept was the organization's
	// own UUID — never a real VCS team. We did not measure this type against a
	// group whose value differs from the org UUID, and we make no claim about
	// what it can or cannot restrict in general. CircleCI's own documentation
	// describes contexts as governed by "security groups" (an org's VCS teams,
	// with "All members" as the permissive default), which is a members axis,
	// separate from the "project" restriction below; see the resource docs for
	// how the two combine.
	ContextRestrictionTypeGroup = "group"
	// ContextRestrictionTypeExpression restricts a context with an expression.
	//
	// [NET, measured on 2026-08-21]: the API checks the expression's grammar —
	// unbalanced quotes, an unrecognized operator (e.g. "===", "+") or a bare
	// malformed literal are all rejected with 400 "Invalid argument." at create
	// time — but it does not check that the fields the expression names actually
	// exist or hold a compatible type. "pipeline.this_field_does_not_exist ==
	// \"x\"" and "pipeline.number == \"abc\"" (a number field compared to a
	// string) are both accepted (201) exactly like a genuine expression. The one
	// partial exception observed is "pipeline.trigger_parameters", where any
	// dotted continuation is rejected regardless of depth or whether it exists —
	// a narrow allowlist on that one field, not general type-checking. A typo in
	// most other field names therefore creates a restriction CircleCI accepts
	// without complaint but that may never do what its author intended.
	ContextRestrictionTypeExpression = "expression"
)

// No route updates a restriction in place: create and delete are the only
// mutations. [NET, measured on 2026-08-21]: PATCH and PUT to
// contextRestrictionRoute both answer a bare 404 from the router itself (no
// v2 error envelope), meaning the route does not exist at all rather than
// merely rejecting the method. The provider resource reflects this by forcing
// replacement on every configurable attribute.

// ContextRestriction limits which projects, groups or pipeline conditions may
// use a context.
//
// ProjectID is only populated for ContextRestrictionTypeProject restrictions —
// the API sets it to a copy of RestrictionValue in that case and omits the field
// entirely otherwise, so it reads back as "" for group and expression
// restrictions.
type ContextRestriction struct {
	ID        string `json:"id"`
	ContextID string `json:"context_id"`
	// Name is the human-readable name of whatever the restriction points at, for
	// example a group's name. It is "" for expression restrictions.
	Name             string `json:"name"`
	RestrictionType  string `json:"restriction_type"`
	RestrictionValue string `json:"restriction_value"`
	ProjectID        string `json:"project_id"`
}

// ListContextRestrictions returns every restriction on a context.
//
// The response carries no next_page_token: this endpoint returns the whole set
// in one body, so there is nothing to drain. The result is nil when the context
// is unrestricted, but [NET, measured on 2026-08-21] that state is not where a
// context starts: creating a context (via CreateContext) also creates a
// ContextRestrictionTypeGroup restriction on it — visible here even though
// nothing ever called CreateContextRestriction — named "All members", with
// RestrictionValue equal to the context's own organization UUID. That
// restriction is not special or protected: DeleteContextRestriction removes
// it like any other, on both a standalone and an OAuth-backed organization,
// and CircleCI never recreates it.
//
// This is a members restriction, not a projects restriction: per CircleCI's
// documentation, a `group` restriction names a security group (an org's VCS
// team) that may use the context, and "All members" is the permissive
// default naming every member. A `project` restriction is a separate axis —
// which projects may use the context — and per CircleCI's documentation and
// support the two combine as an AND, so a context with "All members" plus a
// `project` restriction is usable by any org member, but only from the listed
// projects; it is not "usable by every project" as long as "All members" is
// present. Do not delete the default group restriction to "activate" a
// project restriction: removing every group restriction narrows the context
// to organization administrators only (per CircleCI's documentation) and
// breaks scheduled and bot-triggered pipelines, which hold no group
// membership. We measured the listing behaviour above; the enforcement
// semantics in this paragraph are sourced from CircleCI's documentation and
// support, not measured by us. To tell whether a context is genuinely
// group-restricted, check whether a `group` entry's RestrictionValue differs
// from the organization's own UUID — a value equal to the org UUID is the
// permissive default, not a real restriction.
func (c *Client) ListContextRestrictions(ctx context.Context, contextID string) ([]ContextRestriction, error) {
	var response itemsResponse[ContextRestriction]
	if err := c.GetV2(ctx, contextRestrictionsRoute, &response, RouteParams(contextID)); err != nil {
		return nil, err
	}

	return response.Items, nil
}

// CreateContextRestrictionRequest is the create body.
type CreateContextRestrictionRequest struct {
	RestrictionType  string `json:"restriction_type"`
	RestrictionValue string `json:"restriction_value"`
}

// CreateContextRestriction adds a restriction to a context and returns it as
// stored.
//
// The response never carries a "name": the create route's response has no
// such field — the human-readable name of whatever the restriction points at
// (e.g. a project's slug) is learned out of band and only reported by a later
// list/read. Callers
// must not treat the returned Name as meaningful; it decodes to "".
func (c *Client) CreateContextRestriction(
	ctx context.Context,
	contextID string,
	req CreateContextRestrictionRequest,
) (*ContextRestriction, error) {
	var created ContextRestriction
	if err := c.PostV2(ctx, contextRestrictionsRoute, req, &created, RouteParams(contextID)); err != nil {
		return nil, err
	}

	return &created, nil
}

// DeleteContextRestriction removes a restriction from a context.
//
// This route sits behind the same middleware as GetContext and
// DeleteContext (see context.go), so a context that no longer exists — and
// therefore a restriction that no longer exists along with it — answers 403
// rather than 404.
func (c *Client) DeleteContextRestriction(ctx context.Context, contextID, restrictionID string) error {
	return c.DeleteV2(ctx, contextRestrictionRoute, RouteParams(contextID, restrictionID))
}
