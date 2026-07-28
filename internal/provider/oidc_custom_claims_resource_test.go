// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// governanceProvider serves only the governance resources and data sources.
//
// The provider's own Resources/DataSources lists are registered separately, so
// these tests bring their own provider to avoid depending on that registration.
type governanceProvider struct {
	*CircleCiProvider
}

func (p *governanceProvider) Resources(_ context.Context) []func() fwresource.Resource {
	return []func() fwresource.Resource{
		NewOIDCCustomClaimsResource,
		NewConfigPolicyBundleResource,
		NewConfigPolicySettingsResource,
		NewOTelExporterResource,
	}
}

func (p *governanceProvider) DataSources(_ context.Context) []func() fwdatasource.DataSource {
	return []func() fwdatasource.DataSource{NewOTelExportersDataSource}
}

// governanceProviderFactories mirrors testAccProtoV6ProviderFactories for the
// governance types.
var governanceProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"circleci": providerserver.NewProtocol6WithError(
		&governanceProvider{CircleCiProvider: &CircleCiProvider{version: "test"}},
	),
}

func governanceProviderConfig(host string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
`, host)
}

const (
	testOIDCOrgID     = "00000000-0000-0000-0000-000000000000"
	testOIDCProjectID = "11111111-1111-1111-1111-111111111111"
)

// oidcClaimsAPI is an in-memory stand-in for the v2 OIDC custom claims
// endpoints. Keys are the scope path, so org-level and project-level claims are
// stored independently, as the API stores them.
type oidcClaimsAPI struct {
	mu       sync.Mutex
	scopes   map[string]*oidcScopeState
	requests []string
}

type oidcScopeState struct {
	audience          *[]string
	ttl               *string
	audienceUpdatedAt string
	ttlUpdatedAt      string
}

func newOIDCClaimsAPI() *oidcClaimsAPI {
	return &oidcClaimsAPI{scopes: map[string]*oidcScopeState{}}
}

// newOIDCClaimsServer starts a fake API. Every request line is recorded so tests
// can assert the routes and the required claims query parameter.
func newOIDCClaimsServer(t *testing.T, api *oidcClaimsAPI) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		api.requests = append(api.requests, r.Method+" "+r.RequestURI)
		api.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		if !strings.HasSuffix(r.URL.Path, "/oidc-custom-claims") {
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)

			return
		}

		switch r.Method {
		case http.MethodGet:
			api.write(w, r.URL.Path)
		case http.MethodPatch:
			api.handlePatch(t, w, r)
		case http.MethodDelete:
			api.handleDelete(t, w, r)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

func (a *oidcClaimsAPI) scope(path string) *oidcScopeState {
	state, ok := a.scopes[path]
	if !ok {
		state = &oidcScopeState{}
		a.scopes[path] = state
	}

	return state
}

func (a *oidcClaimsAPI) handlePatch(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()

	var body struct {
		Audience *[]string `json:"audience"`
		TTL      *string   `json:"ttl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("patch body is not JSON: %v", err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	state := a.scope(r.URL.Path)

	// A partial update: an omitted field leaves the claim alone.
	if body.Audience != nil {
		state.audience = body.Audience
		state.audienceUpdatedAt = "2024-01-02T03:04:05Z"
	}
	if body.TTL != nil {
		// The API normalizes ttl into Go's duration format before storing it, so
		// "3h" comes back as "3h0m0s" and "90m" as "1h30m0s". Echoing the request
		// verbatim hid an inconsistent-result-after-apply failure.
		state.ttl = oidcNormalizeTTL(body.TTL)
		state.ttlUpdatedAt = "2024-01-02T03:04:06.123456789Z"
	}

	a.write(w, r.URL.Path)
}

