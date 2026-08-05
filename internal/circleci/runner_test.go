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

// newRunnerClient starts a fake runner API and returns a Client whose
// runner_host points at it. Host is deliberately unroutable: every method
// under test goes to the runner host, never the main API host, and a
// connection error there is a clearer failure than a silent success.
func newRunnerClient(t *testing.T, handler http.HandlerFunc) *circleci.Client {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return circleci.New(circleci.Config{
		Host:       "http://127.0.0.1:1",
		RunnerHost: srv.URL,
		Token:      "tok",
	})
}

func TestListResourceClasses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		namespace string
		orgID     string
		body      string
		wantQuery string
		wantLen   int
	}{
		{
			name:      "by namespace",
			namespace: "acme",
			body:      `{"items":[{"id":"11111111-2222-3333-4444-555555555555","resource_class":"acme/linux","description":"linux runners"}]}`,
			wantQuery: "namespace=acme",
			wantLen:   1,
		},
		{
			name:  "by org id",
			orgID: "00000000-1111-2222-3333-444444444444",
			body:  `{"items":[{"id":"11111111-2222-3333-4444-555555555555","resource_class":"acme/linux","description":""}]}`,
			// org-id is checked before namespace, and namespace is not sent
			// at all when both are set.
			wantQuery: "org-id=00000000-1111-2222-3333-444444444444",
			wantLen:   1,
		},
		{
			name:      "no results is an empty slice not an error",
			namespace: "acme",
			body:      `{"items":[]}`,
			wantQuery: "namespace=acme",
			wantLen:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotMethod, gotPath, gotQuery string

			client := newRunnerClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				gotQuery = r.URL.RawQuery

				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tt.body)
			})

			classes, err := client.ListResourceClasses(context.Background(), tt.namespace, tt.orgID)
			if err != nil {
				t.Fatalf("ListResourceClasses returned error: %v", err)
			}

			if gotMethod != http.MethodGet {
				t.Errorf("method = %q, want GET", gotMethod)
			}
			if gotPath != "/api/v3/runner/resource" {
				t.Errorf("path = %q, want /api/v3/runner/resource", gotPath)
			}
			if gotQuery != tt.wantQuery {
				t.Errorf("query = %q, want %q", gotQuery, tt.wantQuery)
			}
			if len(classes) != tt.wantLen {
				t.Fatalf("len(classes) = %d, want %d", len(classes), tt.wantLen)
			}
		})
	}
}

