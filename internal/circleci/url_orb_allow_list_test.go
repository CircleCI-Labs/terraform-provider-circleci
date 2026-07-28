// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

const urlOrbAllowListResponse = `{
  "items": [
    {
      "id": "ba98990a-5a00-4cad-b55e-b44117b92e0c",
      "name": "CircleCI-Public orbs",
      "prefix": "https://raw.githubusercontent.com/CircleCI-Public/orbs/refs/heads/main/",
      "auth": "github-app"
    },
    {
      "id": "cc98990a-5a00-4cad-b55e-b44117b92e0d",
      "name": "public mirror",
      "prefix": "https://orbs.example.com/public/",
      "auth": "none"
    }
  ]
}`

func TestListURLOrbAllowListRoute(t *testing.T) {
	t.Parallel()

	srv, calls := newSettingsServer(t, http.StatusOK, urlOrbAllowListResponse)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	entries, err := c.ListURLOrbAllowList(context.Background(), "gh/acme")
	if err != nil {
		t.Fatalf("ListURLOrbAllowList returned error: %v", err)
	}

	if len(*calls) != 1 {
		t.Fatalf("made %d requests, want 1: the endpoint does not paginate", len(*calls))
	}

	got := (*calls)[0]
	if want := http.MethodGet; got.method != want {
		t.Errorf("method = %q, want %q", got.method, want)
	}
	// This is v2, not v3: the same route must work on CircleCI Server.
	if want := "/api/v2/organization/gh/acme/url-orb-allow-list"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}

	if len(entries) != 2 {
		t.Fatalf("returned %d entries, want 2", len(entries))
	}
	if entries[0].ID != "ba98990a-5a00-4cad-b55e-b44117b92e0c" {
		t.Errorf("entries[0].ID = %q, want the id from the items array", entries[0].ID)
	}
	if entries[1].Auth != circleci.URLOrbAllowListAuthNone {
		t.Errorf("entries[1].Auth = %q, want %q", entries[1].Auth, circleci.URLOrbAllowListAuthNone)
	}
}

func TestListURLOrbAllowListEscapesOrganizationUUID(t *testing.T) {
	t.Parallel()

	srv, calls := newSettingsServer(t, http.StatusOK, `{"items":[]}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	if _, err := c.ListURLOrbAllowList(context.Background(), "00000000-1111-2222-3333-444444444444"); err != nil {
		t.Fatalf("ListURLOrbAllowList returned error: %v", err)
	}

	want := "/api/v2/organization/00000000-1111-2222-3333-444444444444/url-orb-allow-list"
	if got := (*calls)[0].path; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestCreateURLOrbAllowListEntry(t *testing.T) {
	t.Parallel()

	srv, calls := newSettingsServer(t, http.StatusOK,
		`{"id":"ba98990a-5a00-4cad-b55e-b44117b92e0c","message":"Created."}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	req := circleci.CreateURLOrbAllowListEntryRequest{
		Name:   "CircleCI-Public orbs",
		Prefix: "https://raw.githubusercontent.com/CircleCI-Public/orbs/refs/heads/main/",
		Auth:   circleci.URLOrbAllowListAuthGitHubApp,
	}

	entry, err := c.CreateURLOrbAllowListEntry(context.Background(), "gh/acme", req)
	if err != nil {
		t.Fatalf("CreateURLOrbAllowListEntry returned error: %v", err)
	}

	got := (*calls)[0]
	if want := http.MethodPost; got.method != want {
		t.Errorf("method = %q, want %q", got.method, want)
	}
	if want := "/api/v2/organization/gh/acme/url-orb-allow-list"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}

	var sent map[string]any
	if err := json.Unmarshal(got.body, &sent); err != nil {
		t.Fatalf("request body %q is not JSON: %v", got.body, err)
	}

	want := map[string]any{"name": req.Name, "prefix": req.Prefix, "auth": req.Auth}
	if len(sent) != len(want) {
		t.Errorf("request body = %s, want exactly %v", got.body, want)
	}
	for key, value := range want {
		if sent[key] != value {
			t.Errorf("request body %s = %v, want %v", key, sent[key], value)
		}
	}

	// The create response carries only an id, so the rest of the entry has to
	// come back from the request.
	if entry.ID != "ba98990a-5a00-4cad-b55e-b44117b92e0c" {
		t.Errorf("entry.ID = %q, want the id from the response", entry.ID)
	}
	if entry.Name != req.Name || entry.Prefix != req.Prefix || entry.Auth != req.Auth {
		t.Errorf("entry = %+v, want the requested fields echoed back", *entry)
	}
}

