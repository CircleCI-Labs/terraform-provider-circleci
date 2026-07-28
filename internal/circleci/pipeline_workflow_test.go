// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

func TestWorkflowServiceListByPipeline(t *testing.T) {
	t.Parallel()

	const pipelineID = "1e2d3c4b-5a69-7887-9a0b-1c2d3e4f5061"

	client, seen := newV2Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// The v2 list envelope. Two workflows in different states, because a
		// pipeline's workflows are frequently mid-flight when read.
		_, _ = w.Write([]byte(`{
			"items": [
				{
					"id": "5034460f-c7c4-4c43-9457-de07e2029e7b",
					"name": "build-and-test",
					"status": "success",
					"created_at": "2024-04-24T15:10:21.123Z",
					"stopped_at": "2024-04-24T15:14:02.456Z",
					"pipeline_id": "` + pipelineID + `",
					"pipeline_number": 25,
					"project_slug": "gh/acme/repo",
					"started_by": "9f1c2f6a-1a2b-4c3d-8e9f-0a1b2c3d4e5f"
				},
				{
					"id": "6145571a-d8d5-5d54-a568-ef18f3130f8c",
					"name": "deploy",
					"status": "running",
					"created_at": "2024-04-24T15:14:03.000Z",
					"stopped_at": null,
					"pipeline_id": "` + pipelineID + `",
					"pipeline_number": 25,
					"project_slug": "gh/acme/repo",
					"started_by": "9f1c2f6a-1a2b-4c3d-8e9f-0a1b2c3d4e5f"
				}
			],
			"next_page_token": null
		}`))
	})

	workflows, err := client.Workflows().ListByPipeline(context.Background(), pipelineID)
	if err != nil {
		t.Fatalf("ListByPipeline returned error: %v", err)
	}
	if len(workflows) != 2 {
		t.Fatalf("got %d workflows, want 2", len(workflows))
	}
	if workflows[0].Name != "build-and-test" || workflows[1].Name != "deploy" {
		t.Errorf("workflow names = %q, %q; want build-and-test, deploy", workflows[0].Name, workflows[1].Name)
	}
	// A running workflow has no stopped_at, and the API sends JSON null rather than
	// omitting the key. Decoding that as anything but empty would put the string
	// "null" into Terraform state.
	if workflows[1].StoppedAt != "" {
		t.Errorf("running workflow stopped_at = %q, want empty", workflows[1].StoppedAt)
	}

	wantPath := "/api/v2/pipeline/" + pipelineID + "/workflow"
	if got := (*seen)[0]; got.method != http.MethodGet || got.path != wantPath {
		t.Errorf("request = %s %s, want GET %s", got.method, got.path, wantPath)
	}
}

// TestWorkflowServiceListByPipelineDrainsPages covers the pagination drain. The
// route carries next_page_token, so a pipeline with more workflows than one page
// would otherwise be silently truncated.
func TestWorkflowServiceListByPipelineDrainsPages(t *testing.T) {
	t.Parallel()

	const pipelineID = "1e2d3c4b-5a69-7887-9a0b-1c2d3e4f5061"

	client, seen := newV2Server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Query().Get("page-token") == "" {
			_, _ = w.Write([]byte(`{
				"items": [{"id": "w1", "name": "first", "pipeline_id": "` + pipelineID + `"}],
				"next_page_token": "page-2"
			}`))

			return
		}

		_, _ = w.Write([]byte(`{
			"items": [{"id": "w2", "name": "second", "pipeline_id": "` + pipelineID + `"}],
			"next_page_token": null
		}`))
	})

	workflows, err := client.Workflows().ListByPipeline(context.Background(), pipelineID)
	if err != nil {
		t.Fatalf("ListByPipeline returned error: %v", err)
	}
	if len(workflows) != 2 {
		t.Fatalf("got %d workflows, want 2 across two pages", len(workflows))
	}
	if workflows[0].ID != "w1" || workflows[1].ID != "w2" {
		t.Errorf("workflow ids = %q, %q; want w1, w2 in page order", workflows[0].ID, workflows[1].ID)
	}

	if len(*seen) != 2 {
		t.Fatalf("made %d requests, want 2", len(*seen))
	}
	if !strings.Contains((*seen)[1].rawURI, "page-token=page-2") {
		t.Errorf("second request URI = %q, want it to carry page-token=page-2", (*seen)[1].rawURI)
	}
}

func TestWorkflowServiceListByPipelineNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newV2Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Pipeline not found"}`))
	})

	_, err := client.Workflows().ListByPipeline(context.Background(), "missing")
	if !circleci.IsNotFound(err) {
		t.Errorf("ListByPipeline error = %v, want one satisfying IsNotFound", err)
	}
}
