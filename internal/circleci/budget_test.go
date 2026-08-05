// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

const testBudgetOrgID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

// newBudgetServer starts a private-origin stand-in and returns a client
// pointed at it via Config.PrivateHost — the test-only override private.go
// documents — plus every request the stand-in recorded.
func newBudgetServer(t *testing.T, status int, body string) (*circleci.Client, *[]recordedCall) {
	t.Helper()

	calls := new([]recordedCall)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}

		*calls = append(*calls, recordedCall{method: r.Method, path: r.URL.Path, body: raw})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	client := circleci.New(circleci.Config{
		// A deliberately unreachable ordinary host: every method under test
		// must go over PrivateHost, never Host, and this makes it obvious if
		// one accidentally did not.
		Host:        "http://127.0.0.1:1",
		PrivateHost: srv.URL,
		Token:       "tok",
	})

	return client, calls
}

func TestListBudgets_HappyPath(t *testing.T) {
	t.Parallel()

	// Field names and values mirror the org-migration CLI's own happy-path case:
	// one org-level budget (project_id null) and one per-project budget, each
	// carrying every field the type declares.
	client, calls := newBudgetServer(t, http.StatusOK, `{
		"budgets": [
			{
				"credits": 1000000,
				"budget_id": "budget-uuid-1",
				"enforcement_type": "warn",
				"project_id": null,
				"consumption": 0,
				"percentage": 0.0,
				"threshold_exceeded": false
			},
			{
				"credits": 50000,
				"budget_id": "budget-uuid-2",
				"enforcement_type": "block",
				"project_id": "proj-uuid-1",
				"consumption": 1000,
				"percentage": 2.0,
				"threshold_exceeded": false
			}
		]
	}`)

	budgets, err := client.ListBudgets(context.Background(), testBudgetOrgID)
	if err != nil {
		t.Fatalf("ListBudgets returned error: %v", err)
	}

	if len(*calls) != 1 {
		t.Fatalf("made %d requests, want 1", len(*calls))
	}

	got := (*calls)[0]
	if want := http.MethodGet; got.method != want {
		t.Errorf("method = %q, want %q", got.method, want)
	}
	if want := "/private/orgs/" + testBudgetOrgID + "/budgets"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}

	if len(budgets) != 2 {
		t.Fatalf("got %d budgets, want 2", len(budgets))
	}

	org := budgets[0]
	if org.BudgetID != "budget-uuid-1" || org.Credits != 1000000 || org.EnforcementType != circleci.BudgetEnforcementWarn {
		t.Errorf("org budget = %+v, want budget-uuid-1/1000000/warn", org)
	}
	if org.ProjectID != nil {
		t.Errorf("org budget ProjectID = %v, want nil", *org.ProjectID)
	}

	proj := budgets[1]
	if proj.BudgetID != "budget-uuid-2" || proj.Credits != 50000 || proj.EnforcementType != circleci.BudgetEnforcementBlock {
		t.Errorf("project budget = %+v, want budget-uuid-2/50000/block", proj)
	}
	if proj.ProjectID == nil || *proj.ProjectID != "proj-uuid-1" {
		t.Errorf("project budget ProjectID = %v, want proj-uuid-1", proj.ProjectID)
	}
	if proj.Consumption != 1000 || proj.Percentage != 2.0 || proj.ThresholdExceeded {
		t.Errorf("project budget stats = consumption=%d percentage=%v threshold_exceeded=%v, want 1000/2.0/false",
			proj.Consumption, proj.Percentage, proj.ThresholdExceeded)
	}
}

func TestListBudgets_Empty(t *testing.T) {
	t.Parallel()

	client, _ := newBudgetServer(t, http.StatusOK, `{"budgets":[]}`)

	budgets, err := client.ListBudgets(context.Background(), testBudgetOrgID)
	if err != nil {
		t.Fatalf("ListBudgets returned error: %v", err)
	}
	if len(budgets) != 0 {
		t.Errorf("got %d budgets, want 0", len(budgets))
	}
}

func TestListBudgets_ServerError(t *testing.T) {
	t.Parallel()

	client, _ := newBudgetServer(t, http.StatusForbidden, `{"message":"forbidden"}`)

	if _, err := client.ListBudgets(context.Background(), testBudgetOrgID); err == nil {
		t.Fatal("ListBudgets returned no error, want one")
	}
}

