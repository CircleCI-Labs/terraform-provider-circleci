// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"net/http"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

func TestJobServiceGet(t *testing.T) {
	t.Parallel()

	client, seen := newV2Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"created_at": "2024-04-24T15:10:00.000Z",
			"duration": 45000,
			"executor": {"resource_class": "medium", "type": "docker"},
			"messages": [],
			"queued_at": "2024-04-24T15:09:00.000Z",
			"started_at": "2024-04-24T15:10:00.000Z",
			"status": "success",
			"stopped_at": "2024-04-24T15:10:45.000Z",
			"parallel_runs": [{"index": 0, "status": "success"}],
			"contexts": [{"name": "org-global"}],
			"latest_workflow": {"id": "5034460f-c7c4-4c43-9457-de07e2029e7b", "name": "build-and-test"},
			"name": "build",
			"number": 578122,
			"organization": {"name": "CircleCI-Public"},
			"parallelism": 1,
			"pipeline": {"id": "1e2d3c4b-5a69-7887-9a0b-1c2d3e4f5061"},
			"project": {"id": "9f1c2f6a-1a2b-4c3d-8e9f-0a1b2c3d4e5f", "slug": "gh/example-org/example-repo", "name": "example-repo", "external_url": "https://github.com/example-org/example-repo"},
			"web_url": "https://app.circleci.com/pipelines/github/example-org/example-repo/jobs/578122"
		}`))
	})

	job, err := client.Jobs().Get(context.Background(), "gh/example-org/example-repo", 578122)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	if job.Status != "success" {
		t.Errorf("job status = %q, want success", job.Status)
	}
	if job.Number != 578122 {
		t.Errorf("job number = %d, want 578122", job.Number)
	}
	if job.Executor == nil || job.Executor.ResourceClass == nil || *job.Executor.ResourceClass != "medium" {
		t.Errorf("job executor = %+v, want resource_class medium", job.Executor)
	}
	if job.Project == nil || job.Project.Slug != "gh/example-org/example-repo" {
		t.Errorf("job project = %+v, want slug gh/example-org/example-repo", job.Project)
	}
	if len(job.ParallelRuns) != 1 || job.ParallelRuns[0].Status != "success" {
		t.Errorf("job parallel runs = %+v, want one success run", job.ParallelRuns)
	}

	wantURI := "/api/v2/project/gh/example-org/example-repo/job/578122"
	if got := (*seen)[0]; got.method != http.MethodGet || got.rawURI != wantURI {
		t.Errorf("request = %s %s, want GET %s", got.method, got.rawURI, wantURI)
	}
}

func TestJobServiceGetNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newV2Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"not found"}`))
	})

	_, err := client.Jobs().Get(context.Background(), "gh/o/r", 1)
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

// TestJobServiceGetEscapesSlug confirms the project slug's separators stay
// literal, matching the same escaping PipelineRunService.GetByNumber relies
// on for CircleCI Server compatibility.
func TestJobServiceGetEscapesSlug(t *testing.T) {
	t.Parallel()

	client, seen := newV2Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status": "success", "messages": []}`))
	})

	if _, err := client.Jobs().Get(context.Background(), "gh/some org/repo", 1); err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	wantURI := "/api/v2/project/gh/some%20org/repo/job/1"
	if got := (*seen)[0]; got.rawURI != wantURI {
		t.Errorf("raw request URI = %q, want %q", got.rawURI, wantURI)
	}
}

func TestJobServiceGetUnscheduledExecutorIsNil(t *testing.T) {
	t.Parallel()

	// A job that has not yet been dispatched has no executor at all.
	client, _ := newV2Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status": "queued", "messages": []}`))
	})

	job, err := client.Jobs().Get(context.Background(), "gh/o/r", 1)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if job.Executor != nil {
		t.Errorf("job executor = %+v, want nil for an undispatched job", job.Executor)
	}
}
