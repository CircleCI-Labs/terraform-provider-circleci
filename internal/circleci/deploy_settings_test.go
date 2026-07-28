// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"net/http"
	"testing"
)

func TestDeploySettingsServiceGet(t *testing.T) {
	t.Parallel()

	const projectID = "1e2d3c4b-5a69-7887-9a0b-1c2d3e4f5061"
	const rollbackID = "9f1c2f6a-1a2b-4c3d-8e9f-0a1b2c3d4e5f"

	client, seen := newDeployServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rollback_pipeline_definition_id": "` + rollbackID + `"}`))
	})

	settings, err := client.DeployProjectSettings().Get(context.Background(), projectID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	if settings.RollbackPipelineDefinitionID == nil || *settings.RollbackPipelineDefinitionID != rollbackID {
		t.Errorf("rollback pipeline definition id = %v, want %q", settings.RollbackPipelineDefinitionID, rollbackID)
	}
	// The API omits deploy_pipeline_definition_id entirely when unset, so it
	// must decode to a nil pointer rather than an empty string.
	if settings.DeployPipelineDefinitionID != nil {
		t.Errorf("deploy pipeline definition id = %q, want nil", *settings.DeployPipelineDefinitionID)
	}

	wantPath := "/api/v2/deploy/projects/" + projectID + "/settings"
	if got := (*seen)[0]; got.method != http.MethodGet || got.path != wantPath {
		t.Errorf("request = %s %s, want GET %s", got.method, got.path, wantPath)
	}
}

func TestDeploySettingsServiceGetEmpty(t *testing.T) {
	t.Parallel()

	// A project with neither pipeline definition configured gets back `{}`.
	client, _ := newDeployServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})

	settings, err := client.DeployProjectSettings().Get(context.Background(), "p1")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if settings.RollbackPipelineDefinitionID != nil || settings.DeployPipelineDefinitionID != nil {
		t.Errorf("settings = %+v, want both fields nil", settings)
	}
}
