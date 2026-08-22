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
// settings — are exercised against an in-process stand-in for the backend
// behind /api/v2/deploy/* rather than a real installation. Every
// one of them is a pure read with no writable counterpart in this provider,
// so there is nothing to create first.
//
// The response bodies are the shapes production sends, verified [NET]
// against real CircleCI organizations for this workstream — including two
// quirks that are easy to assume away when writing a fake from a spec
// instead of a real response: the components list route's always-zero
// release_count (see the components handler below) and the plain
// `{"message":"Not found."}` 404 body, with no permissions wording.

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
	// testDeployOrganizationID is the organization the mock API serves seeded
	// environments and components for.
	testDeployOrganizationID = "b9291e0d-a11e-41fb-8517-c545388b5953"
	// testDeployEmptyOrganizationID is a second, equally real organization that
	// has simply never used deploy/release tracking.
	//
	// [NET] Every one of this provider's four disposable acceptance-test
	// fixture organizations answers exactly this way for both
	// GET /api/v2/deploy/environments and GET /api/v2/deploy/components: HTTP
	// 200 with `{"items":[],"next_page_token":""}`, scoped by a valid,
	// existing org-id that the caller has access to. That is indistinguishable
	// on the wire from "the deploys product was never enabled for this org" —
	// there is no separate signal for that — so this fixture models the one
	// shape this family's four required test fixtures actually exercise.
	// Before this org-id branch existed, the mock ignored org-id entirely and
	// handed back the seeded environment/component for *any* org, so it could
	// not represent this case at all — see TestAccDeployEnvironmentsDataSource_emptyOrg
	// and TestAccDeployComponentsDataSource_emptyOrg.
	//
	// [NET, 2026-08-22] This is no longer just an assumption about every
	// fixture organization looking the same: two organizations now carry real,
	// seeded deploy data (see this file's real-API counterpart's header
	// comment), and every organization that was not seeded — checked directly
	// against the API, not only through this mock's assumptions — still
	// answers exactly this shape. Seeding one organization changed nothing
	// about any other, including the response headers, so "never enabled" and
	// "enabled but unused" remain indistinguishable.
	testDeployEmptyOrganizationID = "5b6a4b0e-3f0f-4d3a-9d5f-2f7e6c8b9a10"
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
	// The seeded environment belongs only to testDeployOrganizationID: every
	// other org-id — in particular testDeployEmptyOrganizationID — gets back
	// no items, matching the real API scoping a list by org-id (confirmed
	// [NET]; see testDeployEmptyOrganizationID's comment). A handler keyed on
	// path alone, ignoring the org-id query parameter, cannot represent an org
	// with no deploy data at all, which is what all four of this provider's
	// real acceptance-test fixtures actually are.
	case r.URL.Path == "/api/v2/deploy/environments" && r.Method == http.MethodGet &&
		r.URL.Query().Get("org-id") != testDeployOrganizationID:
		m.writeJSON(w, http.StatusOK, `{"items":[],"next_page_token":""}`)

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

	// [NET] A deploy environment/component/settings id that does not exist
	// answers 404 with exactly this body, with no mention of permissions.
	case strings.HasPrefix(r.URL.Path, "/api/v2/deploy/environments/") && r.Method == http.MethodGet:
		m.writeJSON(w, http.StatusNotFound, `{"message":"Not found."}`)

	// Same org-id scoping as the environments handler above, and for the same
	// reason: the seeded component belongs only to testDeployOrganizationID.
	case r.URL.Path == "/api/v2/deploy/components" && r.Method == http.MethodGet &&
		r.URL.Query().Get("org-id") != testDeployOrganizationID:
		m.writeJSON(w, http.StatusOK, `{"items":[],"next_page_token":""}`)

	// release_count is 0 here, not the singular Get response's 42, on
	// purpose: [NET] the plural list route has been observed to always
	// answer release_count: 0 for every component, while the singular Get
	// for the very same component id answers the true count, reproducibly.
	// See circleci.DeployComponent's ReleaseCount doc comment.
	case r.URL.Path == "/api/v2/deploy/components" && r.Method == http.MethodGet:
		m.writeJSON(w, http.StatusOK, fmt.Sprintf(`{
			"items": [{
				"id": %[1]q,
				"project_id": %[2]q,
				"name": "release-agent",
				"release_count": 0,
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
		m.writeJSON(w, http.StatusNotFound, `{"message":"Not found."}`)

	case r.URL.Path == "/api/v2/deploy/projects/"+testDeployProjectID+"/settings" && r.Method == http.MethodGet:
		m.writeJSON(w, http.StatusOK, `{"rollback_pipeline_definition_id": "1e2d3c4b-5a69-7887-9a0b-1c2d3e4f5061"}`)

	default:
		m.writeJSON(w, http.StatusNotFound, `{"message":"not found"}`)
	}
}
