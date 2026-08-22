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
// This is an upsert in the sense that matters to a caller — a second call for
// the same (orgID, projectID) scope still leaves exactly one entry in that
// scope, not two — but [NET] measurement against gh-app-cci-1
// (2026-08-21) shows it is not an upsert of the underlying record: every PUT
// for an existing scope, including one that resends the same credits with no
// change at all, deletes the old budget and creates a new one with a freshly
// minted budget_id. Three consecutive PUTs with credits 2000, 2000, 3000
// against the same scope came back as three different budget_ids in a row —
// e.g. 11111111-…, 22222222-…, 33333333-… (illustrative shape; the actual
// values were ordinary random UUIDs) — a different id every time, even when
// nothing changed. Nothing
// about the request distinguishes "first write for this scope" from
// "overwrite" from the caller's side; the server decides it downstream of
// project_id-based matching. Callers must never treat a budget's id as stable
// across a second write to its own scope — see budget_resource.go's Create/
// Update, which re-reads the id after every write rather than assuming it, and
// the schema's own note not to rely on `id` as a durable handle.
//
// The response is not empty the way an earlier version of this comment
// claimed (and the migration CLI's own tests, which model it as `{}`, agreed
// with): [NET] measurement shows a 200 with a JSON body carrying
// credits, budget_id, enforcement_type and project_id — e.g.
// `{"credits":1000,"budget_id":"<uuid>","enforcement_type":"warn","project_id":null}`.
// What it does not carry is consumption, percentage or threshold_exceeded — so
// FindBudget is still called afterward, not because the write response is
// useless, but because those three runtime statistics have no other source.
// This client does not currently decode the write response at all, matching
// the migration CLI's own map[string]any-and-discard behaviour; a future
// change could use it to learn the id one write sooner, at the cost of a
// second, differently-shaped success type to maintain.
func (c *Client) SetBudget(ctx context.Context, orgID string, projectID *string, credits int) error {
	body := budgetSetRequest{Credits: credits, ProjectID: projectID}

	return c.PutPrivate(ctx, budgetsRoute, body, nil, RouteParams(orgID))
}

// DeleteBudget removes one budget by its server-assigned id. This is the only
// route that addresses a single budget directly; there is no equivalent GET.
//
// [NET] measurement (gh-app-cci-1, 2026-08-21): deleting an id that does not
// currently exist — either never created, or already deleted — answers 500
// with `{"error":"There was an error deleting the budget"}`, not 404. This is
// true both for a syntactically valid but unknown UUID and for the id of a
// budget this same client just deleted. IsNotFound(err) is therefore false for
// that case: there is no status-code way to tell "already gone" apart from a
// genuine server error on this route. Combined with the id churn documented on
// SetBudget above, a caller cannot even assume "the id I have is the current
// one for this scope" going into the delete. budgetResource.Delete corroborates
// by re-listing the scope (FindBudget) rather than trusting this error's status
// code — see its doc comment.
func (c *Client) DeleteBudget(ctx context.Context, orgID, budgetID string) error {
	return c.DeletePrivate(ctx, budgetRoute, RouteParams(orgID, budgetID))
}
