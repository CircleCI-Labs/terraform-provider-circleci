// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

const testOTelOrg = "b9291e0d-a11e-41fb-8517-c545388b5953"

// otelAPI is an in-memory stand-in for the experimental v2 OTLP exporter
// endpoints.
type otelAPI struct {
	mu        sync.Mutex
	exporters []map[string]any
	requests  []string
	// createBodies holds every create request body, decoded generically. The
	// write-only tests compare them across the `headers` and `headers_wo` paths:
	// the two spellings must reach the API identically, and comparing the whole
	// body means a difference in any key fails rather than only a difference in
	// the one being looked at.
	createBodies []map[string]any
	created      int
	// limit caps the number of exporters, standing in for the API's own limit.
	limit int
}

func newOTelAPI() *otelAPI {
	return &otelAPI{limit: circleci.OTelExporterLimit}
}

// newOTelServer starts a fake API. Every request line is recorded so tests can
// assert the routes, including the org-id query parameter the collection needs.
func newOTelServer(t *testing.T, api *otelAPI) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		api.requests = append(api.requests, r.Method+" "+r.RequestURI)
		api.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		if !strings.HasPrefix(r.URL.Path, "/api/v2/otel/exporters") {
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)

			return
		}

		id := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/v2/otel/exporters"), "/")

		switch {
		case r.Method == http.MethodPost:
			api.handleCreate(t, w, r)
		case r.Method == http.MethodGet && id == "":
			api.handleList(t, w, r)
		case r.Method == http.MethodDelete && id != "":
			api.handleDelete(w, id)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

func (a *otelAPI) handleCreate(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("could not read the create body: %v", err)
	}

	var body struct {
		OrgID    string            `json:"org_id"`
		Endpoint string            `json:"endpoint"`
		Protocol string            `json:"protocol"`
		Insecure bool              `json:"insecure"`
		Headers  map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Errorf("create body is not JSON: %v", err)
	}

	// Recorded generically as well as decoded, so a test can compare two request
	// bodies key for key without the comparison being limited to the fields this
	// struct happens to name.
	generic := map[string]any{}
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Errorf("create body is not a JSON object: %v", err)
	}

	a.mu.Lock()
	a.createBodies = append(a.createBodies, generic)
	a.mu.Unlock()
	if body.OrgID == "" {
		t.Error("create body omitted org_id; the create route has no org-id query parameter")
	}
	if strings.Contains(body.Endpoint, "://") {
		t.Errorf("create body carried a scheme in the endpoint %q", body.Endpoint)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.exporters) >= a.limit {
		// The service answers 422, not 400, when an org is at its exporter limit.
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Org has reached the maximum number of exporters."}`))

		return
	}

	a.created++

	exporter := map[string]any{
		"id":       fmt.Sprintf("00000000-0000-0000-0000-00000000000%d", a.created),
		"org_id":   body.OrgID,
		"endpoint": body.Endpoint,
		"protocol": body.Protocol,
		"insecure": body.Insecure,
	}

	// Header values are encrypted at rest: the API never returns what was sent,
	// only a redaction placeholder.
	if len(body.Headers) > 0 {
		redacted := make(map[string]string, len(body.Headers))
		for name := range body.Headers {
			redacted[name] = circleci.OTelRedactedHeaderValue
		}
		exporter["headers"] = redacted
	}

	a.exporters = append(a.exporters, exporter)

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(exporter)
}

