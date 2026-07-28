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

const (
	testGroupOrgID = "3ddcf1d1-7f5f-4139-8cef-71ad0921a968"
	testGroupID    = "9f1c2f6a-1a2b-4c3d-8e9f-0a1b2c3d4e5f"
)

// groupRequest is one request as the mock server saw it.
type groupRequest struct {
	method string
	path   string
	rawURI string
	query  string
	body   string
}

// newGroupServer serves the group routes from handler and records every request,
// so tests can assert on the exact paths and payloads sent.
func newGroupServer(t *testing.T, handler http.HandlerFunc) (*circleci.Client, *[]groupRequest) {
	t.Helper()

	var seen []groupRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		seen = append(seen, groupRequest{
			method: r.Method,
			path:   r.URL.Path,
			rawURI: r.RequestURI,
			query:  r.URL.RawQuery,
			body:   string(body),
		})

		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	return circleci.New(circleci.Config{Host: srv.URL, Token: "tok"}), &seen
}

func TestGroupServiceGet(t *testing.T) {
	t.Parallel()

	client, seen := newGroupServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + testGroupID + `","name":"platform","description":"Platform team"}`))
	})

	group, err := client.Groups().Get(context.Background(), testGroupOrgID, testGroupID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	if group.ID != testGroupID {
		t.Errorf("group id = %q, want %q", group.ID, testGroupID)
	}
	if group.Name != "platform" {
		t.Errorf("group name = %q, want %q", group.Name, "platform")
	}
	if group.Description != "Platform team" {
		t.Errorf("group description = %q, want %q", group.Description, "Platform team")
	}

	wantPath := "/api/v2/organizations/" + testGroupOrgID + "/groups/" + testGroupID
	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}
	if got := (*seen)[0]; got.method != http.MethodGet || got.path != wantPath {
		t.Errorf("request = %s %s, want GET %s", got.method, got.path, wantPath)
	}
}

func TestGroupServiceGetNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newGroupServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Group not found"}`))
	})

	_, err := client.Groups().Get(context.Background(), testGroupOrgID, testGroupID)
	if err == nil {
		t.Fatal("Get returned no error for a 404, want one")
	}
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
	// The diagnostic detail must carry the server's message, not just the status.
	if detail := circleci.Detail(err); detail == "" {
		t.Error("Detail() = \"\", want the server message")
	}
}

func TestGroupServiceGetEmptyBodyIsNotFound(t *testing.T) {
	t.Parallel()

	// A 2xx body with no id must not become an empty resource in state.
	client, _ := newGroupServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})

	if _, err := client.Groups().Get(context.Background(), testGroupOrgID, testGroupID); !circleci.IsNotFound(err) {
		t.Errorf("Get error = %v, want a not found error", err)
	}
}

func TestGroupServiceCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		request  circleci.CreateGroupRequest
		wantBody map[string]any
	}{
		{
			name:     "with description",
			request:  circleci.CreateGroupRequest{Name: "platform", Description: "Platform team"},
			wantBody: map[string]any{"name": "platform", "description": "Platform team"},
		},
		{
			// An empty description is omitted rather than sent as "", so the
			// server applies its own default.
			name:     "without description",
			request:  circleci.CreateGroupRequest{Name: "platform"},
			wantBody: map[string]any{"name": "platform"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, seen := newGroupServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":"` + testGroupID + `","name":"platform","description":"Platform team"}`))
			})

			group, err := client.Groups().Create(context.Background(), testGroupOrgID, tt.request)
			if err != nil {
				t.Fatalf("Create returned error: %v", err)
			}
			if group.ID != testGroupID {
				t.Errorf("group id = %q, want %q", group.ID, testGroupID)
			}

			if len(*seen) != 1 {
				t.Fatalf("request count = %d, want 1", len(*seen))
			}
			got := (*seen)[0]

			wantPath := "/api/v2/organizations/" + testGroupOrgID + "/groups"
			if got.method != http.MethodPost || got.path != wantPath {
				t.Errorf("request = %s %s, want POST %s", got.method, got.path, wantPath)
			}

			var body map[string]any
			if err := json.Unmarshal([]byte(got.body), &body); err != nil {
				t.Fatalf("request body %q is not JSON: %v", got.body, err)
			}
			if len(body) != len(tt.wantBody) {
				t.Errorf("request body = %v, want %v", body, tt.wantBody)
			}
			for key, want := range tt.wantBody {
				if body[key] != want {
					t.Errorf("request body %q = %v, want %v", key, body[key], want)
				}
			}
		})
	}
}

