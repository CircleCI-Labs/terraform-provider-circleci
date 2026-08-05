// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"net/http"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

func TestDeployComponentServiceGet(t *testing.T) {
	t.Parallel()

	const componentID = "9f1c2f6a-1a2b-4c3d-8e9f-0a1b2c3d4e5f"
	const projectID = "1e2d3c4b-5a69-7887-9a0b-1c2d3e4f5061"

	client, seen := newDeployServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "` + componentID + `",
			"project_id": "` + projectID + `",
			"name": "release-agent",
			"release_count": 42,
			"labels": [{"key": "team", "value": "deploy"}],
			"created_at": "2024-04-24T15:10:21.123Z",
			"updated_at": "2024-04-24T15:10:21.123Z"
		}`))
	})

	component, err := client.DeployComponents().Get(context.Background(), componentID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	if component.ID != componentID {
		t.Errorf("component id = %q, want %q", component.ID, componentID)
	}
	if component.ProjectID == nil || *component.ProjectID != projectID {
		t.Errorf("component project id = %v, want %q", component.ProjectID, projectID)
	}
	if component.ReleaseCount != 42 {
		t.Errorf("component release count = %d, want 42", component.ReleaseCount)
	}
	if component.ArchivedAt != nil {
		t.Errorf("component archived at = %v, want nil (omitted by the API)", component.ArchivedAt)
	}

	wantPath := "/api/v2/deploy/components/" + componentID
	if got := (*seen)[0]; got.method != http.MethodGet || got.path != wantPath {
		t.Errorf("request = %s %s, want GET %s", got.method, got.path, wantPath)
	}
}

// TestDeployComponentServiceGetNullProjectID confirms a component with no
// associated CircleCI project decodes project_id:null into a nil pointer
// rather than an empty-string project id, which would look like a real
// (if oddly formed) association in Terraform state.
func TestDeployComponentServiceGetNullProjectID(t *testing.T) {
	t.Parallel()

	client, _ := newDeployServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "c1",
			"project_id": null,
			"name": "shared-lib",
			"release_count": 0,
			"labels": []
		}`))
	})

	component, err := client.DeployComponents().Get(context.Background(), "c1")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if component.ProjectID != nil {
		t.Errorf("component project id = %q, want nil", *component.ProjectID)
	}
}

func TestDeployComponentServiceListFilters(t *testing.T) {
	t.Parallel()

	client, seen := newDeployServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"next_page_token":null}`))
	})

	const projectID = "1e2d3c4b-5a69-7887-9a0b-1c2d3e4f5061"

	if _, err := client.DeployComponents().List(context.Background(), testDeployOrgID, projectID, "release-agent"); err != nil {
		t.Fatalf("List returned error: %v", err)
	}

	// httpcl builds the query string via url.Values, which encodes keys in
	// alphabetical order regardless of the order they were added.
	wantQuery := "component-name=release-agent&org-id=" + testDeployOrgID + "&project-id=" + projectID
	if got := (*seen)[0]; got.query != wantQuery {
		t.Errorf("request query = %q, want %q", got.query, wantQuery)
	}
}

func TestDeployComponentServiceListOmitsEmptyFilters(t *testing.T) {
	t.Parallel()

	client, seen := newDeployServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"next_page_token":null}`))
	})

	if _, err := client.DeployComponents().List(context.Background(), testDeployOrgID, "", ""); err != nil {
		t.Fatalf("List returned error: %v", err)
	}

	wantQuery := "org-id=" + testDeployOrgID
	if got := (*seen)[0]; got.query != wantQuery {
		t.Errorf("request query = %q, want %q (no empty filter params)", got.query, wantQuery)
	}
}