// TestListResourceClassesIgnoresListOnlyFields pins down the fact that the list
// route and the create route answer with different shapes.
//
// The list route renders id, resource_class, description plus active_tasks
// and an embedded runners array, while the create route renders only the
// narrower id, resource_class, description. ResourceClass decodes both
// because it names only the three fields they have in common, and the two
// extras are dropped rather than causing an error. The fixture below is the
// *list* shape, so this fails if the struct ever grows a strict decoder or if
// someone "simplifies" the fixture to the create shape and thereby stops
// covering the route the client actually calls.
func TestListResourceClassesIgnoresListOnlyFields(t *testing.T) {
	t.Parallel()

	client := newRunnerClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[{
			"id": "11111111-2222-3333-4444-555555555555",
			"resource_class": "acme/linux",
			"description": "linux runners",
			"active_tasks": 2,
			"runners": [
				{"name":"acme/linux/agent-1","hostname":"host-1","resource_class":"acme/linux","first_connected":"2026-01-01T00:00:00Z","last_connected":"2026-01-02T00:00:00Z","last_used":null,"version":"1.2.3","status":"busy"}
			]
		}]}`)
	})

	classes, err := client.ListResourceClasses(context.Background(), "acme", "")
	if err != nil {
		t.Fatalf("ListResourceClasses returned error: %v", err)
	}

	if len(classes) != 1 {
		t.Fatalf("len(classes) = %d, want 1", len(classes))
	}
	if classes[0].ResourceClass != "acme/linux" {
		t.Errorf("ResourceClass = %q, want %q", classes[0].ResourceClass, "acme/linux")
	}
	if classes[0].Description != "linux runners" {
		t.Errorf("Description = %q, want %q", classes[0].Description, "linux runners")
	}
}

// TestListRunnersOmitsStatusOutsideAnOrgListing records that `status` is not a
// property of a runner but of the listing it came from.
//
// Only an org-id-scoped listing populates a runner's status; a resource-class
// or namespace listing never does, and the field is `json:"status,omitempty"`
// — so the key is absent from the response, not present and empty. Status must
// therefore decode to "" rather than the client inventing a value, and the two
// values an org-scoped listing does send are "busy" and "idle" — never
// "running", which the API never produces.
func TestListRunnersOmitsStatusOutsideAnOrgListing(t *testing.T) {
	t.Parallel()

	client := newRunnerClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// No status key: this is a resource-class-scoped listing.
		_, _ = io.WriteString(w, `{"items":[
			{"name":"acme/linux/agent-1","hostname":"host-1","resource_class":"acme/linux","first_connected":"2026-01-01T00:00:00Z","last_connected":"2026-01-02T00:00:00Z","last_used":null,"ip":"10.0.0.1","version":"1.2.3"}
		]}`)
	})

	runners, err := client.ListRunners(context.Background(), circleci.ListRunnersParams{ResourceClass: "acme/linux"})
	if err != nil {
		t.Fatalf("ListRunners returned error: %v", err)
	}

	if len(runners) != 1 {
		t.Fatalf("len(runners) = %d, want 1", len(runners))
	}
	if runners[0].Status != "" {
		t.Errorf("Status = %q, want empty: only an org-scoped listing carries one", runners[0].Status)
	}
	if runners[0].Hostname != "host-1" {
		t.Errorf("Hostname = %q, want %q", runners[0].Hostname, "host-1")
	}
}

func TestListResourceClassesDoesNotErrorOnEmptyFilters(t *testing.T) {
	t.Parallel()

	// The caller (the provider's ConfigValidators) is responsible for requiring
	// at least one filter; the client itself sends whatever it is given and lets
	// the API answer.
	client := newRunnerClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"message":"missing namespace or org-id parameter"}`)
	})

	_, err := client.ListResourceClasses(context.Background(), "", "")
	if err == nil {
		t.Fatal("ListResourceClasses returned no error for an unfiltered list, want the API's 400")
	}
}

func TestCreateResourceClass(t *testing.T) {
	t.Parallel()

	var (
		gotMethod string
		gotPath   string
		gotBody   []byte
	)

	client := newRunnerClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"11111111-2222-3333-4444-555555555555","resource_class":"acme/linux","description":"linux runners"}`)
	})

	created, err := client.CreateResourceClass(context.Background(), circleci.ResourceClassInput{
		OrganizationID: "00000000-1111-2222-3333-444444444444",
		ResourceClass:  "acme/linux",
		Description:    "linux runners",
	})
	if err != nil {
		t.Fatalf("CreateResourceClass returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v3/runner/resource" {
		t.Errorf("path = %q, want /api/v3/runner/resource", gotPath)
	}

	var body map[string]any
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("request body %q is not JSON: %v", gotBody, err)
	}
	if body["org_id"] != "00000000-1111-2222-3333-444444444444" {
		t.Errorf("request body org_id = %v, want the organization id", body["org_id"])
	}
	if body["resource_class"] != "acme/linux" {
		t.Errorf("request body resource_class = %v, want %q", body["resource_class"], "acme/linux")
	}
	if body["description"] != "linux runners" {
		t.Errorf("request body description = %v, want %q", body["description"], "linux runners")
	}

	if created.ID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("ID = %q, want the id from the response", created.ID)
	}
	if created.ResourceClass != "acme/linux" {
		t.Errorf("ResourceClass = %q, want %q", created.ResourceClass, "acme/linux")
	}
	if created.Description != "linux runners" {
		t.Errorf("Description = %q, want %q", created.Description, "linux runners")
	}
}

func TestDeleteResourceClass(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		force    bool
		wantPath string
	}{
		{name: "without force", force: false, wantPath: "/api/v3/runner/resource/11111111-2222-3333-4444-555555555555"},
		{name: "with force", force: true, wantPath: "/api/v3/runner/resource/11111111-2222-3333-4444-555555555555/force"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotMethod, gotPath string

			client := newRunnerClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				w.WriteHeader(http.StatusNoContent)
			})

			err := client.DeleteResourceClass(context.Background(), "11111111-2222-3333-4444-555555555555", tt.force)
			if err != nil {
				t.Fatalf("DeleteResourceClass returned error: %v", err)
			}

			if gotMethod != http.MethodDelete {
				t.Errorf("method = %q, want DELETE", gotMethod)
			}
			if gotPath != tt.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tt.wantPath)
			}
		})
	}
}

func TestDeleteResourceClassNotFound(t *testing.T) {
	t.Parallel()

	// The delete route answers 404 for both a genuinely missing resource class
	// and an unauthorized caller, so this is the same status either way.
	client := newRunnerClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"not found with provided token: check permissions to view or admin self-hosted runners"}`)
	})

	err := client.DeleteResourceClass(context.Background(), "missing", false)
	if err == nil {
		t.Fatal("DeleteResourceClass returned no error for a 404, want one")
	}
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

