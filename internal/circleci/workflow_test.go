// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"net/http"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

func TestWorkflowServiceGet(t *testing.T) {
	t.Parallel()

	const workflowID = "5034460f-c7c4-4c43-9457-de07e2029e7b"

	client, seen := newV2Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "` + workflowID + `",
			"name": "build-and-test",
			"status": "running",
			"created_at": "2024-04-24T15:10:21.123Z",
			"stopped_at": null,
			"pipeline_id": "1e2d3c4b-5a69-7887-9a0b-1c2d3e4f5061",
			"pipeline_number": 25,
			"project_slug": "gh/CircleCI-Public/api-preview-docs",
			"started_by": "9f1c2f6a-1a2b-4c3d-8e9f-0a1b2c3d4e5f"
		}`))
	})

	workflow, err := client.Workflows().Get(context.Background(), workflowID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if workflow.ID != workflowID {
		t.Errorf("workflow id = %q, want %q", workflow.ID, workflowID)
	}
	if workflow.Status != "running" {
		t.Errorf("workflow status = %q, want running", workflow.Status)
	}
	if workflow.StoppedAt != "" {
		t.Errorf("workflow stopped at = %q, want empty for a running workflow", workflow.StoppedAt)
	}

	wantPath := "/api/v2/workflow/" + workflowID
	if got := (*seen)[0]; got.method != http.MethodGet || got.path != wantPath {
		t.Errorf("request = %s %s, want GET %s", got.method, got.path, wantPath)
	}
}

func TestWorkflowServiceGetNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newV2Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Workflow not found"}`))
	})

	_, err := client.Workflows().Get(context.Background(), "does-not-exist")
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

func TestWorkflowServiceListJobsDrainsPages(t *testing.T) {
	t.Parallel()

	const workflowID = "5034460f-c7c4-4c43-9457-de07e2029e7b"

	pages := []string{
		`{"items":[{"id":"j1","name":"build","type":"build","status":"success","started_at":"2024-04-24T15:10:21.123Z","dependencies":[],"project_slug":"gh/o/r"}],"next_page_token":"tok-2"}`,
		`{"items":[{"id":"j2","name":"test","type":"build","status":"running","started_at":"2024-04-24T15:12:00.000Z","dependencies":["build"],"project_slug":"gh/o/r","requires":{"j1":["success"]}}],"next_page_token":null}`,
	}

	var calls int
	client, seen := newV2Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := pages[min(calls, len(pages)-1)]
		calls++
		_, _ = w.Write([]byte(body))
	})

	jobs, err := client.Workflows().ListJobs(context.Background(), workflowID)
	if err != nil {
		t.Fatalf("ListJobs returned error: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("job count = %d, want 2 (both pages drained)", len(jobs))
	}
	if jobs[0].ID != "j1" || jobs[1].ID != "j2" {
		t.Errorf("job ids = %q, %q, want j1, j2", jobs[0].ID, jobs[1].ID)
	}
	if jobs[1].Requires["j1"][0] != "success" {
		t.Errorf("job requires = %+v, want j1 -> [success]", jobs[1].Requires)
	}

	wantPath := "/api/v2/workflow/" + workflowID + "/job"
	if got := (*seen)[0]; got.path != wantPath {
		t.Errorf("first request path = %q, want %q", got.path, wantPath)
	}
	if len(*seen) != 2 {
		t.Fatalf("request count = %d, want 2", len(*seen))
	}
}
