// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

const (
	testMembershipOrgID   = "3ddcf1d1-7f5f-4139-8cef-71ad0921a968"
	testMembershipGroupID = "9f1c2f6a-1a2b-4c3d-8e9f-0a1b2c3d4e5f"
	testMemberUserID      = "f959e57f-86c8-43c8-a3ac-af0c2ec03422"
)

// membershipRequest is one request as the mock server saw it.
type membershipRequest struct {
	method string
	path   string
	rawURI string
	query  string
	body   string
}

// newMembershipServer starts a fake private-origin API and returns a Client
// whose private host points at it. Host is deliberately unroutable: every
// method under test goes to the private host, never the main API host, and a
// connection error there is a clearer failure than a silent success.
func newMembershipServer(t *testing.T, handler http.HandlerFunc) (*circleci.Client, *[]membershipRequest) {
	t.Helper()

	var seen []membershipRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		seen = append(seen, membershipRequest{
			method: r.Method,
			path:   r.URL.Path,
			rawURI: r.RequestURI,
			query:  r.URL.RawQuery,
			body:   string(body),
		})

		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	return circleci.New(circleci.Config{
		Host:        "http://127.0.0.1:1",
		PrivateHost: srv.URL,
		Token:       "tok",
	}), &seen
}

func TestGroupMembershipServiceList(t *testing.T) {
	t.Parallel()

	client, seen := newMembershipServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// No "count" field: a real GET here answers only {"items": [...]}, see
		// group_membership.go.
		_, _ = w.Write([]byte(`{"items":[{
			"user_id":"` + testMemberUserID + `",
			"username":"api-infra",
			"avatar_url":"https://avatars.example/a.png",
			"email":"api@example.com",
			"group_id":"` + testMembershipGroupID + `",
			"created_at":"2023-12-13T10:10:37.951356Z"
		}]}`))
	})

	members, err := client.GroupMembership().List(context.Background(), testMembershipOrgID, testMembershipGroupID)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}

	if len(members) != 1 {
		t.Fatalf("member count = %d, want 1", len(members))
	}
	got := members[0]
	if got.UserID != testMemberUserID {
		t.Errorf("member user_id = %q, want %q", got.UserID, testMemberUserID)
	}
	if got.Username != "api-infra" {
		t.Errorf("member username = %q, want %q", got.Username, "api-infra")
	}
	if got.Email != "api@example.com" {
		t.Errorf("member email = %q, want %q", got.Email, "api@example.com")
	}
	if got.AvatarURL != "https://avatars.example/a.png" {
		t.Errorf("member avatar_url = %q, want %q", got.AvatarURL, "https://avatars.example/a.png")
	}
	if got.GroupID != testMembershipGroupID {
		t.Errorf("member group_id = %q, want %q", got.GroupID, testMembershipGroupID)
	}
	if got.CreatedAt != "2023-12-13T10:10:37.951356Z" {
		t.Errorf("member created_at = %q, want %q", got.CreatedAt, "2023-12-13T10:10:37.951356Z")
	}

	wantPath := "/private/ciam/orgs/" + testMembershipOrgID + "/groups/" + testMembershipGroupID + "/users"
	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}
	if req := (*seen)[0]; req.method != http.MethodGet || req.path != wantPath {
		t.Errorf("request = %s %s, want GET %s", req.method, req.path, wantPath)
	}
}

func TestGroupMembershipServiceListEmpty(t *testing.T) {
	t.Parallel()

	client, _ := newMembershipServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	})

	members, err := client.GroupMembership().List(context.Background(), testMembershipOrgID, testMembershipGroupID)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(members) != 0 {
		t.Errorf("member count = %d, want 0", len(members))
	}
}

func TestGroupMembershipServiceListNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newMembershipServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Group not found"}`))
	})

	_, err := client.GroupMembership().List(context.Background(), testMembershipOrgID, testMembershipGroupID)
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

func TestGroupMembershipServiceAdd(t *testing.T) {
	t.Parallel()

	client, seen := newMembershipServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})

	userIDs := []string{testMemberUserID, "ecfb05cd-5cc9-43e4-b5e5-e91ec08183a7"}
	if err := client.GroupMembership().Add(context.Background(), testMembershipOrgID, testMembershipGroupID, userIDs); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}
	got := (*seen)[0]

	wantPath := "/private/ciam/orgs/" + testMembershipOrgID + "/groups/" + testMembershipGroupID + "/add-users"
	if got.method != http.MethodPost || got.path != wantPath {
		t.Errorf("request = %s %s, want POST %s", got.method, got.path, wantPath)
	}

	assertUserIDsBody(t, got.body, userIDs)
}

func TestGroupMembershipServiceRemove(t *testing.T) {
	t.Parallel()

	client, seen := newMembershipServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})

	userIDs := []string{testMemberUserID}
	if err := client.GroupMembership().Remove(context.Background(), testMembershipOrgID, testMembershipGroupID, userIDs); err != nil {
		t.Fatalf("Remove returned error: %v", err)
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}
	got := (*seen)[0]

	// Removal is a POST to a separate action route, not a DELETE on the members
	// route, and not the same route as Add. Getting this wrong would silently
	// add the users instead, or double-add them.
	wantPath := "/private/ciam/orgs/" + testMembershipOrgID + "/groups/" + testMembershipGroupID + "/delete-users"
	if got.method != http.MethodPost || got.path != wantPath {
		t.Errorf("request = %s %s, want POST %s", got.method, got.path, wantPath)
	}

	assertUserIDsBody(t, got.body, userIDs)
}

