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

// set implements the upsert PUT: a request whose project_id (nil or a value)
// matches an existing entry updates its credits in place; otherwise a new
// entry is appended with a freshly minted budget_id and CircleCI's apparent
// defaults for a brand-new budget (enforcement_type "warn", zeroed
// statistics) — the same shape the migration CLI's own happy-path fixture
// uses for a budget it did not otherwise describe.
//
// Deliberately lenient about the request body: any field beyond credits and
// project_id is simply ignored rather than rejected, matching the "unknown
// keys ignored" behaviour DESIGN.md documents for the (equally undocumented)
// contexts route, and matching that this fake must not be stricter than the
// real service without evidence that the real service is strict.
func (a *fakeBudgetAPI) set(w http.ResponseWriter, r *http.Request, orgID string) {
	var body struct {
		Credits   int     `json:"credits"`
		ProjectID *string `json:"project_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		a.write(w, http.StatusBadRequest, map[string]string{"message": "invalid body"})

		return
	}

	for _, e := range a.budgets[orgID] {
		if budgetScopeMatchesForTest(e.projectID, body.ProjectID) {
			e.credits = body.Credits
			a.write(w, http.StatusOK, map[string]any{})

			return
		}
	}

	a.nextSeq++
	a.budgets[orgID] = append(a.budgets[orgID], &fakeBudgetEntry{
		budgetID:        "fake-budget-" + strconv.Itoa(a.nextSeq),
		credits:         body.Credits,
		enforcementType: circleci.BudgetEnforcementWarn,
		projectID:       body.ProjectID,
	})

	a.write(w, http.StatusOK, map[string]any{})
}

func (a *fakeBudgetAPI) delete(w http.ResponseWriter, orgID, budgetID string) {
	entries := a.budgets[orgID]
	for i, e := range entries {
		if e.budgetID == budgetID {
			a.budgets[orgID] = append(entries[:i], entries[i+1:]...)
			a.write(w, http.StatusOK, map[string]any{})

			return
		}
	}

	a.write(w, http.StatusNotFound, map[string]string{"message": "budget not found"})
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
