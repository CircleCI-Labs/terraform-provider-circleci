// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

const (
	testProjectGroupOrgID     = "f9101358-f810-427c-8742-bab7303a96ef"
	testProjectGroupProjectID = "a8991d85-ae2f-4582-8c46-314837ec2756"
	testProjectGroupGroupID   = "b30e974d-da2c-425e-b88c-8a828d726760"
)

// projectGroupRequest is one request as the mock server saw it.
type projectGroupRequest struct {
	method string
	path   string
	rawURI string
	query  string
	body   string
}

// newProjectGroupServer serves the project group routes from handler and records
// every request, so tests can assert on the exact paths and payloads sent.
func newProjectGroupServer(t *testing.T, handler http.HandlerFunc) (*circleci.Client, *[]projectGroupRequest) {
	t.Helper()

	var seen []projectGroupRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		seen = append(seen, projectGroupRequest{
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

func TestProjectRoles(t *testing.T) {
	t.Parallel()

	// The org-level roles are not valid on a project grant, so the list must be
	// exactly these three.
	want := []string{"project-admin", "project-contributor", "project-viewer"}
	if got := circleci.ProjectRoles(); !slices.Equal(got, want) {
		t.Errorf("ProjectRoles() = %v, want %v", got, want)
	}
}

func TestProjectGroupServiceList(t *testing.T) {
	t.Parallel()

	client, seen := newProjectGroupServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
			{"id":"` + testProjectGroupGroupID + `","name":"CIAM","role":"project-admin"},
			{"id":"22222222-2222-2222-2222-222222222222","name":"API","role":"project-contributor"}
		]}`))
	})

	groups, err := client.ProjectGroups().List(context.Background(), testProjectGroupOrgID, testProjectGroupProjectID)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}

	if len(groups) != 2 {
		t.Fatalf("group count = %d, want 2", len(groups))
	}
	if groups[0].ID != testProjectGroupGroupID {
		t.Errorf("group id = %q, want %q", groups[0].ID, testProjectGroupGroupID)
	}
	if groups[0].Name != "CIAM" {
		t.Errorf("group name = %q, want %q", groups[0].Name, "CIAM")
	}
	if groups[0].Role != circleci.ProjectRoleAdmin {
		t.Errorf("group role = %q, want %q", groups[0].Role, circleci.ProjectRoleAdmin)
	}
	if groups[1].Role != circleci.ProjectRoleContributor {
		t.Errorf("second group role = %q, want %q", groups[1].Role, circleci.ProjectRoleContributor)
	}

	wantPath := "/api/v2/organizations/" + testProjectGroupOrgID +
		"/projects/" + testProjectGroupProjectID + "/groups"
	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}
	if req := (*seen)[0]; req.method != http.MethodGet || req.path != wantPath {
		t.Errorf("request = %s %s, want GET %s", req.method, req.path, wantPath)
	}
	// The response carries no next_page_token, so no second page is requested.
	if query := (*seen)[0].query; query != "" {
		t.Errorf("request query = %q, want no query on the first page", query)
	}
}

func TestProjectGroupServiceListEmpty(t *testing.T) {
	t.Parallel()

	client, _ := newProjectGroupServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	})

	groups, err := client.ProjectGroups().List(context.Background(), testProjectGroupOrgID, testProjectGroupProjectID)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("group count = %d, want 0", len(groups))
	}
}

func TestProjectGroupServiceGet(t *testing.T) {
	t.Parallel()

	client, _ := newProjectGroupServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
			{"id":"11111111-1111-1111-1111-111111111111","name":"Other","role":"project-viewer"},
			{"id":"` + testProjectGroupGroupID + `","name":"CIAM","role":"project-admin"}
		]}`))
	})

	group, err := client.ProjectGroups().Get(context.Background(),
		testProjectGroupOrgID, testProjectGroupProjectID, testProjectGroupGroupID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if group.ID != testProjectGroupGroupID {
		t.Errorf("group id = %q, want %q", group.ID, testProjectGroupGroupID)
	}
	if group.Role != circleci.ProjectRoleAdmin {
		t.Errorf("group role = %q, want %q", group.Role, circleci.ProjectRoleAdmin)
	}
	if group.Name != "CIAM" {
		t.Errorf("group name = %q, want %q", group.Name, "CIAM")
	}
}

