// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

// circleci_budget and circleci_budgets are served entirely on
// Client.PrivateHost(), which (deliberately — see internal/circleci/private.go)
// has no provider-schema attribute to redirect at a local fake, unlike
// Client.Host() or RunnerHost(). So, like circleci_group_membership's own fake
// tests, these cannot drive the resource through resource.UnitTest and an HCL
// "provider" block: there would be no way to stop the request leaving for the
// real private origin. Instead they call Create/Read/Update/Delete/Import
// directly against a resource or data source built with
// circleci.New(circleci.Config{PrivateHost: fake.URL}).
//
// The fake deliberately does NOT reject unknown request fields: nothing in the
// migration CLI's own tests (the only specification this route has) suggests
// the service is strict, and every other private route this provider talks to
// that IS documented as lenient (contexts, per DESIGN.md) says the API ignores
// keys it does not recognise. Making the fake stricter than that would risk a
// false failure rather than catch a real one.

// fakeBudgetEntry is one budget as the fake API stores it.
type fakeBudgetEntry struct {
	budgetID          string
	credits           int
	enforcementType   string
	projectID         *string
	consumption       int
	percentage        float64
	thresholdExceeded bool
}

// fakeBudgetAPI is an in-memory stand-in for the private budgets routes
// (internal/circleci/budget.go).
type fakeBudgetAPI struct {
	t *testing.T

	mu sync.Mutex
	// budgets maps org id to that org's budgets, in insertion order — the same
	// shape GET returns.
	budgets map[string][]*fakeBudgetEntry
	// missingOrgs makes GET for these org ids answer 404, simulating an org this
	// token cannot see.
	missingOrgs map[string]bool
	nextSeq     int

	// requests is every request the fake saw, as "METHOD /path", in order.
	requests []string
}

// newFakeBudgetAPI starts a fake budgets API and returns it alongside its
// origin.
func newFakeBudgetAPI(t *testing.T) (*fakeBudgetAPI, string) {
	t.Helper()

	api := &fakeBudgetAPI{
		budgets:     map[string][]*fakeBudgetEntry{},
		missingOrgs: map[string]bool{},
	}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	return api, srv.URL
}

// client returns a *circleci.Client whose private origin is this fake, and
// whose main host is deliberately unroutable — every budget call must go to
// PrivateHost, never Host.
func (a *fakeBudgetAPI) client(host string) *circleci.Client {
	return circleci.New(circleci.Config{
		Host:        "http://127.0.0.1:1",
		PrivateHost: host,
		Token:       "fake",
		Deployment:  circleci.DeploymentCloud,
	})
}

// seed puts a fully-formed budget into an org's collection behind Terraform's
// back, for tests that need to start from an existing entry (drift, read,
// delete, import).
func (a *fakeBudgetAPI) seed(orgID string, entry fakeBudgetEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()

	stored := entry
	a.budgets[orgID] = append(a.budgets[orgID], &stored)
}

// removeOrg makes GET for orgID answer 404, as if the token had lost access.
func (a *fakeBudgetAPI) removeOrg(orgID string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.missingOrgs[orgID] = true
}

// entries returns a snapshot of one org's budgets.
func (a *fakeBudgetAPI) entries(orgID string) []fakeBudgetEntry {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make([]fakeBudgetEntry, 0, len(a.budgets[orgID]))
	for _, e := range a.budgets[orgID] {
		out = append(out, *e)
	}

	return out
}

// recorded returns every request the fake saw so far.
func (a *fakeBudgetAPI) recorded() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]string(nil), a.requests...)
}

func (a *fakeBudgetAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.requests = append(a.requests, r.Method+" "+r.URL.Path)

	// /private/orgs/{orgID}/budgets[/{budgetID}]
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "private" || parts[1] != "orgs" || parts[3] != "budgets" {
		a.write(w, http.StatusNotFound, map[string]string{"message": "Not Found: " + r.URL.Path})

		return
	}

	orgID := parts[2]

	switch {
	case len(parts) == 4 && r.Method == http.MethodGet:
		a.list(w, orgID)
	case len(parts) == 4 && r.Method == http.MethodPut:
		a.set(w, r, orgID)
	case len(parts) == 5 && r.Method == http.MethodDelete:
		a.delete(w, orgID, parts[4])
	default:
		a.write(w, http.StatusMethodNotAllowed, map[string]string{"message": "Method Not Allowed"})
	}
}