func (a *otelAPI) handleList(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()

	orgID := r.URL.Query().Get("org-id")
	if orgID == "" {
		// The parameter is required; the real API answers 400 without it.
		t.Error("list request carried no org-id query parameter")
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// A bare JSON array, with no items/next_page_token envelope.
	matching := make([]map[string]any, 0, len(a.exporters))
	for _, exporter := range a.exporters {
		if exporter["org_id"] == orgID {
			matching = append(matching, exporter)
		}
	}

	// Sort by endpoint so the list order does not depend on which resource
	// Terraform happened to create first.
	sort.Slice(matching, func(i, j int) bool {
		left, _ := matching[i]["endpoint"].(string)
		right, _ := matching[j]["endpoint"].(string)

		return left < right
	})

	_ = json.NewEncoder(w).Encode(matching)
}

func (a *otelAPI) handleDelete(w http.ResponseWriter, id string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for i, exporter := range a.exporters {
		if exporter["id"] == id {
			a.exporters = append(a.exporters[:i], a.exporters[i+1:]...)
			w.WriteHeader(http.StatusNoContent)

			return
		}
	}

	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"message":"Exporter not found"}`))
}

// recorded returns the request lines seen so far.
func (a *otelAPI) recorded() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]string(nil), a.requests...)
}

// removeAll deletes every exporter, simulating removal outside Terraform.
func (a *otelAPI) removeAll() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.exporters = nil
}

// addHeader adds a header to the first exporter, simulating a change made
// outside Terraform. Only the header names are observable through the API, so
// this is the kind of drift the provider can actually detect.
func (a *otelAPI) addHeader(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.exporters) == 0 {
		return
	}

	headers, ok := a.exporters[0]["headers"].(map[string]string)
	if !ok {
		headers = map[string]string{}
	}
	headers[name] = circleci.OTelRedactedHeaderValue
	a.exporters[0]["headers"] = headers
}

func otelExporterConfig(host, endpoint, protocol string) string {
	return governanceProviderConfig(host) + fmt.Sprintf(`
resource "circleci_otel_exporter" "test" {
  organization_id = %q
  endpoint        = %q
  protocol        = %q
}
`, testOTelOrg, endpoint, protocol)
}

func TestAccOTelExporterResource(t *testing.T) {
	api := newOTelAPI()
	srv := newOTelServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: otelExporterConfig(srv.URL, "otel.example.com:4317", "grpc"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_otel_exporter.test",
						tfjsonpath.New("id"),
						knownvalue.StringExact("00000000-0000-0000-0000-000000000001"),
					),
					statecheck.ExpectKnownValue(
						"circleci_otel_exporter.test",
						tfjsonpath.New("organization_id"),
						knownvalue.StringExact(testOTelOrg),
					),
					statecheck.ExpectKnownValue(
						"circleci_otel_exporter.test",
						tfjsonpath.New("endpoint"),
						knownvalue.StringExact("otel.example.com:4317"),
					),
					statecheck.ExpectKnownValue(
						"circleci_otel_exporter.test",
						tfjsonpath.New("protocol"),
						knownvalue.StringExact("grpc"),
					),
					// insecure defaults to false rather than staying unknown.
					statecheck.ExpectKnownValue(
						"circleci_otel_exporter.test",
						tfjsonpath.New("insecure"),
						knownvalue.Bool(false),
					),
					statecheck.ExpectKnownValue(
						"circleci_otel_exporter.test",
						tfjsonpath.New("headers"),
						knownvalue.Null(),
					),
					statecheck.ExpectKnownValue(
						"circleci_otel_exporter.test",
						tfjsonpath.New("issues"),
						knownvalue.ListExact([]knownvalue.Check{}),
					),
				},
			},
			// Import testing: the ID is organization_id/exporter_id, because
			// reading an exporter means listing its organization's exporters.
			{
				ResourceName:                         "circleci_otel_exporter.test",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "id",
				ImportStateId:                        testOTelOrg + "/00000000-0000-0000-0000-000000000001",
			},
			// Delete testing automatically occurs in TestCase.
		},
	})

	var sawCreate, sawList, sawDelete bool
	for _, req := range api.recorded() {
		switch req {
		case "POST /api/v2/otel/exporters":
			sawCreate = true
		case "GET /api/v2/otel/exporters?org-id=" + testOTelOrg:
			sawList = true
		case "DELETE /api/v2/otel/exporters/00000000-0000-0000-0000-000000000001":
			sawDelete = true
		}
	}

	if !sawCreate {
		t.Errorf("no create request, got %v", api.recorded())
	}
	// A read has to go through the collection: there is no single-exporter route.
	if !sawList {
		t.Errorf("no list request scoped by org-id, got %v", api.recorded())
	}
	if !sawDelete {
		t.Errorf("no delete request, got %v", api.recorded())
	}
}

// TestAccOTelExporterEveryChangeReplaces covers the missing update route: with no
// PATCH available, every configurable attribute has to force replacement.
func TestAccOTelExporterEveryChangeReplaces(t *testing.T) {
	api := newOTelAPI()
	srv := newOTelServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{Config: otelExporterConfig(srv.URL, "otel.example.com:4317", "grpc")},
			{
				// Changing the protocol destroys and recreates, so the id changes.
				Config: otelExporterConfig(srv.URL, "otel.example.com:4318", "http"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_otel_exporter.test",
							plancheck.ResourceActionDestroyBeforeCreate,
						),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_otel_exporter.test",
						tfjsonpath.New("id"),
						knownvalue.StringExact("00000000-0000-0000-0000-000000000002"),
					),
					statecheck.ExpectKnownValue(
						"circleci_otel_exporter.test",
						tfjsonpath.New("protocol"),
						knownvalue.StringExact("http"),
					),
				},
			},
		},
	})
}

// TestAccOTelExporterHeadersAreNotRefreshed is the load-bearing test for the
// redaction problem. Every read answers "xxxx" for a header value, so the
// provider must keep the configured values rather than adopt the placeholder —
// otherwise the secret is lost from state and the next plan forces a needless
// replacement.
func TestAccOTelExporterHeadersAreNotRefreshed(t *testing.T) {
	api := newOTelAPI()
	srv := newOTelServer(t, api)

	config := governanceProviderConfig(srv.URL) + fmt.Sprintf(`
resource "circleci_otel_exporter" "test" {
  organization_id = %q
  endpoint        = "otel.example.com:4317"
  protocol        = "grpc"
  insecure        = true
  headers = {
    "x-api-key" = "super-secret"
  }
}
`, testOTelOrg)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_otel_exporter.test",
						tfjsonpath.New("headers").AtMapKey("x-api-key"),
						knownvalue.StringExact("super-secret"),
					),
					statecheck.ExpectKnownValue(
						"circleci_otel_exporter.test",
						tfjsonpath.New("insecure"),
						knownvalue.Bool(true),
					),
				},
			},
			{
				// A refresh must leave the configured value alone: an empty plan
				// afterwards is what proves the placeholder was not adopted.
				RefreshState: true,
			},
			{
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}

// TestAccOTelExporterHeaderAddedOutsideTerraform covers the drift the provider
// can see: header names are returned in full, so a header added elsewhere shows
// up even though its value never does.
func TestAccOTelExporterHeaderAddedOutsideTerraform(t *testing.T) {
	api := newOTelAPI()
	srv := newOTelServer(t, api)

	config := governanceProviderConfig(srv.URL) + fmt.Sprintf(`
resource "circleci_otel_exporter" "test" {
  organization_id = %q
  endpoint        = "otel.example.com:4317"
  protocol        = "grpc"
  headers = {
    "x-api-key" = "super-secret"
  }
}
`, testOTelOrg)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig:          func() { api.addHeader("x-tenant") },
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccOTelExporterDeletedOutsideTerraform covers drift detection through the
// list: an exporter absent from its organization's exporters is gone, and the
// client reports that as ErrNotFound rather than a 404.
func TestAccOTelExporterDeletedOutsideTerraform(t *testing.T) {
	api := newOTelAPI()
	srv := newOTelServer(t, api)

	config := otelExporterConfig(srv.URL, "otel.example.com:4317", "grpc")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig:          func() { api.removeAll() },
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestOTelExporterRejectsEndpointWithScheme(t *testing.T) {
	api := newOTelAPI()
	srv := newOTelServer(t, api)

	for _, endpoint := range []string{"https://otel.example.com:4317", "grpc://otel.example.com", "otel.example.com/v1/traces"} {
		t.Run(endpoint, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: governanceProviderFactories,
				Steps: []resource.TestStep{
					{
						Config:      otelExporterConfig(srv.URL, endpoint, "grpc"),
						ExpectError: regexp.MustCompile(`(?s)must be a bare host and port`),
					},
				},
			})
		})
	}
}

func TestOTelExporterRejectsUnknownProtocol(t *testing.T) {
	api := newOTelAPI()
	srv := newOTelServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      otelExporterConfig(srv.URL, "otel.example.com:4317", "thrift"),
				ExpectError: regexp.MustCompile(`(?s)Attribute protocol value must be one of`),
			},
		},
	})
}

// TestOTelExporterLimitErrorIsExplained covers the per-organization limit: the
// API rejects a sixth exporter, and the diagnostic has to say why.
func TestOTelExporterLimitErrorIsExplained(t *testing.T) {
	api := newOTelAPI()
	api.limit = 0
	srv := newOTelServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      otelExporterConfig(srv.URL, "otel.example.com:4317", "grpc"),
				ExpectError: regexp.MustCompile(`(?s)at most 5 exporters`),
			},
		},
	})
}

func TestOTelExporterImportIDValidation(t *testing.T) {
	api := newOTelAPI()
	srv := newOTelServer(t, api)

	config := otelExporterConfig(srv.URL, "otel.example.com:4317", "grpc")

	for _, importID := range []string{"just-an-id", "", "org/id/extra"} {
		t.Run(importID, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: governanceProviderFactories,
				Steps: []resource.TestStep{
					{Config: config},
					{
						ResourceName:  "circleci_otel_exporter.test",
						ImportState:   true,
						ImportStateId: importID,
						ExpectError:   regexp.MustCompile(`Invalid Import ID Format`),
					},
				},
			})
		})
	}
}