func TestProjectGroupServiceGetNotAssignedIsNotFound(t *testing.T) {
	t.Parallel()

	// There is no route for a single project group, so Get filters the list. A
	// group with no grant must report not found, which is what lets the resource
	// detect a grant revoked in the web UI as drift.
	client, _ := newProjectGroupServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":"11111111-1111-1111-1111-111111111111","name":"Other","role":"project-viewer"}]}`))
	})

	_, err := client.ProjectGroups().Get(context.Background(),
		testProjectGroupOrgID, testProjectGroupProjectID, testProjectGroupGroupID)
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

func TestProjectGroupServiceGetPropagatesErrors(t *testing.T) {
	t.Parallel()

	// A 403 must not be flattened into "not assigned": that would drop a live
	// grant out of state whenever the token loses permission.
	client, _ := newProjectGroupServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Permission denied."}`))
	})

	_, err := client.ProjectGroups().Get(context.Background(),
		testProjectGroupOrgID, testProjectGroupProjectID, testProjectGroupGroupID)
	if err == nil {
		t.Fatal("Get returned no error for a 403, want one")
	}
	if circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = true, want false for a 403", err)
	}
	if !circleci.IsUnauthorized(err) {
		t.Errorf("IsUnauthorized(%v) = false, want true", err)
	}
}

// TestProjectGroupServiceGetDistinguishesTransport404FromEmptyList is the
// counterpart to TestProjectGroupServiceGetNotAssignedIsNotFound and
// TestProjectGroupServiceGetPropagatesErrors: there is no single-grant route,
// so Get always works by listing the project's groups and matching on id.
// circleci.IsNotFound says yes to both a transport 404 from that list call
// and the bare ErrNotFound this package returns for a successful-but-empty
// match, but only the latter means the grant was actually revoked. A caller
// that checks the broader IsNotFound, rather than
// errors.Is(err, circleci.ErrNotFound), cannot tell a revoked grant from an
// organization or project the token has lost access to.
//
// [NET, 2026-08-21] The real route answers that case with 403 "Permission
// denied.", not 404 -- confirmed against a real organization with a bogus
// project id, and separately with both a bogus organization and project id.
// This test still fakes a 404 to prove the *mechanism* generically covers a
// transport failure, distinct from the emptyList case above; the 403 case
// used in production is exercised by TestProjectGroupServiceGetPropagatesErrors.
func TestProjectGroupServiceGetDistinguishesTransport404FromEmptyList(t *testing.T) {
	t.Parallel()

	client, _ := newProjectGroupServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Project not found"}`))
	})

	_, err := client.ProjectGroups().Get(context.Background(),
		testProjectGroupOrgID, testProjectGroupProjectID, testProjectGroupGroupID)
	if err == nil {
		t.Fatal("Get returned no error for a 404 from the list, want one")
	}
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true: a transport 404 still satisfies it", err)
	}
	if errors.Is(err, circleci.ErrNotFound) {
		t.Errorf("errors.Is(%v, ErrNotFound) = true, want false: a 404 from the list call itself "+
			"is not the same as the grant being absent from a successful list", err)
	}
}