func (a *fakeBudgetAPI) list(w http.ResponseWriter, orgID string) {
	if a.missingOrgs[orgID] {
		a.write(w, http.StatusForbidden, map[string]string{"message": "forbidden"})

		return
	}

	type wireBudget struct {
		Credits           int     `json:"credits"`
		BudgetID          string  `json:"budget_id"`
		EnforcementType   string  `json:"enforcement_type"`
		ProjectID         *string `json:"project_id"`
		Consumption       int     `json:"consumption"`
		Percentage        float64 `json:"percentage"`
		ThresholdExceeded bool    `json:"threshold_exceeded"`
	}

	items := make([]wireBudget, 0, len(a.budgets[orgID]))
	for _, e := range a.budgets[orgID] {
		items = append(items, wireBudget{
			Credits:           e.credits,
			BudgetID:          e.budgetID,
			EnforcementType:   e.enforcementType,
			ProjectID:         e.projectID,
			Consumption:       e.consumption,
			Percentage:        e.percentage,
			ThresholdExceeded: e.thresholdExceeded,
		})
	}

	a.write(w, http.StatusOK, struct {
		Budgets []wireBudget `json:"budgets"`
	}{Budgets: items})
}

// set implements the write PUT. [NET] measurement against gh-app-cci-1
// (2026-08-21) found this is not an upsert of the underlying record the way an
// earlier version of this fake modelled it: a request whose project_id
// matches an existing entry does not update that entry's credits in place —
// it deletes it and appends a brand-new entry with a freshly minted
// budget_id, even when credits is unchanged from what was already stored.
// Three consecutive real writes of 2000, 2000 then 3000 credits to the same
// scope came back with three different budget_ids in a row. So this fake
// removes any existing entry for the scope before appending the replacement,
// on every write, matching that churn — a fake that updated in place could
// pass a provider-level test asserting an id stays stable across an update,
// which the real API cannot do.
//
// The response body is not an empty object either, contrary to an earlier
// version of this fake (and of budgetSetRequest's own doc comment, now
// corrected): [NET] measurement shows 200 with
// {"credits":...,"budget_id":"...","enforcement_type":"warn","project_id":...}.
// It does not carry consumption, percentage or threshold_exceeded.
//
// credits < 1 is measured to answer 400 {"error":"Invalid budget settings"} —
// this fake validates that; everything else about the request body remains
// deliberately lenient (unknown fields ignored), matching the "unknown keys
// ignored" behaviour DESIGN.md documents for the equally undocumented
// contexts route, since there is no evidence this route is any stricter than
// that for fields this fake does not otherwise model.
func (a *fakeBudgetAPI) set(w http.ResponseWriter, r *http.Request, orgID string) {
	var body struct {
		Credits   int     `json:"credits"`
		ProjectID *string `json:"project_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		a.write(w, http.StatusBadRequest, map[string]string{"message": "invalid body"})

		return
	}

	if body.Credits < 1 {
		a.write(w, http.StatusBadRequest, map[string]string{"error": "Invalid budget settings"})

		return
	}

	kept := make([]*fakeBudgetEntry, 0, len(a.budgets[orgID]))
	for _, e := range a.budgets[orgID] {
		if !budgetScopeMatchesForTest(e.projectID, body.ProjectID) {
			kept = append(kept, e)
		}
	}

	a.nextSeq++
	entry := &fakeBudgetEntry{
		budgetID:        "fake-budget-" + strconv.Itoa(a.nextSeq),
		credits:         body.Credits,
		enforcementType: circleci.BudgetEnforcementWarn,
		projectID:       body.ProjectID,
	}
	a.budgets[orgID] = append(kept, entry)

	a.write(w, http.StatusOK, map[string]any{
		"credits":          entry.credits,
		"budget_id":        entry.budgetID,
		"enforcement_type": entry.enforcementType,
		"project_id":       entry.projectID,
	})
}

// delete answers 500, not 404, for a budget_id that does not currently exist
// in this org's collection — [NET] measurement (gh-app-cci-1, 2026-08-21)
// against both a syntactically valid but unknown id and the id of a budget
// this same client had just deleted, every time. An earlier version of this
// fake answered 404, which budgetResource.Delete's own prior version relied
// on circleci.IsNotFound to catch; that combination could never have worked
// against the real API.
func (a *fakeBudgetAPI) delete(w http.ResponseWriter, orgID, budgetID string) {
	entries := a.budgets[orgID]
	for i, e := range entries {
		if e.budgetID == budgetID {
			a.budgets[orgID] = append(entries[:i], entries[i+1:]...)
			a.write(w, http.StatusOK, map[string]any{})

			return
		}
	}

	a.write(w, http.StatusInternalServerError, map[string]string{"error": "There was an error deleting the budget"})
}

func (a *fakeBudgetAPI) write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		a.t.Errorf("encoding fake response: %v", err)
	}
}

// budgetScopeMatchesForTest mirrors budgetScopeMatches in budget.go: both nil,
// or both set to the same value.
func budgetScopeMatchesForTest(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}

	return *a == *b
}
