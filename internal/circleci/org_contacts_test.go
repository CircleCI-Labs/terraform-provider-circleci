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

// newContactsServer returns a server that records every request and answers
// each with body.
func newContactsServer(t *testing.T, status int, body string) (*httptest.Server, *[]recordedCall) {
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

	return srv, calls
}

func TestGetOrganizationContactsRouteAndOrigin(t *testing.T) {
	t.Parallel()

	const orgID = "00000000-1111-2222-3333-444444444444"

	srv, calls := newContactsServer(t, http.StatusOK, `{"primary":["a@example.com","b@example.com"],"security":["c@example.com"]}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	contacts, err := c.GetOrganizationContacts(context.Background(), orgID)
	if err != nil {
		t.Fatalf("GetOrganizationContacts returned error: %v", err)
	}

	if len(*calls) != 1 {
		t.Fatalf("made %d requests, want 1", len(*calls))
	}

	got := (*calls)[0]
	if want := http.MethodGet; got.method != want {
		t.Errorf("method = %q, want %q", got.method, want)
	}

	// This is the load-bearing assertion: the route lives on the ordinary API
	// origin (what circleci.Config.Host / srv.URL points the client at), NOT on
	// DefaultPrivateHost, even though it is a /private route.
	if want := "/api/private/organization/" + orgID + "/contacts"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}

	if len(contacts.Primary) != 2 || contacts.Primary[0] != "a@example.com" || contacts.Primary[1] != "b@example.com" {
		t.Errorf("Primary = %v, want [a@example.com b@example.com]", contacts.Primary)
	}
	if len(contacts.Security) != 1 || contacts.Security[0] != "c@example.com" {
		t.Errorf("Security = %v, want [c@example.com]", contacts.Security)
	}
}

func TestGetOrganizationContactsNotFound(t *testing.T) {
	t.Parallel()

	srv, _ := newContactsServer(t, http.StatusNotFound, `{"message":"not found"}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.GetOrganizationContacts(context.Background(), "missing-org")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

func TestSetOrganizationContactsSendsBothListsInFull(t *testing.T) {
	t.Parallel()

	const orgID = "org-1"

	srv, calls := newContactsServer(t, http.StatusOK, `{"primary":["a@example.com"],"security":[]}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.SetOrganizationContacts(context.Background(), orgID, circleci.OrganizationContacts{
		Primary:  []string{"a@example.com"},
		Security: nil,
	})
	if err != nil {
		t.Fatalf("SetOrganizationContacts returned error: %v", err)
	}

	if len(*calls) != 1 {
		t.Fatalf("made %d requests, want 1", len(*calls))
	}

	got := (*calls)[0]
	if want := http.MethodPut; got.method != want {
		t.Errorf("method = %q, want %q", got.method, want)
	}
	if want := "/api/private/organization/" + orgID + "/contacts"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}

	var sent map[string]any
	if err := json.Unmarshal(got.body, &sent); err != nil {
		t.Fatalf("request body %q is not JSON: %v", got.body, err)
	}

	// security was nil, but PUT is a full replacement: it must be sent as an
	// empty array, not omitted and not null, or the service would have no way
	// to distinguish "leave alone" (impossible for this route) from "clear it".
	security, ok := sent["security"].([]any)
	if !ok {
		t.Fatalf("request body security = %v (%T), want a JSON array", sent["security"], sent["security"])
	}
	if len(security) != 0 {
		t.Errorf("request body security = %v, want empty array", security)
	}

	primary, ok := sent["primary"].([]any)
	if !ok || len(primary) != 1 || primary[0] != "a@example.com" {
		t.Errorf("request body primary = %v, want [a@example.com]", sent["primary"])
	}
}

func TestSetOrganizationContactsTooManyIsAnError(t *testing.T) {
	t.Parallel()

	// Mirrors the org-migration CLI's own coverage of this case: the API rejects
	// a 6th address in a list with HTTP 422.
	srv, _ := newContactsServer(t, http.StatusUnprocessableEntity, `{"message":"too many contacts"}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.SetOrganizationContacts(context.Background(), "org-1", circleci.OrganizationContacts{
		Primary: []string{"a@x.com", "b@x.com", "c@x.com", "d@x.com", "e@x.com", "f@x.com"},
	})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if got := circleci.Detail(err); got != "too many contacts (HTTP 422)" {
		t.Errorf("Detail(err) = %q, want %q", got, "too many contacts (HTTP 422)")
	}
}
