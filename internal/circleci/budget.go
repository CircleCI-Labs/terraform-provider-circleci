// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Budget routes.
//
// Spend budgets have no public API at all. They are served under the private
// origin (see private.go) at /private/orgs/{orgUUID}/budgets — the same origin
// as storage retention controls (storage_retention.go), not the /api/private
// prefix on Client.Host that organization contacts uses (org_contacts.go). See
//
// There is no per-item GET: the only single-budget route is DELETE by
// budget_id. GET answers with the org's whole collection, so a specific budget
// is always located by scanning it for the org-level entry (ProjectID nil) or
// the one entry for a given project — see FindBudget.
//
// Shapes, routes and the upsert semantics of PUT come from
// AwesomeCICD/circleci-org-migration-cli (MIT): the
// maintainer's own migration CLI, which exercises these exact routes in
// production with a plain Circle-Token and is the only available
// specification for them.
const (
	budgetsRoute = "/private/orgs/%s/budgets"
	budgetRoute  = "/private/orgs/%s/budgets/%s"
)

// Budget enforcement types, as reported by the API.
//
// Both are read-only from this provider's point of view: the PUT request body
// (budgetSetRequest) carries only credits and project_id, never
// enforcement_type, so an existing budget's enforcement mode can be observed
// here but never changed. The migration CLI hits the identical limit — its
// exporter warns explicitly, for any budget captured with
// enforcement_type=block, that "the PUT budget endpoint only accepts
// credits — enforcement mode must be set manually on the destination".
const (
	// BudgetEnforcementWarn reports overage without blocking new workflows.
	BudgetEnforcementWarn = "warn"
	// BudgetEnforcementBlock stops new workflows once the budget is exceeded.
	BudgetEnforcementBlock = "block"
)

// Budget is one spend-budget entry: the organization-level budget when
// ProjectID is nil, or one project's budget when it is set.
//
// Consumption, Percentage and ThresholdExceeded are runtime statistics
// computed by CircleCI from actual spend. They are reported on every read and
// cannot be written — SetBudget's request never carries them.
type Budget struct {
	BudgetID          string  `json:"budget_id"`
	Credits           int     `json:"credits"`
	EnforcementType   string  `json:"enforcement_type"`
	ProjectID         *string `json:"project_id"`
	Consumption       int     `json:"consumption"`
	Percentage        float64 `json:"percentage"`
	ThresholdExceeded bool    `json:"threshold_exceeded"`
}

// budgetsResponse mirrors GET /private/orgs/{orgUUID}/budgets.
type budgetsResponse struct {
	Budgets []Budget `json:"budgets"`
}

// ListBudgets returns every spend-budget entry configured for an
// organization: the org-level budget (ProjectID nil), when one is configured,
// and any per-project budgets. The result is nil when the organization has no
// budgets at all.
//
// There is no pagination on this route — the whole set comes back in one
// response — and no filter, so this is also the only way to read a single
// budget back; see FindBudget.
//
// Private route, Cloud only — see private.go. Callers must gate this on
// Client.IsCloud themselves; nothing here does it, the same as every other
// method behind GetPrivate/PutPrivate/DeletePrivate.
func (c *Client) ListBudgets(ctx context.Context, orgID string) ([]Budget, error) {
	var resp budgetsResponse

	if err := c.GetPrivate(ctx, budgetsRoute, &resp, RouteParams(orgID)); err != nil {
		return nil, err
	}

	return resp.Budgets, nil
}

// FindBudget locates one budget within an organization's list: the org-level
// budget when projectID is nil, or the one entry whose ProjectID equals
// *projectID otherwise.
//
// Returns (nil, false, nil) when no entry matches that scope — this is an
// expected state ("no budget configured here yet"), not a failure of the
// request, so it is reported as a boolean rather than IsNotFound.
func (c *Client) FindBudget(ctx context.Context, orgID string, projectID *string) (*Budget, bool, error) {
	budgets, err := c.ListBudgets(ctx, orgID)
	if err != nil {
		return nil, false, err
	}

	for i := range budgets {
		if budgetScopeMatches(budgets[i].ProjectID, projectID) {
			return &budgets[i], true, nil
		}
	}

	return nil, false, nil
}

// budgetScopeMatches reports whether two budget scopes are the same one: both
// nil (the org-level budget) or both set to the same project id.
func budgetScopeMatches(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}

	return *a == *b
}

// budgetSetRequest is the body for PUT /private/orgs/{orgUUID}/budgets.
//
// This is deliberately everything the request carries. There is no
// enforcement_type field because the API does not accept one — see the
// BudgetEnforcement constants' doc comment.
type budgetSetRequest struct {
	Credits   int     `json:"credits"`
	ProjectID *string `json:"project_id"`
}

// SetBudget creates or updates a budget via PUT: pass projectID nil for the
// organization-level budget, or a project UUID for a per-project budget.
//
// This is an upsert. A second call for the same (orgID, projectID) scope
// updates the existing budget's credits in place rather than creating a
// duplicate — matching the migration CLI's own doc comment on the identical
// method ("SetBudget creates or updates a budget via PUT") and its sync
// path, which calls it unconditionally on every run without first checking
// whether a budget already exists for that scope.
//
// The response carries nothing usable: the migration CLI decodes it into a
// discarded map[string]any, and its own tests model the response as an empty
// JSON object. In particular, the assigned or existing budget_id is never
// returned here — call FindBudget afterward to learn it, the same pattern
// SetStorageRetention uses for the sibling private route that also answers
// with nothing on write.
func (c *Client) SetBudget(ctx context.Context, orgID string, projectID *string, credits int) error {
	body := budgetSetRequest{Credits: credits, ProjectID: projectID}

	return c.PutPrivate(ctx, budgetsRoute, body, nil, RouteParams(orgID))
}

// DeleteBudget removes one budget by its server-assigned id. This is the only
// route that addresses a single budget directly; there is no equivalent GET.
func (c *Client) DeleteBudget(ctx context.Context, orgID, budgetID string) error {
	return c.DeletePrivate(ctx, budgetRoute, RouteParams(orgID, budgetID))
}