func TestDeleteURLOrbAllowListEntryRoute(t *testing.T) {
	t.Parallel()

	srv, calls := newSettingsServer(t, http.StatusOK, `{"message":"Deleted."}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	err := c.DeleteURLOrbAllowListEntry(context.Background(), "gh/acme", "ba98990a-5a00-4cad-b55e-b44117b92e0c")
	if err != nil {
		t.Fatalf("DeleteURLOrbAllowListEntry returned error: %v", err)
	}

	got := (*calls)[0]
	if want := http.MethodDelete; got.method != want {
		t.Errorf("method = %q, want %q", got.method, want)
	}
	want := "/api/v2/organization/gh/acme/url-orb-allow-list/ba98990a-5a00-4cad-b55e-b44117b92e0c"
	if got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
}

func TestGetURLOrbAllowListEntryFiltersTheListing(t *testing.T) {
	t.Parallel()

	srv, _ := newSettingsServer(t, http.StatusOK, urlOrbAllowListResponse)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	entry, err := c.GetURLOrbAllowListEntry(context.Background(), "gh/acme", "cc98990a-5a00-4cad-b55e-b44117b92e0d")
	if err != nil {
		t.Fatalf("GetURLOrbAllowListEntry returned error: %v", err)
	}
	if entry.Name != "public mirror" {
		t.Errorf("entry.Name = %q, want %q", entry.Name, "public mirror")
	}
}

func TestGetURLOrbAllowListEntryAbsentIsNotFound(t *testing.T) {
	t.Parallel()

	// There is no read-one route, so an entry that has been deleted shows up as a
	// listing that no longer contains it. That must be IsNotFound so the resource
	// drops out of state rather than erroring.
	srv, _ := newSettingsServer(t, http.StatusOK, urlOrbAllowListResponse)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.GetURLOrbAllowListEntry(context.Background(), "gh/acme", "00000000-0000-0000-0000-000000000000")
	if err == nil {
		t.Fatal("GetURLOrbAllowListEntry returned no error for an absent entry")
	}
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

func TestGetURLOrbAllowListEntryOrganizationNotFound(t *testing.T) {
	t.Parallel()

	srv, _ := newSettingsServer(t, http.StatusNotFound, `{"message":"Organization not found."}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.GetURLOrbAllowListEntry(context.Background(), "gh/nope", "some-id")
	if err == nil {
		t.Fatal("GetURLOrbAllowListEntry returned no error for a 404")
	}
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

func TestURLOrbAllowListAuthValues(t *testing.T) {
	t.Parallel()

	// The provider's schema validator is built from this list, so it must match
	// the enum the API documents.
	want := map[string]bool{
		"bitbucket-oauth": true,
		"github-app":      true,
		"github-oauth":    true,
		"none":            true,
	}

	if len(circleci.URLOrbAllowListAuthValues) != len(want) {
		t.Fatalf("URLOrbAllowListAuthValues = %v, want %d values", circleci.URLOrbAllowListAuthValues, len(want))
	}
	for _, value := range circleci.URLOrbAllowListAuthValues {
		if !want[value] {
			t.Errorf("URLOrbAllowListAuthValues contains %q, which the API does not accept", value)
		}
	}
}
