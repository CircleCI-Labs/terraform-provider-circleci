// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// checkoutKeyAPI is an in-memory stand-in for the v2 checkout key endpoints.
type checkoutKeyAPI struct {
	mu       sync.Mutex
	keys     []map[string]any
	requests []string
	// missing makes every single-key GET answer 404, to simulate a key deleted
	// outside Terraform.
	missing bool
	// pageSize, when positive, splits list responses so pagination is exercised.
	pageSize int
	created  int
}

const checkoutKeyTestSlug = "github/my-org/my-repo"

// newCheckoutKeyServer starts a fake API. Every request line is recorded so tests
// can assert the wire format of the project slug.
func newCheckoutKeyServer(t *testing.T, api *checkoutKeyAPI) *httptest.Server {
	t.Helper()

	prefix := "/api/v2/project/" + checkoutKeyTestSlug + "/checkout-key"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		api.requests = append(api.requests, r.Method+" "+r.RequestURI)
		api.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		if !strings.HasPrefix(r.URL.Path, prefix) {
			t.Errorf("unexpected request path %q, want it to start with %q", r.URL.Path, prefix)
			w.WriteHeader(http.StatusNotFound)

			return
		}

		fingerprint := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, prefix), "/")

		switch {
		case r.Method == http.MethodPost:
			api.handleCreate(t, w, r)
		case r.Method == http.MethodGet && fingerprint == "":
			api.handleList(w, r)
		case r.Method == http.MethodGet:
			api.handleGet(w, fingerprint)
		case r.Method == http.MethodDelete:
			api.handleDelete(w, fingerprint)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

func (a *checkoutKeyAPI) handleCreate(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()

	var body struct {
		Type string `json:"type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("create body is not JSON: %v", err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.created++

	// A key created as "user-key" is reported as "github-user-key" from then on.
	reported := body.Type
	if reported == "user-key" {
		reported = "github-user-key"
	}

	key := map[string]any{
		"public_key":  fmt.Sprintf("ssh-rsa AAAA%d", a.created),
		"type":        reported,
		"fingerprint": fmt.Sprintf("aa:bb:cc:%02d", a.created),
		"preferred":   true,
		"created_at":  "2024-01-02T03:04:05.000Z",
	}
	a.keys = append(a.keys, key)

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(key)
}

func (a *checkoutKeyAPI) handleList(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	items := a.keys
	next := ""

	if a.pageSize > 0 {
		start := 0
		if token := r.URL.Query().Get("page-token"); token != "" {
			// Tokens are "page-N" where N is the index to resume from.
			_, _ = fmt.Sscanf(token, "page-%d", &start)
		}
		end := min(start+a.pageSize, len(items))
		if end < len(items) {
			next = fmt.Sprintf("page-%d", end)
		}
		items = items[start:end]
	}

	payload := map[string]any{"items": items}
	if next != "" {
		payload["next_page_token"] = next
	} else {
		payload["next_page_token"] = nil
	}

	_ = json.NewEncoder(w).Encode(payload)
}

func (a *checkoutKeyAPI) handleGet(w http.ResponseWriter, fingerprint string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.missing {
		for _, key := range a.keys {
			if key["fingerprint"] == fingerprint {
				_ = json.NewEncoder(w).Encode(key)

				return
			}
		}
	}

	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"message":"Checkout key not found"}`))
}

func (a *checkoutKeyAPI) handleDelete(w http.ResponseWriter, fingerprint string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for i, key := range a.keys {
		if key["fingerprint"] == fingerprint {
			a.keys = append(a.keys[:i], a.keys[i+1:]...)
			_, _ = w.Write([]byte(`{"message":"ok"}`))

			return
		}
	}

	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"message":"Checkout key not found"}`))
}

// recorded returns the request lines seen so far.
func (a *checkoutKeyAPI) recorded() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]string(nil), a.requests...)
}

// setMissing makes single-key reads answer 404.
func (a *checkoutKeyAPI) setMissing(missing bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.missing = missing
}

func checkoutKeyProviderConfig(host string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
`, host)
}

func checkoutKeyResourceConfig(host, keyType string) string {
	return checkoutKeyProviderConfig(host) + fmt.Sprintf(`
resource "circleci_checkout_key" "test" {
  project_slug = %q
  type         = %q
}
`, checkoutKeyTestSlug, keyType)
}