func TestDeployComponentServiceListVersions(t *testing.T) {
	t.Parallel()

	const componentID = "9f1c2f6a-1a2b-4c3d-8e9f-0a1b2c3d4e5f"
	const envID = "1e2d3c4b-5a69-7887-9a0b-1c2d3e4f5061"

	client, seen := newDeployServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"items": [{
				"name": "1.2.3",
				"namespace": "default",
				"environment_id": "` + envID + `",
				"is_live": true,
				"pipeline_id": "` + circleci.ZeroUUID + `",
				"workflow_id": "` + circleci.ZeroUUID + `",
				"job_id": "` + circleci.ZeroUUID + `",
				"last_deployed_at": "2024-04-24T15:10:21.123Z"
			}],
			"next_page_token": null
		}`))
	})

	versions, err := client.DeployComponents().ListVersions(context.Background(), componentID)
	if err != nil {
		t.Fatalf("ListVersions returned error: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("version count = %d, want 1", len(versions))
	}
	if versions[0].PipelineID != circleci.ZeroUUID {
		t.Errorf("version pipeline id = %q, want the all-zero UUID sentinel", versions[0].PipelineID)
	}

	wantPath := "/api/v2/deploy/components/" + componentID + "/versions"
	if got := (*seen)[0]; got.method != http.MethodGet || got.path != wantPath {
		t.Errorf("request = %s %s, want GET %s", got.method, got.path, wantPath)
	}
}

// TestDeployComponentServiceListDrainsPages asserts the component listing
// follows the API's page-token to the end.
//
// The environments listing has an equivalent test; components did not, so
// nothing pinned the *name* of the pagination parameter on this route. Getting
// it wrong is silent: the server ignores the unknown parameter, answers page
// one again with the same token, and a wrong name therefore looks exactly like
// a single-page result on a fixture that only ever returns one page. The second
// request's query string is asserted for that reason.
//
// The API also emits a token for every non-empty page, derived from the last
// item, and only returns "" for an empty page, so the drain must be able to
// stop on an empty final page, which is what the third response here is.
func TestDeployComponentServiceListDrainsPages(t *testing.T) {
	t.Parallel()

	pages := []string{
		`{"items":[{"id":"c1","name":"one","project_id":null,"release_count":1,"labels":[],` +
			`"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}],"next_page_token":"tok-2"}`,
		`{"items":[{"id":"c2","name":"two","project_id":null,"release_count":2,"labels":[],` +
			`"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}],"next_page_token":"tok-3"}`,
		`{"items":[],"next_page_token":""}`,
	}

	var calls int
	client, seen := newDeployServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := pages[min(calls, len(pages)-1)]
		calls++
		_, _ = w.Write([]byte(body))
	})

	components, err := client.DeployComponents().List(context.Background(), testDeployOrgID, "", "")
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(components) != 2 {
		t.Fatalf("component count = %d, want 2 (every page drained)", len(components))
	}
	if components[0].ID != "c1" || components[1].ID != "c2" {
		t.Errorf("component ids = %q, %q, want c1, c2", components[0].ID, components[1].ID)
	}

	if len(*seen) != 3 {
		t.Fatalf("request count = %d, want 3", len(*seen))
	}
	if got := (*seen)[0].query; got != "org-id="+testDeployOrgID {
		t.Errorf("first request query = %q, want no page token", got)
	}
	if got, want := (*seen)[1].query, "org-id="+testDeployOrgID+"&page-token=tok-2"; got != want {
		t.Errorf("second request query = %q, want %q", got, want)
	}
	if got, want := (*seen)[2].query, "org-id="+testDeployOrgID+"&page-token=tok-3"; got != want {
		t.Errorf("third request query = %q, want %q", got, want)
	}
}

// TestDeployComponentServiceListVersionsDrainsPages is the same guarantee for
// the component-versions route, which paginates with its own token derived from
// (name, last_deployed_at).
func TestDeployComponentServiceListVersionsDrainsPages(t *testing.T) {
	t.Parallel()

	const componentID = "9f1c2f6a-1a2b-4c3d-8e9f-0a1b2c3d4e5f"

	pages := []string{
		`{"items":[{"name":"1.2.3","namespace":"default","environment_id":"e1","is_live":false,` +
			`"pipeline_id":"` + circleci.ZeroUUID + `","workflow_id":"` + circleci.ZeroUUID + `",` +
			`"job_id":"` + circleci.ZeroUUID + `","last_deployed_at":"2024-04-24T15:10:21.123Z"}],` +
			`"next_page_token":"tok-2"}`,
		`{"items":[{"name":"1.2.4","namespace":"default","environment_id":"e1","is_live":true,` +
			`"pipeline_id":"3c4d5e6f-7a8b-49c0-91d2-e3f4a5b6c7d8",` +
			`"workflow_id":"5e6f7a8b-9c0d-41e2-a3f4-b5c6d7e8f901",` +
			`"job_id":"7a8b9c0d-1e2f-43a4-b5c6-d7e8f9012345","job_number":17,` +
			`"last_deployed_at":"2024-04-25T15:10:21.123Z"}],"next_page_token":null}`,
	}

	var calls int
	client, seen := newDeployServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := pages[min(calls, len(pages)-1)]
		calls++
		_, _ = w.Write([]byte(body))
	})

	versions, err := client.DeployComponents().ListVersions(context.Background(), componentID)
	if err != nil {
		t.Fatalf("ListVersions returned error: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("version count = %d, want 2 (both pages drained)", len(versions))
	}

	// job_number carries omitempty upstream on an int64, where omitempty does
	// work, so it really is absent on the first row and present on the second.
	// The three UUID fields do not, because omitempty never applies to a
	// fixed-size array like uuid.UUID: they arrive as the all-zero sentinel.
	if versions[0].JobNumber != 0 {
		t.Errorf("first version job number = %d, want 0 (omitted by the API)", versions[0].JobNumber)
	}
	if versions[1].JobNumber != 17 {
		t.Errorf("second version job number = %d, want 17", versions[1].JobNumber)
	}
	if versions[0].PipelineID != circleci.ZeroUUID {
		t.Errorf("first version pipeline id = %q, want the all-zero sentinel", versions[0].PipelineID)
	}
	if versions[1].PipelineID != "3c4d5e6f-7a8b-49c0-91d2-e3f4a5b6c7d8" {
		t.Errorf("second version pipeline id = %q, want the recorded run id", versions[1].PipelineID)
	}

	if len(*seen) != 2 {
		t.Fatalf("request count = %d, want 2", len(*seen))
	}
	wantPath := "/api/v2/deploy/components/" + componentID + "/versions"
	if got := (*seen)[0]; got.path != wantPath || got.query != "" {
		t.Errorf("first request = %s?%s, want %s with no query", got.path, got.query, wantPath)
	}
	if got, want := (*seen)[1].query, "page-token=tok-2"; got != want {
		t.Errorf("second request query = %q, want %q", got, want)
	}
}