func TestCreateToken(t *testing.T) {
	t.Parallel()

	var (
		gotMethod string
		gotPath   string
		gotBody   []byte
	)

	client := newRunnerClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{
			"id": "11111111-2222-3333-4444-555555555555",
			"resource_class": "acme/linux",
			"nickname": "ci-1",
			"created_at": "2026-01-01T00:00:00Z",
			"token": "secret-token-value"
		}`)
	})

	created, err := client.CreateToken(context.Background(), circleci.TokenInput{
		OrganizationID: "00000000-1111-2222-3333-444444444444",
		ResourceClass:  "acme/linux",
		Nickname:       "ci-1",
	})
	if err != nil {
		t.Fatalf("CreateToken returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v3/runner/token" {
		t.Errorf("path = %q, want /api/v3/runner/token", gotPath)
	}

	var body map[string]any
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("request body %q is not JSON: %v", gotBody, err)
	}
	if body["resource_class"] != "acme/linux" || body["nickname"] != "ci-1" {
		t.Errorf("request body = %v, want resource_class=acme/linux nickname=ci-1", body)
	}

	// The token secret is disclosed only here, at creation.
	if created.Token != "secret-token-value" {
		t.Errorf("Token = %q, want the secret from the create response", created.Token)
	}
	if created.ID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("ID = %q, want the id from the response", created.ID)
	}
	if created.CreatedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("CreatedAt = %q, want the timestamp verbatim", created.CreatedAt)
	}
}

func TestListTokensOmitsSecret(t *testing.T) {
	t.Parallel()

	var gotQuery string

	client := newRunnerClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery

		w.Header().Set("Content-Type", "application/json")
		// The API never sends the secret on a list response; the field is
		// entirely absent (json:"token,omitempty" on an empty string), not
		// present-and-empty.
		_, _ = io.WriteString(w, `{"items":[
			{"id":"11111111-2222-3333-4444-555555555555","resource_class":"acme/linux","nickname":"ci-1","created_at":"2026-01-01T00:00:00Z"}
		]}`)
	})

	tokens, err := client.ListTokens(context.Background(), "acme/linux")
	if err != nil {
		t.Fatalf("ListTokens returned error: %v", err)
	}

	if gotQuery != "resource-class=acme%2Flinux" {
		t.Errorf("query = %q, want resource-class=acme%%2Flinux", gotQuery)
	}
	if len(tokens) != 1 {
		t.Fatalf("len(tokens) = %d, want 1", len(tokens))
	}
	if tokens[0].Token != "" {
		t.Errorf("Token = %q, want empty: the API never discloses it on a list", tokens[0].Token)
	}
	if tokens[0].Nickname != "ci-1" {
		t.Errorf("Nickname = %q, want %q", tokens[0].Nickname, "ci-1")
	}
}

func TestDeleteToken(t *testing.T) {
	t.Parallel()

	var gotMethod, gotPath string

	client := newRunnerClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	if err := client.DeleteToken(context.Background(), "11111111-2222-3333-4444-555555555555"); err != nil {
		t.Fatalf("DeleteToken returned error: %v", err)
	}

	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if gotPath != "/api/v3/runner/token/11111111-2222-3333-4444-555555555555" {
		t.Errorf("path = %q, want /api/v3/runner/token/11111111-2222-3333-4444-555555555555", gotPath)
	}
}

func TestDeleteTokenNotFound(t *testing.T) {
	t.Parallel()

	client := newRunnerClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"not found"}`)
	})

	err := client.DeleteToken(context.Background(), "missing")
	if err == nil {
		t.Fatal("DeleteToken returned no error for a 404, want one")
	}
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

