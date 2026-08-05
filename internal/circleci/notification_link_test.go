// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

// linkEntity mirrors the v3 data-entity envelope the API renders:
// attributes carry the link fields, references.user.id carries the owning
// CircleCI user, and there is no "id" key at all (the entity ID field uses
// json:"id,omitzero" and the API never sets it).
const testNotificationLinkResponse = `{
  "data": [
    {
      "attributes": {
        "connection_type": "slack",
        "external_id": "U-aaa",
        "external_scope_id": "T-ONE",
        "display_name": "Ada"
      },
      "references": {"user": {"id": "user-1"}}
    }
  ]
}`

func TestListNotificationLinks(t *testing.T) {
	t.Parallel()

	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testNotificationLinkResponse))
	}))
	t.Cleanup(srv.Close)

	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	links, err := c.ListNotificationLinks(context.Background(), circleci.ListNotificationLinksOptions{
		UserID:         "me",
		ConnectionType: "slack",
		TeamID:         "T-ONE",
	})
	if err != nil {
		t.Fatalf("ListNotificationLinks returned error: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("got %d links, want 1", len(links))
	}

	got := links[0]
	if got.ConnectionType != "slack" {
		t.Errorf("ConnectionType = %q, want %q", got.ConnectionType, "slack")
	}
	if got.ExternalID != "U-aaa" {
		t.Errorf("ExternalID = %q, want %q", got.ExternalID, "U-aaa")
	}
	if got.ExternalScopeID != "T-ONE" {
		t.Errorf("ExternalScopeID = %q, want %q", got.ExternalScopeID, "T-ONE")
	}
	if got.DisplayName != "Ada" {
		t.Errorf("DisplayName = %q, want %q", got.DisplayName, "Ada")
	}
	if got.UserID != "user-1" {
		t.Errorf("UserID = %q, want %q", got.UserID, "user-1")
	}

	wantQuery := "filter%5Bconnection_type%5D=slack&filter%5Bteam_id%5D=T-ONE&filter%5Buser_id%5D=me"
	if gotQuery != wantQuery {
		t.Errorf("query = %q, want %q", gotQuery, wantQuery)
	}
}

// TestListNotificationLinksDefaultsUserIDToMe confirms an empty UserID sends
// filter[user_id]=me rather than omitting the filter, since the upstream
// handler treats a missing filter[user_id] as a 400 (it is required), not
// "list everyone's links".
func TestListNotificationLinksDefaultsUserIDToMe(t *testing.T) {
	t.Parallel()

	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	if _, err := c.ListNotificationLinks(context.Background(), circleci.ListNotificationLinksOptions{}); err != nil {
		t.Fatalf("ListNotificationLinks returned error: %v", err)
	}

	if gotQuery != "filter%5Buser_id%5D=me" {
		t.Errorf("query = %q, want filter[user_id]=me and no other filter", gotQuery)
	}
}

func TestListNotificationLinksForbidden(t *testing.T) {
	t.Parallel()

	// filter[user_id] set to another user's UUID is rejected with 403 upstream,
	// never with a partial or empty result.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"title":"cannot access another user's links"}}`))
	}))
	t.Cleanup(srv.Close)

	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.ListNotificationLinks(context.Background(), circleci.ListNotificationLinksOptions{
		UserID: "11111111-1111-1111-1111-111111111111",
	})
	if err == nil {
		t.Fatal("ListNotificationLinks returned no error for a forbidden cross-user lookup")
	}
	if !circleci.IsUnauthorized(err) {
		t.Errorf("IsUnauthorized(%v) = false, want true", err)
	}
}

func TestListNotificationLinksEmpty(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	links, err := c.ListNotificationLinks(context.Background(), circleci.ListNotificationLinksOptions{UserID: "me"})
	if err != nil {
		t.Fatalf("ListNotificationLinks returned error: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("got %d links, want 0", len(links))
	}
}
