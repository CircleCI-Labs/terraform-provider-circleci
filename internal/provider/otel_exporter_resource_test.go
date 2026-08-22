// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strconv"
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
	// listStatus, when non-zero, makes the list route answer with that status
	// instead of the collection. It stands in for the 404 "Org not found" the real
	// route gives both for a missing organization and for a token that cannot
	// manage one, which is a very different thing from an exporter being absent.
	listStatus int
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

// otelEndpointRejection reports the message the API answers 400 with for an
// endpoint and protocol combination it refuses, or "" when it accepts them.
//
// Matched to what the API actually accepts: it parses the endpoint as a URL
// whenever the scheme is http or https, and as a host:port pair otherwise:
//
//   - an http or https URL is valid, with or without a port and a path, but only
//     with protocol "http";
//   - any other value must split into a host AND a port, so a bare hostname is
//     malformed;
//   - the port must be 1-65535.
//
// DNS resolution and the private-address check are deliberately not modelled: they
// depend on the network, and nothing in the provider can anticipate them.
func otelEndpointRejection(endpoint, protocol string) string {
	const (
		malformed = "endpoint must be in the form 'hostname:port' or " +
			"'https://host:port/path' for HTTP endpoints"
		badPort      = "endpoint port must be a number between 1 and 65535"
		wrongForGRPC = "protocol must be 'http' when endpoint starts with http:// or https://"
	)

	if parsed, err := url.Parse(endpoint); err == nil &&
		(parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" {
		if protocol == circleci.OTelProtocolGRPC {
			return wrongForGRPC
		}

		if port := parsed.Port(); port != "" && !validOTelPort(port) {
			return badPort
		}

		return ""
	}

	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || host == "" || port == "" {
		return malformed
	}

	if !validOTelPort(port) {
		return badPort
	}

	return ""
}

func validOTelPort(port string) bool {
	number, err := strconv.Atoi(port)

	return err == nil && number >= 1 && number <= 65535
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

	// The endpoint rules, as the service that validates the request enforces them
	// rather than as the published schema describes them. This fake used to fail the
	// test outright on any endpoint containing "://", which is the client's old
	// assumption restated — a fake agreeing with the client is exactly how a wrong
	// assumption survives a green suite.
	if msg := otelEndpointRejection(body.Endpoint, body.Protocol); msg != "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":` + strconv.Quote(msg) + `}`))

		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.exporters) >= a.limit {
		// The service answers 422, not 400, when an org is at its exporter
		// limit — but [NET], against a real organization already at the limit,
		// its message is the unhelpful generic "Internal server error.", not a
		// message that names the limit. This fake used to invent a friendlier
		// one ("Org has reached the maximum number of exporters."); that was a
		// belief nobody had checked, and the real answer is worse than it
		// assumed. The provider's own diagnostic (see otelExporterResource.Create)
		// supplies the explanation instead of relying on the API's text, which is
		// exactly why that gap did not surface as a test failure.
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Internal server error."}`))

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

// setListStatus makes the list route fail with status, or restores it when status
// is zero.
func (a *otelAPI) setListStatus(status int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.listStatus = status
}