func (a *oidcClaimsAPI) handleDelete(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()

	claims := r.URL.Query().Get("claims")
	if claims == "" {
		// The parameter is required; the real API answers 400 without it.
		t.Error("delete request carried no claims query parameter")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"claims: cannot be blank."}`))

		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	state := a.scope(r.URL.Path)

	for _, claim := range strings.Split(claims, ",") {
		switch claim {
		case "audience":
			state.audience = nil
			state.audienceUpdatedAt = ""
		case "ttl":
			state.ttl = nil
			state.ttlUpdatedAt = ""
		default:
			t.Errorf("delete request asked for unknown claim %q", claim)
		}
	}

	a.write(w, r.URL.Path)
}

// write renders the ClaimResponse for a scope. Note that a scope with nothing
// set still answers 200: there is no 404 for an uncustomized scope.
func (a *oidcClaimsAPI) write(w http.ResponseWriter, path string) {
	state, ok := a.scopes[path]
	if !ok {
		state = &oidcScopeState{}
	}

	payload := map[string]any{"org_id": testOIDCOrgID}
	if strings.Contains(path, "/project/") {
		payload["project_id"] = testOIDCProjectID
	}
	if state.audience != nil {
		payload["audience"] = *state.audience
		payload["audience_updated_at"] = state.audienceUpdatedAt
	}
	if state.ttl != nil {
		payload["ttl"] = *state.ttl
		payload["ttl_updated_at"] = state.ttlUpdatedAt
	}

	_ = json.NewEncoder(w).Encode(payload)
}

// recorded returns the request lines seen so far.
func (a *oidcClaimsAPI) recorded() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]string(nil), a.requests...)
}

// reset clears a scope's claims, simulating a reset outside Terraform.
func (a *oidcClaimsAPI) reset(path string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.scopes[path] = &oidcScopeState{}
}

func TestAccOIDCCustomClaimsResourceOrgScope(t *testing.T) {
	api := newOIDCClaimsAPI()
	srv := newOIDCClaimsServer(t, api)

	config := func(audience, ttl string) string {
		return governanceProviderConfig(srv.URL) + fmt.Sprintf(`
resource "circleci_oidc_custom_claims" "test" {
  organization_id = %q
  audience        = [%q]
  ttl             = %q
}
`, testOIDCOrgID, audience, ttl)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("https://sts.amazonaws.com", "1h"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_oidc_custom_claims.test",
						tfjsonpath.New("organization_id"),
						knownvalue.StringExact(testOIDCOrgID),
					),
					statecheck.ExpectKnownValue(
						"circleci_oidc_custom_claims.test",
						tfjsonpath.New("audience"),
						knownvalue.ListExact([]knownvalue.Check{
							knownvalue.StringExact("https://sts.amazonaws.com"),
						}),
					),
					statecheck.ExpectKnownValue(
						"circleci_oidc_custom_claims.test",
						tfjsonpath.New("ttl"),
						knownvalue.StringExact("1h"),
					),
					statecheck.ExpectKnownValue(
						"circleci_oidc_custom_claims.test",
						tfjsonpath.New("audience_updated_at"),
						knownvalue.StringExact("2024-01-02T03:04:05Z"),
					),
					statecheck.ExpectKnownValue(
						"circleci_oidc_custom_claims.test",
						tfjsonpath.New("project_id"),
						knownvalue.Null(),
					),
				},
			},
			// Update in place: the claims change but the scope does not.
			{
				Config: config("my-audience", "30m"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_oidc_custom_claims.test",
						tfjsonpath.New("audience"),
						knownvalue.ListExact([]knownvalue.Check{
							knownvalue.StringExact("my-audience"),
						}),
					),
					statecheck.ExpectKnownValue(
						"circleci_oidc_custom_claims.test",
						tfjsonpath.New("ttl"),
						knownvalue.StringExact("30m"),
					),
				},
			},
			// Import testing: the ID is the organization ID alone.
			{
				ResourceName:                         "circleci_oidc_custom_claims.test",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "organization_id",
				// The API stores a duration reformatted: a configured "30m" is
				// returned as "30m0s". durationValue treats those as equal, so a
				// managed resource keeps the configured spelling in state while an
				// imported one holds the API's. ImportStateVerify compares raw
				// strings and cannot see that they are the same duration.
				// TestAccOIDCCustomClaimsTTLSpellingDoesNotDiff covers the behaviour
				// this cannot.
				ImportStateVerifyIgnore: []string{"ttl"},
				ImportStateId:           testOIDCOrgID,
			},
			// Delete testing automatically occurs in TestCase.
		},
	})

	requests := api.recorded()

	var sawPatch, sawGet, sawDelete bool
	for _, req := range requests {
		switch {
		case req == "PATCH /api/v2/org/"+testOIDCOrgID+"/oidc-custom-claims":
			sawPatch = true
		case req == "GET /api/v2/org/"+testOIDCOrgID+"/oidc-custom-claims":
			sawGet = true
		case strings.HasPrefix(req, "DELETE /api/v2/org/"+testOIDCOrgID+"/oidc-custom-claims?claims="):
			sawDelete = true
			// Both managed claims must be named: the query parameter is required
			// and the route resets exactly what it is told to.
			if !strings.Contains(req, "audience") || !strings.Contains(req, "ttl") {
				t.Errorf("delete request %q does not name both managed claims", req)
			}
		}
	}

	if !sawPatch {
		t.Errorf("no PATCH to the org-level route, got %v", requests)
	}
	if !sawGet {
		t.Errorf("no GET of the org-level route, got %v", requests)
	}
	if !sawDelete {
		t.Errorf("no DELETE carrying a claims parameter, got %v", requests)
	}
}

// TestAccOIDCCustomClaimsResourceProjectScope covers the project-scoped variant,
// which is the same resource with project_id set.
func TestAccOIDCCustomClaimsResourceProjectScope(t *testing.T) {
	api := newOIDCClaimsAPI()
	srv := newOIDCClaimsServer(t, api)

	config := governanceProviderConfig(srv.URL) + fmt.Sprintf(`
resource "circleci_oidc_custom_claims" "test" {
  organization_id = %q
  project_id      = %q
  ttl             = "45m"
}
`, testOIDCOrgID, testOIDCProjectID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_oidc_custom_claims.test",
						tfjsonpath.New("project_id"),
						knownvalue.StringExact(testOIDCProjectID),
					),
					statecheck.ExpectKnownValue(
						"circleci_oidc_custom_claims.test",
						tfjsonpath.New("ttl"),
						knownvalue.StringExact("45m"),
					),
					// audience was never configured, so it stays null.
					statecheck.ExpectKnownValue(
						"circleci_oidc_custom_claims.test",
						tfjsonpath.New("audience"),
						knownvalue.Null(),
					),
				},
			},
			// Import testing: the ID is organization_id/project_id.
			{
				ResourceName:                         "circleci_oidc_custom_claims.test",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "organization_id",
				// The API stores a duration reformatted: a configured "30m" is
				// returned as "30m0s". durationValue treats those as equal, so a
				// managed resource keeps the configured spelling in state while an
				// imported one holds the API's. ImportStateVerify compares raw
				// strings and cannot see that they are the same duration.
				// TestAccOIDCCustomClaimsTTLSpellingDoesNotDiff covers the behaviour
				// this cannot.
				ImportStateVerifyIgnore: []string{"ttl"},
				ImportStateId:           testOIDCOrgID + "/" + testOIDCProjectID,
			},
		},
	})

	projectRoute := "/api/v2/org/" + testOIDCOrgID + "/project/" + testOIDCProjectID + "/oidc-custom-claims"

	var sawProjectPatch, sawTTLOnlyDelete bool
	for _, req := range api.recorded() {
		if req == "PATCH "+projectRoute {
			sawProjectPatch = true
		}
		// Only the ttl claim was managed, so only it may be reset. Wiping the
		// audience too would clobber a claim Terraform never set.
		if req == "DELETE "+projectRoute+"?claims=ttl" {
			sawTTLOnlyDelete = true
		}
		if strings.Contains(req, "claims=audience") {
			t.Errorf("request %q resets the audience, which this resource never managed", req)
		}
	}

	if !sawProjectPatch {
		t.Errorf("no PATCH to the project-level route %q", projectRoute)
	}
	if !sawTTLOnlyDelete {
		t.Errorf("no DELETE naming only the ttl claim, got %v", api.recorded())
	}
}

// TestAccOIDCCustomClaimsDropsResetClaims covers the authoritative model: a claim
// removed from the configuration is reset rather than abandoned, because the
// PATCH route would otherwise leave it in place forever.
func TestAccOIDCCustomClaimsDropsResetClaims(t *testing.T) {
	api := newOIDCClaimsAPI()
	srv := newOIDCClaimsServer(t, api)

	both := governanceProviderConfig(srv.URL) + fmt.Sprintf(`
resource "circleci_oidc_custom_claims" "test" {
  organization_id = %q
  audience        = ["one"]
  ttl             = "1h"
}
`, testOIDCOrgID)

	ttlOnly := governanceProviderConfig(srv.URL) + fmt.Sprintf(`
resource "circleci_oidc_custom_claims" "test" {
  organization_id = %q
  ttl             = "1h"
}
`, testOIDCOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{Config: both},
			{
				Config: ttlOnly,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_oidc_custom_claims.test",
						tfjsonpath.New("audience"),
						knownvalue.Null(),
					),
					statecheck.ExpectKnownValue(
						"circleci_oidc_custom_claims.test",
						tfjsonpath.New("ttl"),
						knownvalue.StringExact("1h"),
					),
				},
			},
		},
	})

	var sawAudienceOnlyDelete bool
	for _, req := range api.recorded() {
		if req == "DELETE /api/v2/org/"+testOIDCOrgID+"/oidc-custom-claims?claims=audience" {
			sawAudienceOnlyDelete = true
		}
	}

	if !sawAudienceOnlyDelete {
		t.Errorf("dropping audience from the configuration did not reset it, got %v", api.recorded())
	}
}

// TestAccOIDCCustomClaimsResetOutsideTerraform covers drift detection. The API
// answers 200 with only org_id for an uncustomized scope, never 404, so an empty
// customization is the only "gone" signal there is.
func TestAccOIDCCustomClaimsResetOutsideTerraform(t *testing.T) {
	api := newOIDCClaimsAPI()
	srv := newOIDCClaimsServer(t, api)

	config := governanceProviderConfig(srv.URL) + fmt.Sprintf(`
resource "circleci_oidc_custom_claims" "test" {
  organization_id = %q
  ttl             = "1h"
}
`, testOIDCOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					api.reset("/api/v2/org/" + testOIDCOrgID + "/oidc-custom-claims")
				},
				// A refresh finds nothing customized and drops the resource, so the
				// plan that follows recreates it rather than reporting no changes.
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestOIDCCustomClaimsRejectsEmptyConfiguration covers the config validator: a
// resource that customizes nothing has nothing to write, and the API cannot
// record it.
func TestOIDCCustomClaimsRejectsEmptyConfiguration(t *testing.T) {
	api := newOIDCClaimsAPI()
	srv := newOIDCClaimsServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: governanceProviderConfig(srv.URL) + fmt.Sprintf(`
resource "circleci_oidc_custom_claims" "test" {
  organization_id = %q
}
`, testOIDCOrgID),
				ExpectError: regexp.MustCompile(
					`(?s)At least one of these attributes must be configured: \[audience,ttl\]`,
				),
			},
		},
	})
}

// TestOIDCCustomClaimsRejectsInvalidTTL covers the duration validator, which
// turns an HTTP 400 into a plan-time error.
func TestOIDCCustomClaimsRejectsInvalidTTL(t *testing.T) {
	api := newOIDCClaimsAPI()
	srv := newOIDCClaimsServer(t, api)

	for _, ttl := range []string{"1.5h", "1", "forever"} {
		t.Run(ttl, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: governanceProviderFactories,
				Steps: []resource.TestStep{
					{
						Config: governanceProviderConfig(srv.URL) + fmt.Sprintf(`
resource "circleci_oidc_custom_claims" "test" {
  organization_id = %q
  ttl             = %q
}
`, testOIDCOrgID, ttl),
						ExpectError: regexp.MustCompile(`(?s)must be a duration of unit-suffixed integers`),
					},
				},
			})
		})
	}
}

func TestOIDCCustomClaimsImportIDValidation(t *testing.T) {
	api := newOIDCClaimsAPI()
	srv := newOIDCClaimsServer(t, api)

	config := governanceProviderConfig(srv.URL) + fmt.Sprintf(`
resource "circleci_oidc_custom_claims" "test" {
  organization_id = %q
  ttl             = "1h"
}
`, testOIDCOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				ResourceName:  "circleci_oidc_custom_claims.test",
				ImportState:   true,
				ImportStateId: "org/project/extra",
				ExpectError:   regexp.MustCompile(`Invalid Import ID Format`),
			},
		},
	})
}

// oidcNormalizeTTL reproduces the API's rewriting of a duration.
//
// the API formats the stored TTL with time.Duration.String(), so a
// request of "3h" is answered with "3h0m0s". A mock that echoed the request could
// never surface the resulting drift.
func oidcNormalizeTTL(ttl *string) *string {
	if ttl == nil {
		return nil
	}

	parsed, err := time.ParseDuration(*ttl)
	if err != nil {
		return ttl
	}

	normalized := parsed.String()

	return &normalized
}

// TestAccOIDCCustomClaimsTTLSpellingDoesNotDiff is the regression test for the
// duration the API rewrites.
//
// the API formats the stored TTL with time.Duration.String(), so a
// configured "90m" is returned as "1h30m0s". With a plain string attribute that
// produced "Provider produced inconsistent result after apply" on create and a
// diff on every plan afterwards. durationValue compares the two as durations.
func TestAccOIDCCustomClaimsTTLSpellingDoesNotDiff(t *testing.T) {
	// Every spelling below is the same duration, so none may produce a diff.
	for _, ttl := range []string{"90m", "1h30m", "5400s"} {
		t.Run(ttl, func(t *testing.T) {
			api := newOIDCClaimsAPI()
			srv := newOIDCClaimsServer(t, api)

			config := governanceProviderConfig(srv.URL) + fmt.Sprintf(`
resource "circleci_oidc_custom_claims" "test" {
  organization_id = %q
  ttl             = %q
}
`, testOIDCOrgID, ttl)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{
						Config: config,
						ConfigStateChecks: []statecheck.StateCheck{
							// State keeps the configured spelling, not the API's.
							statecheck.ExpectKnownValue(
								"circleci_oidc_custom_claims.test",
								tfjsonpath.New("ttl"),
								knownvalue.StringExact(ttl),
							),
						},
					},
					// A second plan must be empty: the API answers "1h30m0s" while the
					// configuration says something else, and that is not a change.
					{
						Config:   config,
						PlanOnly: true,
					},
				},
			})
		})
	}
}