func TestGroupServiceDelete(t *testing.T) {
	t.Parallel()

	// Delete answers 204 with no body, which must not be decoded.
	client, seen := newGroupServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	if err := client.Groups().Delete(context.Background(), testGroupOrgID, testGroupID); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}

	wantPath := "/api/v2/organizations/" + testGroupOrgID + "/groups/" + testGroupID
	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}
	if got := (*seen)[0]; got.method != http.MethodDelete || got.path != wantPath {
		t.Errorf("request = %s %s, want DELETE %s", got.method, got.path, wantPath)
	}
}

func TestGroupServiceDeleteNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newGroupServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	err := client.Groups().Delete(context.Background(), testGroupOrgID, testGroupID)
	if !circleci.IsNotFound(err) {
		t.Errorf("Delete error = %v, want a not found error", err)
	}
}

func TestGroupServiceListDrainsPages(t *testing.T) {
	t.Parallel()

	pages := []string{
		`{"items":[{"id":"g1","name":"one","description":"first"}],"next_page_token":"tok-2"}`,
		`{"items":[{"id":"g2","name":"two","description":"second"}],"next_page_token":null}`,
	}

	var calls int
	client, seen := newGroupServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := pages[min(calls, len(pages)-1)]
		calls++
		_, _ = w.Write([]byte(body))
	})

	groups, err := client.Groups().List(context.Background(), testGroupOrgID)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}

	if len(groups) != 2 {
		t.Fatalf("group count = %d, want 2 (both pages drained)", len(groups))
	}
	if groups[0].ID != "g1" || groups[1].ID != "g2" {
		t.Errorf("group ids = %q, %q, want g1, g2", groups[0].ID, groups[1].ID)
	}

	if len(*seen) != 2 {
		t.Fatalf("request count = %d, want 2", len(*seen))
	}

	wantPath := "/api/v2/organizations/" + testGroupOrgID + "/groups"
	// The first page must not send page-token at all, and the second must send
	// the token the first page returned.
	if got := (*seen)[0]; got.path != wantPath || got.query != "" {
		t.Errorf("first request = %s?%s, want %s with no query", got.path, got.query, wantPath)
	}
	if got := (*seen)[1]; got.path != wantPath || got.query != "page-token=tok-2" {
		t.Errorf("second request = %s?%s, want %s?page-token=tok-2", got.path, got.query, wantPath)
	}
}

func TestGroupServiceListEmpty(t *testing.T) {
	t.Parallel()

	client, _ := newGroupServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"next_page_token":null}`))
	})

	groups, err := client.Groups().List(context.Background(), testGroupOrgID)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("group count = %d, want 0", len(groups))
	}
}

func TestGroupServiceListStopsWhenTokenRepeats(t *testing.T) {
	t.Parallel()

	// A server that always echoes a token must not spin forever: an empty page
	// terminates the drain.
	var calls int
	client, _ := newGroupServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"next_page_token":"same"}`))
	})

	if _, err := client.Groups().List(context.Background(), testGroupOrgID); err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if calls != 1 {
		t.Errorf("request count = %d, want 1", calls)
	}
}

func TestGroupServiceEscapesRouteParams(t *testing.T) {
	t.Parallel()

	// Ids arrive from configuration and state, so anything path-significant in
	// them must be escaped rather than changing which route is called.
	client, seen := newGroupServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	if err := client.Groups().Delete(context.Background(), "org/../evil", testGroupID); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}

	// Assert on the raw request line: r.URL.Path is already percent-decoded, so
	// it would look the same whether or not the value was escaped.
	wantURI := "/api/v2/organizations/org%2F..%2Fevil/groups/" + testGroupID
	if got := (*seen)[0].rawURI; got != wantURI {
		t.Errorf("raw request URI = %q, want %q", got, wantURI)
	}
}
