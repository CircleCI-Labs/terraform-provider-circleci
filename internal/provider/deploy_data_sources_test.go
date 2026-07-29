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

// The deploy/release data sources — environments, components and project
// settings — are exercised against an in-process stand-in for
// the API/the API rather than a real installation. Every
// one of them is a pure read with no writable counterpart in this provider,
// so there is nothing to create first.
//
// The response bodies are the shapes the API's BFF handlers send
// (the CircleCI API and the API), fetched through
// the API's /api/v2/deploy/* proxy
// (the CircleCI API the API).

// deployProvider serves only the deploy/release data sources.
//
// The provider's own DataSources list is registered separately, so these
// tests bring their own provider rather than depending on that registration.
type deployProvider struct {
	*CircleCiProvider
}

func (p *deployProvider) Resources(_ context.Context) []func() fwresource.Resource {
	return nil
}

func (p *deployProvider) DataSources(_ context.Context) []func() fwdatasource.DataSource {
	return []func() fwdatasource.DataSource{
		NewDeployEnvironmentsDataSource,
		NewDeployEnvironmentDataSource,
		NewDeployComponentsDataSource,
		NewDeployComponentDataSource,
		NewDeploySettingsDataSource,
	}
}

// deployProviderFactories mirrors testAccProtoV6ProviderFactories for the
// deploy/release data sources.
var deployProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"circleci": providerserver.NewProtocol6WithError(
		&deployProvider{CircleCiProvider: &CircleCiProvider{version: "test"}},
	),
}

