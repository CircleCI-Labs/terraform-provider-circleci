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

const testDefinitionProjectID = "33333333-3333-3333-3333-333333333333"

func TestListPipelineDefinitions(t *testing.T) {
	t.Parallel()

	// The shape matches what the API actually returns: an {"items": [...]}
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

// recordedBodyServer captures method, request URI and decoded JSON body of the
// last request, then answers 200 with the given body.
//
// It always answers 200: the error paths go through newRecordingServer instead,
// which takes a status. Keeping a status parameter here that only ever receives 200
// would suggest error coverage lives in this helper when it does not.
func recordedBodyServer(t *testing.T, respBody string) (*httptest.Server, *fakeRecordedRequest) {
	t.Helper()

	var rec fakeRecordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
		rec = fakeRecordedRequest{Method: r.Method, Path: r.URL.Path, Body: body}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)

	return srv, &rec
}

// fakeRecordedRequest mirrors internal/provider's helper of the same name, kept
// separate because circleci_test must not import the provider package.
type fakeRecordedRequest struct {
	Method string
	Path   string
	Body   map[string]any
}

func TestCreatePipelineDefinitionRequest(t *testing.T) {
	t.Parallel()

	// The shape matches what the API actually expects: config_source carries
	// provider, repo.external_id and file_path; checkout_source carries
	// provider and repo.external_id. Every field is sent unconditionally.
	srv, rec := recordedBodyServer(t, `{"id":"44444444-4444-4444-4444-444444444444",
		"name":"build","description":"Main pipeline","created_at":"2024-05-01T10:00:00Z",
		"config_source":{"provider":"github_app","file_path":".circleci/config.yml",
		                 "repo":{"full_name":"acme/api","external_id":"123456"}},
		"checkout_source":{"provider":"github_app","repo":{"full_name":"acme/api","external_id":"123456"}}}`)

	client := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	input := circleci.CreatePipelineDefinitionInput{
		Name:        "build",
		Description: "Main pipeline",
		ConfigSource: circleci.PipelineConfigSourceInput{
			Provider: "github_app",
			Repo:     &circleci.RepoInput{ExternalID: "123456"},
			FilePath: ".circleci/config.yml",
		},
		CheckoutSource: circleci.PipelineCheckoutSourceInput{
			Provider: "github_app",
			Repo:     circleci.RepoInput{ExternalID: "123456"},
		},
	}

	created, err := client.CreatePipelineDefinition(context.Background(), testDefinitionProjectID, input)
	if err != nil {
		t.Fatalf("CreatePipelineDefinition returned error: %v", err)
	}

	if rec.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", rec.Method)
	}
	wantPath := "/api/v2/projects/" + testDefinitionProjectID + "/pipeline-definitions"
	if rec.Path != wantPath {
		t.Errorf("path = %q, want %q", rec.Path, wantPath)
	}

	configSource, _ := rec.Body["config_source"].(map[string]any)
	if configSource["provider"] != "github_app" || configSource["file_path"] != ".circleci/config.yml" {
		t.Errorf("request config_source = %v, want github_app/.circleci/config.yml", configSource)
	}
	repo, _ := configSource["repo"].(map[string]any)
	if repo["external_id"] != "123456" {
		t.Errorf("request config_source.repo.external_id = %v, want 123456", repo["external_id"])
	}
	checkoutSource, _ := rec.Body["checkout_source"].(map[string]any)
	if checkoutSource["provider"] != "github_app" {
		t.Errorf("request checkout_source.provider = %v, want github_app", checkoutSource["provider"])
	}

	if created.ID != "44444444-4444-4444-4444-444444444444" || created.Name != "build" {
		t.Errorf("created = %+v, want id 44444444-4444-4444-4444-444444444444 and name build", created)
	}
}

// TestCreatePipelineDefinitionRequestCircleCIConfigSource pins the fix for a
// pipeline definition whose configuration is hosted by CircleCI itself rather than
// a VCS repository.
//
// The API's config_source oneOf has a "circleci" branch that is
// `additionalProperties: false` over only provider and file_path — no repo
// property at all — so sending a repo object on that branch fails the oneOf
// outright rather than being harmlessly ignored. PipelineConfigSourceInput.Repo
// must therefore be omitted entirely, not sent as a repo with an empty
// external_id.
func TestCreatePipelineDefinitionRequestCircleCIConfigSource(t *testing.T) {
	t.Parallel()

	srv, rec := recordedBodyServer(t, `{"id":"44444444-4444-4444-4444-444444444444",
		"name":"build","config_source":{"provider":"circleci","file_path":".circleci/config.yml"},
		"checkout_source":{"provider":"github_app","repo":{"full_name":"acme/api","external_id":"123456"}}}`)

	client := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	input := circleci.CreatePipelineDefinitionInput{
		Name: "build",
		ConfigSource: circleci.PipelineConfigSourceInput{
			Provider: circleci.PipelineConfigSourceProviderCircleCI,
			FilePath: ".circleci/config.yml",
		},
		CheckoutSource: circleci.PipelineCheckoutSourceInput{
			Provider: "github_app",
			Repo:     circleci.RepoInput{ExternalID: "123456"},
		},
	}

	if _, err := client.CreatePipelineDefinition(context.Background(), testDefinitionProjectID, input); err != nil {
		t.Fatalf("CreatePipelineDefinition returned error: %v", err)
	}

	configSource, _ := rec.Body["config_source"].(map[string]any)
	if configSource["provider"] != "circleci" || configSource["file_path"] != ".circleci/config.yml" {
		t.Errorf("request config_source = %v, want circleci/.circleci/config.yml", configSource)
	}
	if _, present := configSource["repo"]; present {
		t.Errorf("request config_source carries repo = %v for a circleci-hosted config source, want it omitted entirely", configSource["repo"])
	}
}

