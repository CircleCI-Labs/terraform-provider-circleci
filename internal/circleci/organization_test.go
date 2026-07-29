// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

// orgCall is one request the fake organization API received.
type orgCall struct {
	method     string
	path       string
	requestURI string
	body       []byte
}

// newOrganizationAPI serves the organization routes and records every call,
// answering a successful GET or POST with testOrgBody. A non-empty
// notFoundMessage answers every request with a 404 carrying that message
// instead.
func newOrganizationAPI(t *testing.T, notFoundMessage string) (*httptest.Server, func() []orgCall) {
	t.Helper()

	var (
		mu    sync.Mutex
		calls []orgCall
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		mu.Lock()
		calls = append(calls, orgCall{
			method: r.Method, path: r.URL.Path, requestURI: r.RequestURI, body: body,
		})
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		if notFoundMessage != "" {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": notFoundMessage})

			return
		}

		if r.Method == http.MethodDelete {
			// delete-organization-handler answers 202 Accepted with
			// {"message": "Accepted."}.
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "Accepted."})

			return
		}

		_, _ = w.Write([]byte(testOrgBody))
	}))
	t.Cleanup(srv.Close)

	return srv, func() []orgCall {
		mu.Lock()
		defer mu.Unlock()

		return append([]orgCall(nil), calls...)
	}
}

// The shape mirrors circle.http.api.v2.organization's create- and
// get-organization-handler: exactly {id, name, slug, vcs_type}.
const testOrgBody = `{"id":"00000000-1111-2222-3333-444444444444","name":"acme","slug":"gh/acme","vcs_type":"github"}`

func TestCreateOrganization(t *testing.T) {
	t.Parallel()

	srv, recorded := newOrganizationAPI(t, "")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	org, err := c.CreateOrganization(context.Background(), circleci.OrganizationInput{
		Name:    "acme",
		VCSType: "github",
	})
	if err != nil {
		t.Fatalf("CreateOrganization returned error: %v", err)
	}

	if org.ID != "00000000-1111-2222-3333-444444444444" || org.Name != "acme" ||
		org.Slug != "gh/acme" || org.VCSType != "github" {
		t.Errorf("organization = %+v, want the fields from the response body", org)
	}

	calls := recorded()
	if len(calls) != 1 {
		t.Fatalf("made %d requests, want 1", len(calls))
	}
	if calls[0].method != http.MethodPost || calls[0].path != "/api/v2/organization" {
		t.Errorf("request = %s %s, want POST /api/v2/organization", calls[0].method, calls[0].path)
	}

	var sentBody map[string]string
	if err := json.Unmarshal(calls[0].body, &sentBody); err != nil {
		t.Fatalf("request body did not decode as JSON: %v", err)
	}
	if want := map[string]string{"name": "acme", "vcs_type": "github"}; sentBody["name"] != want["name"] ||
		sentBody["vcs_type"] != want["vcs_type"] {
		t.Errorf("request body = %v, want %v", sentBody, want)
	}
}

func TestGetOrganization(t *testing.T) {
	t.Parallel()

	srv, recorded := newOrganizationAPI(t, "")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	org, err := c.GetOrganization(context.Background(), "gh/acme")
	if err != nil {
		t.Fatalf("GetOrganization returned error: %v", err)
	}
	if org.Slug != "gh/acme" {
		t.Errorf("Slug = %q, want %q", org.Slug, "gh/acme")
	}

	calls := recorded()
	if len(calls) != 1 {
		t.Fatalf("made %d requests, want 1", len(calls))
	}
	if calls[0].method != http.MethodGet {
		t.Errorf("method = %q, want GET", calls[0].method)
	}
	// The org-slug-or-id route segment accepts a percent-encoded separator in
	// place of the slug's literal "/", unlike the project and checkout-key
	// routes, so RouteParams's escaping is exactly what production expects here.
	if want := "/api/v2/organization/gh%2Facme"; calls[0].requestURI != want {
		t.Errorf("raw request URI = %q, want %q", calls[0].requestURI, want)
	}
}

func TestGetOrganizationByUUID(t *testing.T) {
	t.Parallel()

	srv, recorded := newOrganizationAPI(t, "")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	if _, err := c.GetOrganization(context.Background(), "00000000-1111-2222-3333-444444444444"); err != nil {
		t.Fatalf("GetOrganization returned error: %v", err)
	}

	calls := recorded()
	if want := "/api/v2/organization/00000000-1111-2222-3333-444444444444"; calls[0].path != want {
		t.Errorf("path = %q, want %q", calls[0].path, want)
	}
}

func TestGetOrganizationNotFound(t *testing.T) {
	t.Parallel()

	// get-organization-handler throws the identical not-found exception whether
	// the org does not exist or the caller cannot view it: anti-enumeration by
	// 404, the opposite of the 403-for-missing behaviour groups use.
	srv, _ := newOrganizationAPI(t, "Org not found.")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.GetOrganization(context.Background(), "gh/nope")
	if !circleci.IsNotFound(err) {
		t.Errorf("GetOrganization error = %v, want a not found error", err)
	}
}

func TestDeleteOrganization(t *testing.T) {
	t.Parallel()

	srv, recorded := newOrganizationAPI(t, "")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	if err := c.DeleteOrganization(context.Background(), "circleci/00000000-1111-2222-3333-444444444444"); err != nil {
		t.Fatalf("DeleteOrganization returned error: %v", err)
	}

	calls := recorded()
	if len(calls) != 1 {
		t.Fatalf("made %d requests, want 1", len(calls))
	}
	if calls[0].method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", calls[0].method)
	}
	if want := "/api/v2/organization/circleci%2F00000000-1111-2222-3333-444444444444"; calls[0].requestURI != want {
		t.Errorf("raw request URI = %q, want %q", calls[0].requestURI, want)
	}
}

func TestDeleteOrganizationNotFound(t *testing.T) {
	t.Parallel()

	srv, _ := newOrganizationAPI(t, "Org not found.")
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	err := c.DeleteOrganization(context.Background(), "gh/nope")
	if !circleci.IsNotFound(err) {
		t.Errorf("DeleteOrganization error = %v, want a not found error", err)
	}
}