func TestProjectGroupServiceAssign(t *testing.T) {
	t.Parallel()

	client, seen := newProjectGroupServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"Project groups updated."}`))
	})

	err := client.ProjectGroups().Assign(context.Background(),
		testProjectGroupOrgID, testProjectGroupProjectID, circleci.ProjectRoleViewer,
		[]string{testProjectGroupGroupID})
	if err != nil {
		t.Fatalf("Assign returned error: %v", err)
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}
	got := (*seen)[0]

	wantPath := "/api/v2/organizations/" + testProjectGroupOrgID +
		"/projects/" + testProjectGroupProjectID + "/groups"
	if got.method != http.MethodPost || got.path != wantPath {
		t.Errorf("request = %s %s, want POST %s", got.method, got.path, wantPath)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(got.body), &payload); err != nil {
		t.Fatalf("request body %q is not JSON: %v", got.body, err)
	}
	if len(payload) != 2 {
		t.Errorf("request body = %v, want exactly role and group_ids", payload)
	}
	if payload["role"] != circleci.ProjectRoleViewer {
		t.Errorf("request body role = %v, want %q", payload["role"], circleci.ProjectRoleViewer)
	}
	groupIDs, ok := payload["group_ids"].([]any)
	if !ok {
		t.Fatalf("request body group_ids = %v, want an array", payload["group_ids"])
	}
	if len(groupIDs) != 1 || groupIDs[0] != testProjectGroupGroupID {
		t.Errorf("request body group_ids = %v, want [%q]", groupIDs, testProjectGroupGroupID)
	}
}

func TestProjectGroupServiceAssignSkipsEmpty(t *testing.T) {
	t.Parallel()

	// The endpoint answers 400 "No valid group_ids provided" for an empty array.
	client, seen := newProjectGroupServer(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("server was called for an empty group id list")
		w.WriteHeader(http.StatusBadRequest)
	})

	err := client.ProjectGroups().Assign(context.Background(),
		testProjectGroupOrgID, testProjectGroupProjectID, circleci.ProjectRoleViewer, nil)
	if err != nil {
		t.Errorf("Assign with no groups returned error: %v", err)
	}
	if len(*seen) != 0 {
		t.Errorf("request count = %d, want 0", len(*seen))
	}
}

func TestProjectGroupServiceUpdateRole(t *testing.T) {
	t.Parallel()

	client, seen := newProjectGroupServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"Project group role updated."}`))
	})

	err := client.ProjectGroups().UpdateRole(context.Background(),
		testProjectGroupOrgID, testProjectGroupProjectID, testProjectGroupGroupID,
		circleci.ProjectRoleAdmin)
	if err != nil {
		t.Fatalf("UpdateRole returned error: %v", err)
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}
	got := (*seen)[0]

	// The role change is a POST to a distinct /update-role action, not a PUT or
	// PATCH on the grant.
	wantPath := "/api/v2/organizations/" + testProjectGroupOrgID +
		"/projects/" + testProjectGroupProjectID +
		"/groups/" + testProjectGroupGroupID + "/update-role"
	if got.method != http.MethodPost || got.path != wantPath {
		t.Errorf("request = %s %s, want POST %s", got.method, got.path, wantPath)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(got.body), &payload); err != nil {
		t.Fatalf("request body %q is not JSON: %v", got.body, err)
	}
	// The update-role body carries only the role; the group is in the path.
	if len(payload) != 1 || payload["role"] != circleci.ProjectRoleAdmin {
		t.Errorf("request body = %v, want only role=%q", payload, circleci.ProjectRoleAdmin)
	}
}

func TestProjectGroupServiceEscapesRouteParams(t *testing.T) {
	t.Parallel()

	client, seen := newProjectGroupServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"ok"}`))
	})

	err := client.ProjectGroups().UpdateRole(context.Background(),
		"org/../evil", "project/../evil", "group/../evil", circleci.ProjectRoleViewer)
	if err != nil {
		t.Fatalf("UpdateRole returned error: %v", err)
	}

	wantURI := "/api/v2/organizations/org%2F..%2Fevil/projects/project%2F..%2Fevil" +
		"/groups/group%2F..%2Fevil/update-role"
	if got := (*seen)[0].rawURI; got != wantURI {
		t.Errorf("raw request URI = %q, want %q", got, wantURI)
	}
}
