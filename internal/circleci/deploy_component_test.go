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
