// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"net/http"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

// The fixture below mirrors
// the CircleCI API's "200 response with
// pipeline values" case: a flat object mixing string and JSON-number values
// (pipeline.number is a number, not a string), which is exactly what
// GetValues has to render to strings for Terraform's map attributes.
const testPipelineValuesBody = `{
  "pipeline.id": "5034460f-c7c4-4c43-9457-de07e2029e7b",
  "pipeline.number": 42,
  "pipeline.project.git_url": "https://github.com/circleci/example",
  "pipeline.project.type": "github",
  "pipeline.git.tag": "",
  "pipeline.git.branch": "main",
  "pipeline.git.revision": "abc123",
  "pipeline.trigger_source": "webhook",
  "pipeline.schedule.name": "",
  "pipeline.schedule.id": "",
  "pipeline.event.type": "push"
}`

func TestPipelineRunServiceGetValues(t *testing.T) {
	t.Parallel()

	const runID = "5034460f-c7c4-4c43-9457-de07e2029e7b"

	client, seen := newV2Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testPipelineValuesBody))
	})

	values, err := client.PipelineRuns().GetValues(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetValues returned error: %v", err)
	}

	// A JSON number must render without a decimal point or exponent, not through
	// %v (which would give "42" anyway for an int, but the point is the encoded
	// float64(42) must not become "4.2e+01" or "42.0").
	if got := values["pipeline.number"]; got != "42" {
		t.Errorf(`values["pipeline.number"] = %q, want "42"`, got)
	}
	if got := values["pipeline.id"]; got != runID {
		t.Errorf(`values["pipeline.id"] = %q, want %q`, got, runID)
	}
	if got := values["pipeline.git.branch"]; got != "main" {
		t.Errorf(`values["pipeline.git.branch"] = %q, want "main"`, got)
	}
	// An empty string value (pipeline.git.tag when the run used a branch) must
	// survive as an empty string, not be dropped from the map.
	if got, ok := values["pipeline.git.tag"]; !ok || got != "" {
		t.Errorf(`values["pipeline.git.tag"] = %q, ok=%v, want "", true`, got, ok)
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}

	wantPath := "/api/v2/pipeline/" + runID + "/values"
	if req := (*seen)[0]; req.method != http.MethodGet || req.path != wantPath {
		t.Errorf("request = %s %s, want GET %s", req.method, req.path, wantPath)
	}
}

func TestPipelineRunServiceGetValuesEmpty(t *testing.T) {
	t.Parallel()

	client, _ := newV2Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})

	values, err := client.PipelineRuns().GetValues(context.Background(), "5034460f-c7c4-4c43-9457-de07e2029e7b")
	if err != nil {
		t.Fatalf("GetValues returned error: %v", err)
	}
	if values != nil {
		t.Errorf("values = %#v, want nil for an empty response", values)
	}
}

func TestPipelineRunServiceGetValuesNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newV2Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"pipeline not found"}`))
	})

	_, err := client.PipelineRuns().GetValues(context.Background(), "5034460f-c7c4-4c43-9457-de07e2029e7b")
	if !circleci.IsNotFound(err) {
		t.Fatalf("GetValues error = %v, want a not found error", err)
	}
}

func TestPipelineRunServiceGetValuesInvalidID(t *testing.T) {
	t.Parallel()

	// Mirrors handler_get_values_test.go's "400 response for invalid pipeline ID"
	// case: the server validates the id is a UUID before proxying anywhere, so
	// the client must surface that 400 rather than treating it as IsNotFound.
	client, _ := newV2Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"Invalid pipeline ID."}`))
	})

	_, err := client.PipelineRuns().GetValues(context.Background(), "not-a-uuid")
	if err == nil {
		t.Fatal("GetValues returned no error for an invalid pipeline ID")
	}
	if circleci.IsNotFound(err) {
		t.Error("GetValues error satisfies IsNotFound, want a plain 400 (not a not-found)")
	}
	if detail := circleci.Detail(err); detail != "Invalid pipeline ID. (HTTP 400)" {
		t.Errorf("Detail() = %q, want it to carry the server message", detail)
	}
}
