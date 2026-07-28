// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"net/http"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

const testDefinitionProjectID = "33333333-3333-3333-3333-333333333333"

func TestListPipelineDefinitions(t *testing.T) {
	t.Parallel()

	// The shape mirrors the API's
	// the CircleCI API: an {"items": [...]}
	// envelope with no next_page_token, whose entries nest config_source and
	// checkout_source, each with an optional repo.
	client, seen := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListJSON(w, `{"items":[
			{"id":"44444444-4444-4444-4444-444444444444","name":"build","description":"Main pipeline",
			 "created_at":"2024-05-01T10:00:00Z",
			 "config_source":{"provider":"github_app","file_path":".circleci/config.yml",
			                  "repo":{"full_name":"acme/api","external_id":"123456"}},
			 "checkout_source":{"provider":"github_app",
			                    "repo":{"full_name":"acme/api","external_id":"123456"}}},
			{"id":"55555555-5555-5555-5555-555555555555","name":"minimal",
			 "config_source":{"provider":"webhook"},
			 "checkout_source":{"provider":"webhook"}}
		]}`)
	})

	definitions, err := client.ListPipelineDefinitions(context.Background(), testDefinitionProjectID)
	if err != nil {
		t.Fatalf("ListPipelineDefinitions returned error: %v", err)
	}

	if len(definitions) != 2 {
		t.Fatalf("definition count = %d, want 2", len(definitions))
	}

	full := definitions[0]
	if full.Name != "build" || full.Description != "Main pipeline" {
		t.Errorf("first definition = %+v, want build/Main pipeline", full)
	}
	if full.CreatedAt != "2024-05-01T10:00:00Z" {
		t.Errorf("first definition created_at = %q, want the timestamp verbatim", full.CreatedAt)
	}
	if full.ConfigSource.FilePath != ".circleci/config.yml" || full.ConfigSource.Provider != "github_app" {
		t.Errorf("first definition config_source = %+v, want the github_app config source", full.ConfigSource)
	}
	if full.ConfigSource.Repo.FullName != "acme/api" || full.ConfigSource.Repo.ExternalID != "123456" {
		t.Errorf("first definition config repo = %+v, want acme/api with external id 123456", full.ConfigSource.Repo)
	}
	if full.CheckoutSource.Repo.FullName != "acme/api" {
		t.Errorf("first definition checkout repo = %+v, want acme/api", full.CheckoutSource.Repo)
	}

	// The API omits description, created_at and repo entirely when they are
	// unknown, which must decode to zero values rather than failing.
	minimal := definitions[1]
	if minimal.Description != "" || minimal.CreatedAt != "" {
		t.Errorf("second definition = %+v, want empty description and created_at", minimal)
	}
	if minimal.ConfigSource.Repo != (circleci.Repo{}) || minimal.CheckoutSource.Repo != (circleci.Repo{}) {
		t.Errorf("second definition repos = %+v/%+v, want zero values", minimal.ConfigSource.Repo, minimal.CheckoutSource.Repo)
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1 (the endpoint is not paginated)", len(*seen))
	}

	wantPath := "/api/v2/projects/" + testDefinitionProjectID + "/pipeline-definitions"
	if got := (*seen)[0]; got.method != http.MethodGet || got.path != wantPath || got.query != "" {
		t.Errorf("request = %s %s?%s, want GET %s with no query", got.method, got.path, got.query, wantPath)
	}
}

func TestListPipelineDefinitionsEmpty(t *testing.T) {
	t.Parallel()

	client, _ := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListJSON(w, `{"items":[]}`)
	})

	definitions, err := client.ListPipelineDefinitions(context.Background(), testDefinitionProjectID)
	if err != nil {
		t.Fatalf("ListPipelineDefinitions returned error: %v", err)
	}
	if len(definitions) != 0 {
		t.Errorf("definition count = %d, want 0", len(definitions))
	}
}

func TestListPipelineDefinitionsNotFound(t *testing.T) {
	t.Parallel()

	// A CircleCI Server installation answers 404 here because it does not route
	// pipeline-definitions, which is why the data source gates on Cloud first.
	client, _ := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListError(w, http.StatusNotFound, "Project not found.")
	})

	_, err := client.ListPipelineDefinitions(context.Background(), testDefinitionProjectID)
	if !circleci.IsNotFound(err) {
		t.Errorf("ListPipelineDefinitions error = %v, want a not found error", err)
	}
}

func TestListPipelineDefinitionsEscapesRouteParams(t *testing.T) {
	t.Parallel()

	client, seen := newListServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeListJSON(w, `{"items":[]}`)
	})

	if _, err := client.ListPipelineDefinitions(context.Background(), "proj/../evil"); err != nil {
		t.Fatalf("ListPipelineDefinitions returned error: %v", err)
	}

	wantURI := "/api/v2/projects/proj%2F..%2Fevil/pipeline-definitions"
	if got := (*seen)[0].rawURI; got != wantURI {
		t.Errorf("raw request URI = %q, want %q", got, wantURI)
	}
}