func TestUnclaimedTaskCount(t *testing.T) {
	t.Parallel()

	var gotPath, gotQuery string

	client := newRunnerClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"unclaimed_task_count":7}`)
	})

	count, err := client.UnclaimedTaskCount(context.Background(), "acme/linux")
	if err != nil {
		t.Fatalf("UnclaimedTaskCount returned error: %v", err)
	}

	if gotPath != "/api/v3/runner/tasks" {
		t.Errorf("path = %q, want /api/v3/runner/tasks", gotPath)
	}
	if gotQuery != "resource-class=acme%2Flinux" {
		t.Errorf("query = %q, want resource-class=acme%%2Flinux", gotQuery)
	}
	if count != 7 {
		t.Errorf("count = %d, want 7", count)
	}
}

func TestUnclaimedTaskCountNotFound(t *testing.T) {
	t.Parallel()

	// The route answers 404 "resource class not found" when the resource
	// class does not resolve.
	client := newRunnerClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"resource class not found"}`)
	})

	_, err := client.UnclaimedTaskCount(context.Background(), "acme/missing")
	if err == nil {
		t.Fatal("UnclaimedTaskCount returned no error for a 404, want one")
	}
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

func TestRunningTaskCount(t *testing.T) {
	t.Parallel()

	var gotPath, gotQuery string

	client := newRunnerClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"running_runner_tasks":3}`)
	})

	count, err := client.RunningTaskCount(context.Background(), "acme/linux")
	if err != nil {
		t.Fatalf("RunningTaskCount returned error: %v", err)
	}

	if gotPath != "/api/v3/runner/tasks/running" {
		t.Errorf("path = %q, want /api/v3/runner/tasks/running", gotPath)
	}
	if gotQuery != "resource-class=acme%2Flinux" {
		t.Errorf("query = %q, want resource-class=acme%%2Flinux", gotQuery)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

func TestRunningTaskCountNotFound(t *testing.T) {
	t.Parallel()

	client := newRunnerClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"resource class not found"}`)
	})

	_, err := client.RunningTaskCount(context.Background(), "acme/missing")
	if err == nil {
		t.Fatal("RunningTaskCount returned no error for a 404, want one")
	}
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

func TestListRunnersEnvelope(t *testing.T) {
	t.Parallel()

	// Regression coverage for the bug DESIGN.md records: the API answers
	// `{"items": [...]}`, not a bare array, and both circleci-sdk-go and its
	// own test fake decoded a bare array, so ListRunners always returned
	// empty against production.
	var gotPath, gotQuery string

	client := newRunnerClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[
			{"name":"acme/linux/agent-1","hostname":"host-1","resource_class":"acme/linux","first_connected":"2026-01-01T00:00:00Z","last_connected":"2026-01-02T00:00:00Z","last_used":null,"ip":"10.0.0.1","version":"1.2.3","status":"idle"}
		]}`)
	})

	runners, err := client.ListRunners(context.Background(), circleci.ListRunnersParams{ResourceClass: "acme/linux"})
	if err != nil {
		t.Fatalf("ListRunners returned error: %v", err)
	}

	if gotPath != "/api/v3/runner" {
		t.Errorf("path = %q, want /api/v3/runner", gotPath)
	}
	if gotQuery != "resource-class=acme%2Flinux" {
		t.Errorf("query = %q, want resource-class=acme%%2Flinux", gotQuery)
	}
	if len(runners) != 1 {
		t.Fatalf("len(runners) = %d, want 1 (the envelope must be unwrapped)", len(runners))
	}
	if runners[0].LastUsed != nil {
		t.Errorf("LastUsed = %v, want nil for an agent that has never claimed a task", runners[0].LastUsed)
	}
}
