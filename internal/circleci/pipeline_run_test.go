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

// v2Request is one request as the mock v2 server saw it.
type v2Request struct {
	method string
	path   string
	rawURI string
}

// newV2Server serves v2-shaped routes from handler and records every request,
// so tests can assert on the exact paths and raw request lines sent.
func newV2Server(t *testing.T, handler http.HandlerFunc) (*circleci.Client, *[]v2Request) {
	t.Helper()

	var seen []v2Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, v2Request{method: r.Method, path: r.URL.Path, rawURI: r.RequestURI})
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	return circleci.New(circleci.Config{Host: srv.URL, Token: "tok"}), &seen
}

func TestPipelineRunServiceGet(t *testing.T) {
	t.Parallel()

	const runID = "5034460f-c7c4-4c43-9457-de07e2029e7b"

	client, seen := newV2Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "` + runID + `",
			"number": 25,
			"project_slug": "gh/CircleCI-Public/api-preview-docs",
			"created_at": "2024-04-24T15:10:21.123Z",
			"errors": [],
			"warnings": [],
			"state": "created",
			"trigger": {
				"type": "webhook",
				"received_at": "2024-04-24T15:10:21.123Z",
				"actor": {"login": "octocat", "avatar_url": "https://example.invalid/a.png"}
			},
			"vcs": {
				"provider_name": "GitHub",
				"origin_repository_url": "https://github.com/CircleCI-Public/api-preview-docs",
				"target_repository_url": "https://github.com/CircleCI-Public/api-preview-docs",
				"revision": "f454a02b5d10fcccfd7d9dd7608a76d6493a98b4",
				"branch": "main"
			}
		}`))
	})

	run, err := client.PipelineRuns().Get(context.Background(), runID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	if run.ID != runID {
		t.Errorf("run id = %q, want %q", run.ID, runID)
	}
	if run.Number != 25 {
		t.Errorf("run number = %d, want 25", run.Number)
	}
	if run.Trigger.Actor.Login != "octocat" {
		t.Errorf("run trigger actor login = %q, want %q", run.Trigger.Actor.Login, "octocat")
	}
	if run.VCS == nil || run.VCS.Branch != "main" {
		t.Errorf("run vcs branch = %v, want main", run.VCS)
	}

	wantPath := "/api/v2/pipeline/" + runID
	if got := (*seen)[0]; got.method != http.MethodGet || got.path != wantPath {
		t.Errorf("request = %s %s, want GET %s", got.method, got.path, wantPath)
	}
}

func TestPipelineRunServiceGetNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newV2Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Pipeline not found"}`))
	})

	_, err := client.PipelineRuns().Get(context.Background(), "does-not-exist")
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

func TestPipelineRunServiceGetByNumber(t *testing.T) {
	t.Parallel()

	client, seen := newV2Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "5034460f-c7c4-4c43-9457-de07e2029e7b",
			"number": 25,
			"project_slug": "gh/CircleCI-Public/api-preview-docs",
			"created_at": "2024-04-24T15:10:21.123Z",
			"errors": [],
			"warnings": [],
			"state": "created",
			"trigger": {"type": "api", "received_at": "2024-04-24T15:10:21.123Z", "actor": {"login": "octocat", "avatar_url": ""}}
		}`))
	})

	run, err := client.PipelineRuns().GetByNumber(context.Background(), "gh/CircleCI-Public/api-preview-docs", 25)
	if err != nil {
		t.Fatalf("GetByNumber returned error: %v", err)
	}
	if run.Number != 25 {
		t.Errorf("run number = %d, want 25", run.Number)
	}

	wantURI := "/api/v2/project/gh/CircleCI-Public/api-preview-docs/pipeline/25"
	if got := (*seen)[0]; got.rawURI != wantURI {
		t.Errorf("raw request URI = %q, want %q", got.rawURI, wantURI)
	}
}

// TestPipelineRunServiceGetByNumberEscapesSlug confirms the project slug's
// separators stay literal ("/") rather than becoming "%2F": CircleCI Server
// does not match a percent-escaped separator against this route.
func TestPipelineRunServiceGetByNumberEscapesSlug(t *testing.T) {
	t.Parallel()

	client, seen := newV2Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "5034460f-c7c4-4c43-9457-de07e2029e7b",
			"number": 1,
			"project_slug": "gh/some org/some/repo",
			"created_at": "2024-04-24T15:10:21.123Z",
			"errors": [],
			"warnings": [],
			"state": "created",
			"trigger": {"type": "api", "received_at": "2024-04-24T15:10:21.123Z", "actor": {"login": "", "avatar_url": ""}}
		}`))
	})

	if _, err := client.PipelineRuns().GetByNumber(context.Background(), "gh/some org/repo", 1); err != nil {
		t.Fatalf("GetByNumber returned error: %v", err)
	}

	wantURI := "/api/v2/project/gh/some%20org/repo/pipeline/1"
	if got := (*seen)[0]; got.rawURI != wantURI {
		t.Errorf("raw request URI = %q, want %q", got.rawURI, wantURI)
	}
}

func TestPipelineRunServiceGetConfig(t *testing.T) {
	t.Parallel()

	const runID = "5034460f-c7c4-4c43-9457-de07e2029e7b"

	client, seen := newV2Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"source": "callithumpian",
			"compiled": "adscititious",
			"setup_config": "mo' repos",
			"compiled_setup_config": "monorepos"
		}`))
	})

	config, err := client.PipelineRuns().GetConfig(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetConfig returned error: %v", err)
	}
	if config.Source != "callithumpian" || config.Compiled != "adscititious" {
		t.Errorf("config = %+v, want source/compiled callithumpian/adscititious", config)
	}
	if config.SetupConfig != "mo' repos" || config.CompiledSetupConfig != "monorepos" {
		t.Errorf("config setup fields = %+v, want mo' repos/monorepos", config)
	}

	wantPath := "/api/v2/pipeline/" + runID + "/config"
	if got := (*seen)[0]; got.path != wantPath {
		t.Errorf("request path = %q, want %q", got.path, wantPath)
	}
}

func TestPipelineRunServiceGetConfigWithoutSetup(t *testing.T) {
	t.Parallel()

	client, _ := newV2Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"source": "callithumpian", "compiled": "adscititious"}`))
	})

	config, err := client.PipelineRuns().GetConfig(context.Background(), "id")
	if err != nil {
		t.Fatalf("GetConfig returned error: %v", err)
	}
	if config.SetupConfig != "" || config.CompiledSetupConfig != "" {
		t.Errorf("config setup fields = %+v, want both empty when the API omits them", config)
	}
}