// assertUserIDsBody checks that body is exactly {"user_ids": [...]} with want in
// order. Members are addressed by UUID, so an extra or renamed field would make
// the call a no-op against the real API.
func assertUserIDsBody(t *testing.T, body string, want []string) {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("request body %q is not JSON: %v", body, err)
	}
	if len(payload) != 1 {
		t.Errorf("request body = %v, want only a user_ids key", payload)
	}

	raw, ok := payload["user_ids"].([]any)
	if !ok {
		t.Fatalf("request body user_ids = %v, want an array", payload["user_ids"])
	}
	if len(raw) != len(want) {
		t.Fatalf("request body user_ids = %v, want %v", raw, want)
	}
	for i, wantID := range want {
		if raw[i] != wantID {
			t.Errorf("request body user_ids[%d] = %v, want %q", i, raw[i], wantID)
		}
	}
}

func TestGroupMembershipServiceAddAndRemoveSkipEmpty(t *testing.T) {
	t.Parallel()

	// The endpoint rejects an empty user_ids array, and there is nothing to do,
	// so neither call should reach the network.
	client, seen := newMembershipServer(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("server was called for an empty user id list")
		w.WriteHeader(http.StatusBadRequest)
	})

	ctx := context.Background()
	if err := client.GroupMembership().Add(ctx, testMembershipOrgID, testMembershipGroupID, nil); err != nil {
		t.Errorf("Add with no users returned error: %v", err)
	}
	if err := client.GroupMembership().Remove(ctx, testMembershipOrgID, testMembershipGroupID, []string{}); err != nil {
		t.Errorf("Remove with no users returned error: %v", err)
	}

	if len(*seen) != 0 {
		t.Errorf("request count = %d, want 0", len(*seen))
	}
}

func TestGroupMembershipServiceEscapesRouteParams(t *testing.T) {
	t.Parallel()

	// Ids arrive from configuration and state, so anything path-significant must
	// be escaped rather than changing which route is called.
	client, seen := newMembershipServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})

	err := client.GroupMembership().Remove(context.Background(), "org/../evil", "group/../evil", []string{"u1"})
	if err != nil {
		t.Fatalf("Remove returned error: %v", err)
	}

	// Assert on the raw request line: r.URL.Path is already percent-decoded, so
	// it would look the same whether or not the value was escaped.
	wantURI := "/private/ciam/orgs/org%2F..%2Fevil/groups/group%2F..%2Fevil/delete-users"
	if got := (*seen)[0].rawURI; got != wantURI {
		t.Errorf("raw request URI = %q, want %q", got, wantURI)
	}
}

func TestGroupMembershipServiceUsesPrivateHostNotMainHost(t *testing.T) {
	t.Parallel()

	// Host is left pointed at a closed port; if any of these calls went to the
	// main API host instead of the private origin, they would fail to connect
	// rather than succeeding against the fake, so a passing test here already
	// proves the routing. This test additionally asserts the failure mode
	// directly, for a clearer signal if that ever regresses.
	client := circleci.New(circleci.Config{
		Host:  "http://127.0.0.1:1",
		Token: "tok",
	})

	_, err := client.GroupMembership().List(context.Background(), testMembershipOrgID, testMembershipGroupID)
	if err == nil {
		t.Fatal("List with no fake private origin succeeded, want a connection error")
	}
}

func TestMemberDelta(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		desired    []string
		actual     []string
		wantAdd    []string
		wantRemove []string
	}{
		{
			name:    "no change",
			desired: []string{"a", "b"},
			actual:  []string{"a", "b"},
		},
		{
			// Order must not matter: the same membership expressed differently is
			// still a no-op, otherwise every plan would churn.
			name:    "no change with different order",
			desired: []string{"b", "a"},
			actual:  []string{"a", "b"},
		},
		{
			name:    "add to empty group",
			desired: []string{"a", "b"},
			actual:  nil,
			wantAdd: []string{"a", "b"},
		},
		{
			name:       "remove everything",
			desired:    nil,
			actual:     []string{"a", "b"},
			wantRemove: []string{"a", "b"},
		},
		{
			// The interesting case: a single plan must produce both calls, and
			// must not touch the members that are staying.
			name:       "add and remove at once",
			desired:    []string{"a", "c", "d"},
			actual:     []string{"a", "b", "e"},
			wantAdd:    []string{"c", "d"},
			wantRemove: []string{"b", "e"},
		},
		{
			name:       "replace the whole membership",
			desired:    []string{"c", "d"},
			actual:     []string{"a", "b"},
			wantAdd:    []string{"c", "d"},
			wantRemove: []string{"a", "b"},
		},
		{
			name:    "both empty",
			desired: nil,
			actual:  nil,
		},
		{
			// A duplicate must not be added twice: the API would accept it, but
			// the second add is pointless traffic.
			name:    "duplicate desired ids collapse",
			desired: []string{"a", "a", "b"},
			actual:  nil,
			wantAdd: []string{"a", "b"},
		},
		{
			name:       "duplicate actual ids collapse",
			desired:    nil,
			actual:     []string{"a", "a"},
			wantRemove: []string{"a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			toAdd, toRemove := circleci.MemberDelta(tt.desired, tt.actual)

			if !slices.Equal(toAdd, tt.wantAdd) {
				t.Errorf("toAdd = %v, want %v", toAdd, tt.wantAdd)
			}
			if !slices.Equal(toRemove, tt.wantRemove) {
				t.Errorf("toRemove = %v, want %v", toRemove, tt.wantRemove)
			}
		})
	}
}