// deployProviderConfig renders a provider block pointed at the mock API.
func deployProviderConfig(host, deployment string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host       = %[1]q
  key        = "fake-token"
  deployment = %[2]q
}
`, host, deployment)
}

const (
	// testDeployOrganizationID is the organization the mock API serves.
	testDeployOrganizationID = "b9291e0d-a11e-41fb-8517-c545388b5953"
	// testDeployEnvironmentID is a pre-seeded environment id.
	testDeployEnvironmentID = "9f1c2f6a-1a2b-4c3d-8e9f-0a1b2c3d4e5f"
	// testDeployComponentID is a pre-seeded component id.
	testDeployComponentID = "1e2d3c4b-5a69-7887-9a0b-1c2d3e4f5061"
	// testDeployProjectID is the CircleCI project id deploy settings are keyed by.
	testDeployProjectID = "6f5e4d3c-2b1a-4988-9c8d-7e6f5a4b3c2d"
	// testDeployVersionRunID is the pipeline run recorded against the second seeded
	// component version. The first version records none, so the two together cover
	// both a real id and the all-zero sentinel.
	testDeployVersionRunID = "3c4d5e6f-7a8b-49c0-91d2-e3f4a5b6c7d8"
)

// mockDeployAPI is an in-memory stand-in for the routes the deploy/release
// data sources read.
type mockDeployAPI struct {
	t *testing.T

	mu       sync.Mutex
	requests []string
}

func newMockDeployAPI(t *testing.T) (*mockDeployAPI, string) {
	t.Helper()

	api := &mockDeployAPI{t: t}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	return api, srv.URL
}

func (m *mockDeployAPI) seenRequests() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]string(nil), m.requests...)
}

func (m *mockDeployAPI) writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (m *mockDeployAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.requests = append(m.requests, r.Method+" "+r.RequestURI)
	m.mu.Unlock()

	switch {
	case r.URL.Path == "/api/v2/deploy/environments" && r.Method == http.MethodGet:
		m.writeJSON(w, http.StatusOK, fmt.Sprintf(`{
			"items": [{
				"id": %[1]q,
				"name": "prod-app",
				"created_at": "2024-04-24T15:10:21.123Z",
				"updated_at": "2024-04-24T15:10:21.123Z",
				"labels": [{"key": "env", "value": "prod"}],
				"description": "Production environment"
			}],
			"next_page_token": null
		}`, testDeployEnvironmentID))

	case r.URL.Path == "/api/v2/deploy/environments/"+testDeployEnvironmentID && r.Method == http.MethodGet:
		m.writeJSON(w, http.StatusOK, fmt.Sprintf(`{
			"id": %[1]q,
			"name": "prod-app",
			"created_at": "2024-04-24T15:10:21.123Z",
			"updated_at": "2024-04-24T15:10:21.123Z",
			"labels": [{"key": "env", "value": "prod"}],
			"description": "Production environment"
		}`, testDeployEnvironmentID))

	case strings.HasPrefix(r.URL.Path, "/api/v2/deploy/environments/") && r.Method == http.MethodGet:
		m.writeJSON(w, http.StatusNotFound, `{"message":"Resource not found or permission denied"}`)

	case r.URL.Path == "/api/v2/deploy/components" && r.Method == http.MethodGet:
		m.writeJSON(w, http.StatusOK, fmt.Sprintf(`{
			"items": [{
				"id": %[1]q,
				"project_id": %[2]q,
				"name": "release-agent",
				"release_count": 42,
				"labels": [{"key": "team", "value": "deploy"}],
				"created_at": "2024-04-24T15:10:21.123Z",
				"updated_at": "2024-04-24T15:10:21.123Z"
			}],
			"next_page_token": null
		}`, testDeployComponentID, testDeployProjectID))

	case r.URL.Path == "/api/v2/deploy/components/"+testDeployComponentID+"/versions" && r.Method == http.MethodGet:
		m.writeJSON(w, http.StatusOK, fmt.Sprintf(`{
			"items": [{
				"name": "1.2.3",
				"namespace": "default",
				"environment_id": "9f1c2f6a-1a2b-4c3d-8e9f-0a1b2c3d4e5f",
				"is_live": true,
				"pipeline_id": "00000000-0000-0000-0000-000000000000",
				"workflow_id": "00000000-0000-0000-0000-000000000000",
				"job_id": "00000000-0000-0000-0000-000000000000",
				"last_deployed_at": "2024-04-24T15:10:21.123Z"
			}, {
				"name": "1.2.4",
				"namespace": "default",
				"environment_id": "9f1c2f6a-1a2b-4c3d-8e9f-0a1b2c3d4e5f",
				"is_live": false,
				"pipeline_id": %[1]q,
				"workflow_id": "5e6f7a8b-9c0d-41e2-a3f4-b5c6d7e8f901",
				"job_id": "7a8b9c0d-1e2f-43a4-b5c6-d7e8f9012345",
				"job_number": 17,
				"last_deployed_at": "2024-04-25T15:10:21.123Z"
			}],
			"next_page_token": null
		}`, testDeployVersionRunID))

	case r.URL.Path == "/api/v2/deploy/components/"+testDeployComponentID && r.Method == http.MethodGet:
		m.writeJSON(w, http.StatusOK, fmt.Sprintf(`{
			"id": %[1]q,
			"project_id": %[2]q,
			"name": "release-agent",
			"release_count": 42,
			"labels": [{"key": "team", "value": "deploy"}],
			"created_at": "2024-04-24T15:10:21.123Z",
			"updated_at": "2024-04-24T15:10:21.123Z"
		}`, testDeployComponentID, testDeployProjectID))

	case strings.HasPrefix(r.URL.Path, "/api/v2/deploy/components/") && r.Method == http.MethodGet:
		m.writeJSON(w, http.StatusNotFound, `{"message":"Resource not found or permission denied"}`)

	case r.URL.Path == "/api/v2/deploy/projects/"+testDeployProjectID+"/settings" && r.Method == http.MethodGet:
		m.writeJSON(w, http.StatusOK, `{"rollback_pipeline_definition_id": "1e2d3c4b-5a69-7887-9a0b-1c2d3e4f5061"}`)

	default:
		m.writeJSON(w, http.StatusNotFound, `{"message":"not found"}`)
	}
}