func TestAccCheckoutKeyResource(t *testing.T) {
	api := &checkoutKeyAPI{}
	srv := newCheckoutKeyServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: checkoutKeyResourceConfig(srv.URL, "deploy-key"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_checkout_key.test",
						tfjsonpath.New("project_slug"),
						knownvalue.StringExact(checkoutKeyTestSlug),
					),
					statecheck.ExpectKnownValue(
						"circleci_checkout_key.test",
						tfjsonpath.New("type"),
						knownvalue.StringExact("deploy-key"),
					),
					statecheck.ExpectKnownValue(
						"circleci_checkout_key.test",
						tfjsonpath.New("fingerprint"),
						knownvalue.StringExact("aa:bb:cc:01"),
					),
					statecheck.ExpectKnownValue(
						"circleci_checkout_key.test",
						tfjsonpath.New("public_key"),
						knownvalue.StringExact("ssh-rsa AAAA1"),
					),
					statecheck.ExpectKnownValue(
						"circleci_checkout_key.test",
						tfjsonpath.New("preferred"),
						knownvalue.Bool(true),
					),
					statecheck.ExpectKnownValue(
						"circleci_checkout_key.test",
						tfjsonpath.New("created_at"),
						knownvalue.StringExact("2024-01-02T03:04:05.000Z"),
					),
				},
			},
			// Import testing: the ID is project_slug/fingerprint, and the slug
			// itself contains slashes.
			{
				ResourceName:                         "circleci_checkout_key.test",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "fingerprint",
				ImportStateId:                        checkoutKeyTestSlug + "/aa:bb:cc:01",
			},
			// Delete testing automatically occurs in TestCase.
		},
	})

	// The project slug's separators must reach the wire literally: an escaped
	// %2F does not match the API route.
	requests := api.recorded()
	if len(requests) == 0 {
		t.Fatal("the provider made no requests")
	}

	var sawCreate, sawRead, sawDelete bool
	for _, req := range requests {
		if strings.Contains(req, "%2F") {
			t.Errorf("request %q escapes the project slug separators", req)
		}

		switch req {
		case "POST /api/v2/project/github/my-org/my-repo/checkout-key":
			sawCreate = true
		case "GET /api/v2/project/github/my-org/my-repo/checkout-key/aa:bb:cc:01":
			sawRead = true
		case "DELETE /api/v2/project/github/my-org/my-repo/checkout-key/aa:bb:cc:01":
			sawDelete = true
		}
	}

	if !sawCreate {
		t.Errorf("no create request with the expected URI, got %q", requests)
	}
	if !sawRead {
		t.Errorf("no read request with the expected URI, got %q", requests)
	}
	if !sawDelete {
		t.Errorf("no delete request with the expected URI, got %q", requests)
	}
}

// TestAccCheckoutKeyResource_UserKeyType covers the vocabulary mismatch: the API
// accepts "user-key" but reports "github-user-key", which would otherwise make
// the applied state differ from the plan.
func TestAccCheckoutKeyResource_UserKeyType(t *testing.T) {
	api := &checkoutKeyAPI{}
	srv := newCheckoutKeyServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: checkoutKeyResourceConfig(srv.URL, "user-key"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_checkout_key.test",
						tfjsonpath.New("type"),
						knownvalue.StringExact("user-key"),
					),
				},
			},
			// A refresh must keep type as configured rather than adopting the
			// API's "github-user-key", which would plan a spurious replacement.
			{
				Config:   checkoutKeyResourceConfig(srv.URL, "user-key"),
				PlanOnly: true,
			},
		},
	})
}

// TestAccCheckoutKeyResource_RemovedOutsideTerraform proves drift detection: a
// 404 on read drops the resource from state, so the next plan recreates it.
func TestAccCheckoutKeyResource_RemovedOutsideTerraform(t *testing.T) {
	api := &checkoutKeyAPI{}
	srv := newCheckoutKeyServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: checkoutKeyResourceConfig(srv.URL, "deploy-key"),
			},
			{
				PreConfig:          func() { api.setMissing(true) },
				Config:             checkoutKeyResourceConfig(srv.URL, "deploy-key"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// Restore the key so the framework's destroy step succeeds.
				PreConfig: func() { api.setMissing(false) },
				Config:    checkoutKeyResourceConfig(srv.URL, "deploy-key"),
			},
		},
	})
}

func TestAccCheckoutKeyResource_RejectsInvalidType(t *testing.T) {
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      checkoutKeyResourceConfig("http://127.0.0.1:1", "ssh-key"),
			ExpectError: regexp.MustCompile(`(?s)type.*(deploy-key|user-key)`),
		}},
	})
}

// TestAccCheckoutKeyResource_RejectsStandaloneSlug covers the account-type
// condition confirmed against the real API: any circleci/-prefixed slug (GitHub
// App, GitLab, or standalone) answers 400 "This API is not supported for this
// project." The slug shape is knowable purely from configuration, so it is
// rejected at plan time instead, and no request should ever reach the API.
func TestAccCheckoutKeyResource_RejectsStandaloneSlug(t *testing.T) {
	api := &checkoutKeyAPI{}
	srv := newCheckoutKeyServer(t, api)

	cfg := checkoutKeyProviderConfig(srv.URL) + `
resource "circleci_checkout_key" "test" {
  project_slug = "circleci/aaaaaaaa-0000-0000-0000-000000000001/bbbbbbbb-0000-0000-0000-000000000002"
  type         = "deploy-key"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`(?s)Checkout keys are not available.*GitHub OAuth.*Bitbucket`),
		}},
	})

	if requests := api.recorded(); len(requests) != 0 {
		t.Errorf("got requests %v, want none: the configuration never passed validation", requests)
	}
}

func TestAccCheckoutKeyResource_RejectsInvalidProjectSlug(t *testing.T) {
	cfg := checkoutKeyProviderConfig("http://127.0.0.1:1") + `
resource "circleci_checkout_key" "test" {
  project_slug = "my-org/my-repo"
  type         = "deploy-key"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`vcs-type/org-name/repo-name`),
		}},
	})
}