func (a *otelAPI) handleList(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()

	a.mu.Lock()
	status := a.listStatus
	a.mu.Unlock()

	if status != 0 {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"message":"Org not found"}`))

		return
	}

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

// TestAccOTelExporterImportWithHeadersForcesReplacement proves, against a real
// plan, the consequence the resource documentation already claims in "Headers
// cannot be read back": importing an exporter that has headers and then
// supplying the real values in configuration (the only way to have a usable
// exporter) plans a replacement on the very next apply, not an empty plan.
//
// The exporter is seeded directly into the fake, bypassing Terraform Create
// entirely, to stand in for one that already exists in the organization.
// ImportStatePersist is required to observe this: a bare ImportState step
// imports into a throwaway working directory and discards it, so a Config step
// afterwards would plan against whatever state the *previous* step left
// behind, not against the imported state.
func TestAccOTelExporterImportWithHeadersForcesReplacement(t *testing.T) {
	api := newOTelAPI()
	srv := newOTelServer(t, api)

	api.created = 1
	api.exporters = append(api.exporters, map[string]any{
		"id":       "00000000-0000-0000-0000-000000000001",
		"org_id":   testOTelOrg,
		"endpoint": "otel.example.com:4317",
		"protocol": "grpc",
		"insecure": false,
		"headers":  map[string]string{"api-key": circleci.OTelRedactedHeaderValue},
	})

	config := governanceProviderConfig(srv.URL) + fmt.Sprintf(`
resource "circleci_otel_exporter" "test" {
  organization_id = %q
  endpoint        = "otel.example.com:4317"
  protocol        = "grpc"
  headers = {
    "api-key" = "s3cret"
  }
}
`, testOTelOrg)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "circleci_otel_exporter.test",
				ImportState:        true,
				ImportStateId:      testOTelOrg + "/00000000-0000-0000-0000-000000000001",
				ImportStatePersist: true,
				Config:             config,
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_otel_exporter.test", plancheck.ResourceActionReplace,
						),
					},
				},
			},
		},
	})
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
//
// The assertion is a PreApply plan check on a normal apply step, not
// ExpectNonEmptyPlan on a bare RefreshState step. The latter was this test's
// original shape, and it turned out to pass unconditionally — with or without
// api.addHeader actually called — because a bare RefreshState step has no
// config to plan against, so "non-empty" there asserted nothing. A normal step
// refreshes before planning on its own, and its ConfigPlanChecks.PreApply can
// inspect what that refresh produced.
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
				PreConfig: func() { api.addHeader("x-tenant") },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_otel_exporter.test",
							plancheck.ResourceActionDestroyBeforeCreate,
						),
					},
				},
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
				// Read reports a missing exporter as ErrNotFound and that drops the
				// resource from state (see otelExporterResource.Read), so the plan
				// that follows recreates it rather than reporting no changes.
				PreConfig: func() { api.removeAll() },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_otel_exporter.test",
							plancheck.ResourceActionCreate,
						),
					},
				},
			},
		},
	})
}

// TestOTelExporterAcceptsAURLEndpoint is the regression test for a plan-time
// validator that was stricter than the API.
//
// `endpoint` used to be matched against `^[A-Za-z0-9._\-\[\]:]+$`, on the strength
// of the published schema's "Don't include https:// or grpc://". The service that
// validates the request accepts an http or https URL as well, and has a dedicated
// error for pairing one with grpc — a rule that could not exist if the form were
// invalid. So this configuration was refused during `terraform plan` even though
// the API would have taken it.
//
// The assertion is on what the fake received, not only on state: the URL has to
// reach the API byte for byte, path included.
func TestOTelExporterAcceptsAURLEndpoint(t *testing.T) {
	const endpoint = "https://otel.example.com:4318/v1/traces"

	api := newOTelAPI()
	srv := newOTelServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: otelExporterConfig(srv.URL, endpoint, "http"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_otel_exporter.test",
						tfjsonpath.New("endpoint"),
						knownvalue.StringExact(endpoint),
					),
				},
			},
		},
	})

	api.mu.Lock()
	defer api.mu.Unlock()

	if len(api.createBodies) != 1 {
		t.Fatalf("create bodies = %d, want 1", len(api.createBodies))
	}
	if got := api.createBodies[0]["endpoint"]; got != endpoint {
		t.Errorf("endpoint sent = %v, want %q", got, endpoint)
	}
}

// TestOTelExporterRejectsInvalidEndpoints covers the forms the API really does
// refuse, each with the plan-time check that spares the practitioner an HTTP 400
// mid-apply:
//
//   - a scheme other than http or https matches neither branch of the service's
//     validation, so it is malformed;
//   - the bare form needs a port, because that branch is net.SplitHostPort;
//   - a path is only meaningful in the URL form;
//   - and a URL endpoint with protocol = "grpc" is refused by name.
func TestOTelExporterRejectsInvalidEndpoints(t *testing.T) {
	api := newOTelAPI()
	srv := newOTelServer(t, api)

	tests := []struct {
		endpoint string
		protocol string
		want     *regexp.Regexp
	}{
		{"grpc://otel.example.com:4317", "grpc", regexp.MustCompile(`(?s)must be either a bare host and port`)},
		{"otel.example.com", "grpc", regexp.MustCompile(`(?s)must be either a bare host and port`)},
		{"otel.example.com/v1/traces", "http", regexp.MustCompile(`(?s)must be either a bare host and port`)},
		{"https://otel.example.com/v1/traces", "grpc", regexp.MustCompile(`(?s)Invalid protocol for a URL endpoint`)},
	}

	for _, tc := range tests {
		t.Run(tc.endpoint+" "+tc.protocol, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: governanceProviderFactories,
				Steps: []resource.TestStep{
					{
						Config:      otelExporterConfig(srv.URL, tc.endpoint, tc.protocol),
						ExpectError: tc.want,
					},
				},
			})
		})
	}

	// Nothing may have reached the API: every rejection above is a plan-time one.
	api.mu.Lock()
	defer api.mu.Unlock()

	if len(api.createBodies) != 0 {
		t.Errorf("%d create requests were sent; all four cases must fail before apply",
			len(api.createBodies))
	}
}

// TestOTelExporterFakeMatchesTheServiceOnEndpoints guards the guard.
//
// The fake's endpoint rules match what the API accepts, and the provider's
// validator is derived from the same understanding. If the two ever disagree
// the suite should say which — so this asserts the fake's own verdict on the cases
// the provider tests above rely on, independently of Terraform.
func TestOTelExporterFakeMatchesTheServiceOnEndpoints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		endpoint   string
		protocol   string
		wantReject bool
	}{
		{"otel.example.com:4317", circleci.OTelProtocolGRPC, false},
		{"[2001:db8::1]:4317", circleci.OTelProtocolGRPC, false},
		{"https://otel.example.com/v1/traces", circleci.OTelProtocolHTTP, false},
		{"http://otel.example.com:4318", circleci.OTelProtocolHTTP, false},
		{"https://otel.example.com/v1/traces", circleci.OTelProtocolGRPC, true},
		{"otel.example.com", circleci.OTelProtocolGRPC, true},
		{"grpc://otel.example.com:4317", circleci.OTelProtocolGRPC, true},
		{"otel.example.com:0", circleci.OTelProtocolGRPC, true},
		{"otel.example.com:99999", circleci.OTelProtocolGRPC, true},
	}

	for _, tc := range tests {
		got := otelEndpointRejection(tc.endpoint, tc.protocol)
		if (got != "") != tc.wantReject {
			t.Errorf("otelEndpointRejection(%q, %q) = %q, want rejected = %t",
				tc.endpoint, tc.protocol, got, tc.wantReject)
		}
	}
}

// TestAccOTelExporterKeepsStateWhenTheOrganizationAnswers404 is the drift-detection
// counterpart to TestAccOTelExporterDeletedOutsideTerraform.
//
// An exporter is read by listing its organization's exporters, and that list route
// answers 404 "Org not found" both for an organization that does not exist and for
// a token that cannot manage one that does. circleci.IsNotFound says yes to that
// 404, so treating it like an absent exporter would drop a live exporter from state
// and have the next apply create a duplicate — against a limit of five per
// organization, so the damage compounds. Read must report it and leave state alone.
func TestAccOTelExporterKeepsStateWhenTheOrganizationAnswers404(t *testing.T) {
	api := newOTelAPI()
	srv := newOTelServer(t, api)

	config := otelExporterConfig(srv.URL, "otel.example.com:4317", "grpc")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig:    func() { api.setListStatus(http.StatusNotFound) },
				RefreshState: true,
				// The diagnostic is line-wrapped by Terraform, so match a short phrase
				// rather than a sentence: this is the branch that reports the 404
				// instead of removing the resource.
				ExpectError: regexp.MustCompile(`(?s)404 for organization`),
			},
			// Undo the failure so the framework's own destroy step can run.
			{
				PreConfig: func() { api.setListStatus(0) },
				Config:    config,
			},
		},
	})
}

// TestAccOTelExporterRequiresCloud covers the deployment gate.
//
// The routes are /api/v2, which is not sufficient: CircleCI's public API service
// forwards /api/v2/otel to a backend a CircleCI Server installation does not
// deploy, so a Server installation has no such route at all. Without the gate
// `terraform plan` would succeed and the create would 404 mid-apply.
func TestAccOTelExporterRequiresCloud(t *testing.T) {
	wantError := regexp.MustCompile(`(?s)requires CircleCI Cloud.*"server".*circleci\.example\.com`)

	tests := []struct {
		name   string
		config string
	}{
		{
			name: "circleci_otel_exporter resource",
			config: `
resource "circleci_otel_exporter" "test" {
  organization_id = "b9291e0d-a11e-41fb-8517-c545388b5953"
  endpoint        = "otel.example.com:4317"
  protocol        = "grpc"
}
`,
		},
		{
			name: "circleci_otel_exporters data source",
			config: `
data "circleci_otel_exporters" "test" {
  organization_id = "b9291e0d-a11e-41fb-8517-c545388b5953"
}
`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: governanceProviderFactories,
				Steps: []resource.TestStep{
					{
						Config:      cloudOnlyServerProvider + tc.config,
						ExpectError: wantError,
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
