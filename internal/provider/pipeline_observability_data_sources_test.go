// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// The pipeline observability data sources — pipeline runs, pipeline
// configuration, workflows, workflow jobs and jobs — are exercised against an
// in-process stand-in for the CircleCI v2 API (pipeline runs, pipeline
// config, workflows, workflow jobs) and a separate backend that serves jobs,
// rather than a real installation.
//
// The response bodies are the shapes production sends.

// observabilityProvider serves only the pipeline observability data sources.
//
// The provider's own DataSources list is registered separately, so these
// tests bring their own provider rather than depending on that registration.
type observabilityProvider struct {
	*CircleCiProvider
}

func (p *observabilityProvider) Resources(_ context.Context) []func() fwresource.Resource {
	return nil
}

func (p *observabilityProvider) DataSources(_ context.Context) []func() fwdatasource.DataSource {
	return []func() fwdatasource.DataSource{
		NewPipelineRunDataSource,
		NewPipelineRunConfigDataSource,
		NewWorkflowDataSource,
		NewWorkflowJobsDataSource,
		NewJobDataSource,
	}
}

// observabilityProviderFactories mirrors testAccProtoV6ProviderFactories for
// the pipeline observability data sources.
var observabilityProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"circleci": providerserver.NewProtocol6WithError(
		&observabilityProvider{CircleCiProvider: &CircleCiProvider{version: "test"}},
	),
}

// observabilityProviderConfig renders a provider block pointed at the mock API.
func observabilityProviderConfig(host, deployment string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host       = %[1]q
  key        = "fake-token"
  deployment = %[2]q
}
`, host, deployment)
}

const (
	// testRunID is a pre-seeded pipeline run id.
	testRunID = "5034460f-c7c4-4c43-9457-de07e2029e7b"
	// testRunProjectSlug is the project slug the pre-seeded run belongs to.
	testRunProjectSlug = "gh/CircleCI-Public/api-preview-docs"
	// testRunNumber is the pre-seeded run's number.
	testRunNumber = 25
	// testWorkflowID is a pre-seeded workflow id.
	testWorkflowID = "1e2d3c4b-5a69-7887-9a0b-1c2d3e4f5061"
	// testJobNumber is a pre-seeded job's number, within testRunProjectSlug.
	testJobNumber = 578122
)

// mockObservabilityAPI is an in-memory stand-in for the routes the pipeline
// observability data sources read.
type mockObservabilityAPI struct {
	t *testing.T

	mu       sync.Mutex
	requests []string
}

func newMockObservabilityAPI(t *testing.T) (*mockObservabilityAPI, string) {
	t.Helper()

	api := &mockObservabilityAPI{t: t}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	return api, srv.URL
}

func (m *mockObservabilityAPI) seenRequests() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]string(nil), m.requests...)
}

func (m *mockObservabilityAPI) writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

const testRunBody = `{
	"id": "5034460f-c7c4-4c43-9457-de07e2029e7b",
	"number": 25,
	"project_slug": "gh/CircleCI-Public/api-preview-docs",
	"created_at": "2024-04-24T15:10:21.123Z",
	"errors": [],
	"warnings": [{"type": "config-deprecated-syntax", "message": "old syntax"}],
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
}`

func (m *mockObservabilityAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.requests = append(m.requests, r.Method+" "+r.RequestURI)
	m.mu.Unlock()

	switch {
	case r.URL.Path == "/api/v2/pipeline/"+testRunID:
		m.writeJSON(w, http.StatusOK, testRunBody)

	case r.URL.Path == "/api/v2/project/"+testRunProjectSlug+fmt.Sprintf("/pipeline/%d", testRunNumber):
		m.writeJSON(w, http.StatusOK, testRunBody)

	case r.URL.Path == "/api/v2/pipeline/"+testRunID+"/config":
		m.writeJSON(w, http.StatusOK, `{
			"source": "callithumpian",
			"compiled": "adscititious",
			"setup_config": "mo' repos",
			"compiled_setup_config": "monorepos"
		}`)

	case r.URL.Path == "/api/v2/workflow/"+testWorkflowID+"/job":
		m.writeJSON(w, http.StatusOK, `{
			"items": [{
				"id": "j1",
				"name": "build",
				"type": "build",
				"status": "success",
				"started_at": "2024-04-24T15:10:21.123Z",
				"dependencies": [],
				"project_slug": "gh/CircleCI-Public/api-preview-docs",
				"job_number": 578122
			}],
			"next_page_token": null
		}`)

	case r.URL.Path == "/api/v2/workflow/"+testWorkflowID:
		m.writeJSON(w, http.StatusOK, fmt.Sprintf(`{
			"id": %[1]q,
			"name": "build-and-test",
			"status": "running",
			"created_at": "2024-04-24T15:10:21.123Z",
			"pipeline_id": %[2]q,
			"pipeline_number": %[3]d,
			"project_slug": %[4]q,
			"started_by": "9f1c2f6a-1a2b-4c3d-8e9f-0a1b2c3d4e5f"
		}`, testWorkflowID, testRunID, testRunNumber, testRunProjectSlug))

	case r.URL.Path == "/api/v2/project/"+testRunProjectSlug+fmt.Sprintf("/job/%d", testJobNumber):
		m.writeJSON(w, http.StatusOK, `{
			"status": "success",
			"messages": [],
			"executor": {"resource_class": "medium", "type": "docker"},
			"name": "build",
			"number": 578122,
			"parallelism": 1,
			"organization": {"name": "CircleCI-Public"},
			"project": {"id": "9f1c2f6a-1a2b-4c3d-8e9f-0a1b2c3d4e5f", "slug": "gh/CircleCI-Public/api-preview-docs", "name": "api-preview-docs", "external_url": "https://github.com/CircleCI-Public/api-preview-docs"}
		}`)

	case strings.HasPrefix(r.URL.Path, "/api/v2/pipeline/") || strings.HasPrefix(r.URL.Path, "/api/v2/workflow/"):
		m.writeJSON(w, http.StatusNotFound, `{"message":"not found"}`)

	default:
		m.writeJSON(w, http.StatusNotFound, `{"message":"not found"}`)
	}
}