func TestFindBudget_MatchesScope(t *testing.T) {
	t.Parallel()

	client, _ := newBudgetServer(t, http.StatusOK, `{
		"budgets": [
			{"credits": 1000000, "budget_id": "budget-org", "enforcement_type": "warn", "project_id": null},
			{"credits": 50000, "budget_id": "budget-proj", "enforcement_type": "block", "project_id": "proj-1"}
		]
	}`)

	orgBudget, ok, err := client.FindBudget(context.Background(), testBudgetOrgID, nil)
	if err != nil {
		t.Fatalf("FindBudget(nil) returned error: %v", err)
	}
	if !ok || orgBudget.BudgetID != "budget-org" {
		t.Errorf("FindBudget(nil) = %+v, %v, want budget-org, true", orgBudget, ok)
	}

	projID := "proj-1"
	projBudget, ok, err := client.FindBudget(context.Background(), testBudgetOrgID, &projID)
	if err != nil {
		t.Fatalf("FindBudget(%q) returned error: %v", projID, err)
	}
	if !ok || projBudget.BudgetID != "budget-proj" {
		t.Errorf("FindBudget(%q) = %+v, %v, want budget-proj, true", projID, projBudget, ok)
	}

	missing := "proj-does-not-exist"
	notFound, ok, err := client.FindBudget(context.Background(), testBudgetOrgID, &missing)
	if err != nil {
		t.Fatalf("FindBudget(%q) returned error: %v", missing, err)
	}
	if ok || notFound != nil {
		t.Errorf("FindBudget(%q) = %+v, %v, want nil, false", missing, notFound, ok)
	}
}

func TestSetBudget_OrgLevel(t *testing.T) {
	t.Parallel()

	// The response body is deliberately an empty object: SetBudget's own doc
	// comment explains why nothing here decodes it, mirroring the migration
	// CLI's SetBudget, which discards its response into map[string]any.
	client, calls := newBudgetServer(t, http.StatusOK, `{}`)

	if err := client.SetBudget(context.Background(), testBudgetOrgID, nil, 2000000); err != nil {
		t.Fatalf("SetBudget returned error: %v", err)
	}

	if len(*calls) != 1 {
		t.Fatalf("made %d requests, want 1", len(*calls))
	}

	got := (*calls)[0]
	if want := http.MethodPut; got.method != want {
		t.Errorf("method = %q, want %q", got.method, want)
	}
	if want := "/private/orgs/" + testBudgetOrgID + "/budgets"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}

	var body map[string]any
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatalf("decoding request body: %v", err)
	}

	if v, ok := body["credits"]; !ok || v != float64(2000000) {
		t.Errorf(`body["credits"] = %v (present=%v), want 2000000`, v, ok)
	}
	// project_id must be present and explicitly null for the org-level budget,
	// not simply omitted — the API distinguishes "no project" from "field not
	// sent" (per budgetSetRequest's lack of `omitempty`).
	if v, ok := body["project_id"]; !ok || v != nil {
		t.Errorf(`body["project_id"] = %v (present=%v), want present and null`, v, ok)
	}
}

func TestSetBudget_ProjectLevel(t *testing.T) {
	t.Parallel()

	client, calls := newBudgetServer(t, http.StatusOK, `{}`)

	projID := "proj-uuid-99"
	if err := client.SetBudget(context.Background(), testBudgetOrgID, &projID, 75000); err != nil {
		t.Fatalf("SetBudget returned error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal((*calls)[0].body, &body); err != nil {
		t.Fatalf("decoding request body: %v", err)
	}

	if v, ok := body["credits"]; !ok || v != float64(75000) {
		t.Errorf(`body["credits"] = %v (present=%v), want 75000`, v, ok)
	}
	if v := body["project_id"]; v != projID {
		t.Errorf(`body["project_id"] = %v, want %q`, v, projID)
	}
}

func TestSetBudget_ServerError(t *testing.T) {
	t.Parallel()

	client, _ := newBudgetServer(t, http.StatusBadRequest, `{"message":"invalid"}`)

	if err := client.SetBudget(context.Background(), testBudgetOrgID, nil, 0); err == nil {
		t.Fatal("SetBudget returned no error, want one")
	}
}

func TestDeleteBudget_HappyPath(t *testing.T) {
	t.Parallel()

	client, calls := newBudgetServer(t, http.StatusOK, `{}`)

	const budgetID = "budget-uuid-to-delete"
	if err := client.DeleteBudget(context.Background(), testBudgetOrgID, budgetID); err != nil {
		t.Fatalf("DeleteBudget returned error: %v", err)
	}

	if len(*calls) != 1 {
		t.Fatalf("made %d requests, want 1", len(*calls))
	}

	got := (*calls)[0]
	if want := http.MethodDelete; got.method != want {
		t.Errorf("method = %q, want %q", got.method, want)
	}
	if want := "/private/orgs/" + testBudgetOrgID + "/budgets/" + budgetID; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
}

func TestDeleteBudget_NotFound(t *testing.T) {
	t.Parallel()

	client, _ := newBudgetServer(t, http.StatusNotFound, `{"message":"not found"}`)

	err := client.DeleteBudget(context.Background(), testBudgetOrgID, "missing-id")
	if err == nil {
		t.Fatal("DeleteBudget returned no error, want one")
	}
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(err) = false for a 404, want true: %v", err)
	}
}
