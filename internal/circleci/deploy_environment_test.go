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

const testDeployOrgID = "3ddcf1d1-7f5f-4139-8cef-71ad0921a968"

// deployRequest is one request as a mock deploy server saw it.
type deployRequest struct {
	method string
	path   string
	query  string
}

// newDeployServer serves deploy routes shaped like the real API from handler
// and records every request, so tests can assert on the exact paths and query
// strings sent.
func newDeployServer(t *testing.T, handler http.HandlerFunc) (*circleci.Client, *[]deployRequest) {
	t.Helper()

	var seen []deployRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, deployRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery})
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	return circleci.New(circleci.Config{Host: srv.URL, Token: "tok"}), &seen
}

func TestDeployEnvironmentServiceGet(t *testing.T) {
	t.Parallel()

	const envID = "9f1c2f6a-1a2b-4c3d-8e9f-0a1b2c3d4e5f"

	client, seen := newDeployServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "` + envID + `",
			"name": "prod-app",
			"created_at": "2024-04-24T15:10:21.123Z",
			"updated_at": "2024-04-24T15:10:21.123Z",
			"labels": [{"key": "env", "value": "prod"}],
			"description": "Production environment"
		}`))
	})

	env, err := client.DeployEnvironments().Get(context.Background(), envID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	if env.ID != envID {
		t.Errorf("env id = %q, want %q", env.ID, envID)
	}
	if env.Name != "prod-app" {
		t.Errorf("env name = %q, want %q", env.Name, "prod-app")
	}
	if len(env.Labels) != 1 || env.Labels[0].Key != "env" || env.Labels[0].Value != "prod" {
		t.Errorf("env labels = %+v, want [{env prod}]", env.Labels)
	}
	if env.Description != "Production environment" {
		t.Errorf("env description = %q, want %q", env.Description, "Production environment")
	}

	wantPath := "/api/v2/deploy/environments/" + envID
	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}
	if got := (*seen)[0]; got.method != http.MethodGet || got.path != wantPath {
		t.Errorf("request = %s %s, want GET %s", got.method, got.path, wantPath)
	}
}

func TestDeployEnvironmentServiceGetNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newDeployServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Resource not found or permission denied"}`))
	})

	_, err := client.DeployEnvironments().Get(context.Background(), "does-not-exist")
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
	if detail := circleci.Detail(err); detail == "" {
		t.Error("Detail() = \"\", want the server message")
	}
}

func TestDeployEnvironmentServiceListDrainsPages(t *testing.T) {
	t.Parallel()

	pages := []string{
		`{"items":[{"id":"e1","name":"one","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z","labels":[]}],"next_page_token":"tok-2"}`,
		`{"items":[{"id":"e2","name":"two","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z","labels":[]}],"next_page_token":null}`,
	}

	var calls int
	client, seen := newDeployServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := pages[min(calls, len(pages)-1)]
		calls++
		_, _ = w.Write([]byte(body))
	})

	envs, err := client.DeployEnvironments().List(context.Background(), testDeployOrgID)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(envs) != 2 {
		t.Fatalf("env count = %d, want 2 (both pages drained)", len(envs))
	}
	if envs[0].ID != "e1" || envs[1].ID != "e2" {
		t.Errorf("env ids = %q, %q, want e1, e2", envs[0].ID, envs[1].ID)
	}

	if len(*seen) != 2 {
		t.Fatalf("request count = %d, want 2", len(*seen))
	}
	wantPath := "/api/v2/deploy/environments"
	if got := (*seen)[0]; got.path != wantPath || got.query != "org-id="+testDeployOrgID {
		t.Errorf("first request = %s?%s, want %s?org-id=%s", got.path, got.query, wantPath, testDeployOrgID)
	}
	if got := (*seen)[1]; got.query != "org-id="+testDeployOrgID+"&page-token=tok-2" {
		t.Errorf("second request query = %q, want org-id=%s&page-token=tok-2", got.query, testDeployOrgID)
	}
}

func TestDeployEnvironmentServiceListEmpty(t *testing.T) {
	t.Parallel()

	client, _ := newDeployServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"next_page_token":null}`))
	})

	envs, err := client.DeployEnvironments().List(context.Background(), testDeployOrgID)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(envs) != 0 {
		t.Errorf("env count = %d, want 0", len(envs))
	}
}