func TestGetPipelineDefinitionRequest(t *testing.T) {
	t.Parallel()

	srv, rec := recordedBodyServer(t, `{"id":"44444444-4444-4444-4444-444444444444",
		"name":"build","config_source":{"provider":"github_app"},"checkout_source":{"provider":"github_app"}}`)

	client := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	found, err := client.GetPipelineDefinition(context.Background(), testDefinitionProjectID, "44444444-4444-4444-4444-444444444444")
	if err != nil {
		t.Fatalf("GetPipelineDefinition returned error: %v", err)
	}

	if rec.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", rec.Method)
	}
	wantPath := "/api/v2/projects/" + testDefinitionProjectID + "/pipeline-definitions/44444444-4444-4444-4444-444444444444"
	if rec.Path != wantPath {
		t.Errorf("path = %q, want %q", rec.Path, wantPath)
	}
	if found.Name != "build" {
		t.Errorf("found.Name = %q, want build", found.Name)
	}
}

func TestGetPipelineDefinitionNotFound(t *testing.T) {
	t.Parallel()

	srv, _ := newRecordingServer(t, http.StatusNotFound, `{"message":"Pipeline definition not found"}`)
	client := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := client.GetPipelineDefinition(context.Background(), testDefinitionProjectID, "nope")
	if !circleci.IsNotFound(err) {
		t.Errorf("GetPipelineDefinition error = %v, want a not found error", err)
	}
}

func TestUpdatePipelineDefinitionRequest(t *testing.T) {
	t.Parallel()

	// Pins a fix at the client layer: the update body's config_source must
	// carry ONLY file_path, since the API's update route has no provider or
	// repo field on config_source at all, while checkout_source carries both
	// provider and repo.external_id, matching the create shape.
	srv, rec := recordedBodyServer(t, `{"id":"44444444-4444-4444-4444-444444444444",
		"name":"build","config_source":{"provider":"github_app","file_path":"new.yml"},
		"checkout_source":{"provider":"github_app","repo":{"external_id":"789"}}}`)

	client := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	input := circleci.UpdatePipelineDefinitionInput{
		Name:        "build",
		Description: "updated",
		ConfigSource: circleci.PipelineConfigSourceUpdateInput{
			FilePath: "new.yml",
		},
		CheckoutSource: circleci.PipelineCheckoutSourceInput{
			Provider: "github_app",
			Repo:     circleci.RepoInput{ExternalID: "789"},
		},
	}

	if _, err := client.UpdatePipelineDefinition(context.Background(), testDefinitionProjectID, "44444444-4444-4444-4444-444444444444", input); err != nil {
		t.Fatalf("UpdatePipelineDefinition returned error: %v", err)
	}

	if rec.Method != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", rec.Method)
	}
	wantPath := "/api/v2/projects/" + testDefinitionProjectID + "/pipeline-definitions/44444444-4444-4444-4444-444444444444"
	if rec.Path != wantPath {
		t.Errorf("path = %q, want %q", rec.Path, wantPath)
	}

	configSource, _ := rec.Body["config_source"].(map[string]any)
	if len(configSource) != 1 {
		t.Errorf("update config_source has keys %v, want only file_path", keysOf(configSource))
	}
	if configSource["file_path"] != "new.yml" {
		t.Errorf("update config_source.file_path = %v, want new.yml", configSource["file_path"])
	}
	checkoutSource, _ := rec.Body["checkout_source"].(map[string]any)
	if checkoutSource["provider"] != "github_app" {
		t.Errorf("update checkout_source.provider = %v, want github_app", checkoutSource["provider"])
	}
}

func TestDeletePipelineDefinitionRequest(t *testing.T) {
	t.Parallel()

	srv, rec := recordedBodyServer(t, `{"message":"Pipeline definition deleted."}`)
	client := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	if err := client.DeletePipelineDefinition(context.Background(), testDefinitionProjectID, "44444444-4444-4444-4444-444444444444"); err != nil {
		t.Fatalf("DeletePipelineDefinition returned error: %v", err)
	}

	if rec.Method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", rec.Method)
	}
	wantPath := "/api/v2/projects/" + testDefinitionProjectID + "/pipeline-definitions/44444444-4444-4444-4444-444444444444"
	if rec.Path != wantPath {
		t.Errorf("path = %q, want %q", rec.Path, wantPath)
	}
}

func TestDeletePipelineDefinitionNotFound(t *testing.T) {
	t.Parallel()

	srv, _ := newRecordingServer(t, http.StatusNotFound, `{"message":"Pipeline definition not found"}`)
	client := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	err := client.DeletePipelineDefinition(context.Background(), testDefinitionProjectID, "nope")
	if !circleci.IsNotFound(err) {
		t.Errorf("DeletePipelineDefinition error = %v, want a not found error", err)
	}
}

// keysOf returns the keys of m, for a readable failure message.
func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	return keys
}
