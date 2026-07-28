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
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

const testPolicyOwner = "00000000-0000-0000-0000-000000000000"

// configPolicyAPI is an in-memory stand-in for the v2 config policy endpoints.
//
// It reproduces the property that shapes the resource: an upload replaces the
// whole bundle for a policy context, so nothing merges.
type configPolicyAPI struct {
	mu       sync.Mutex
	bundles  map[string]map[string]string
	settings map[string]bool
	requests []string
	// uploadStatus, when non-zero, makes every upload fail with that status,
	// standing in for the plan gate the real API applies.
	uploadStatus int
	// settingsStatus does the same for decision settings writes.
	settingsStatus int
}

func newConfigPolicyAPI() *configPolicyAPI {
	return &configPolicyAPI{
		bundles:  map[string]map[string]string{},
		settings: map[string]bool{},
	}
}

// newConfigPolicyServer starts a fake API. Every request line is recorded so
// tests can assert the routes, including the policy context segment.
func newConfigPolicyServer(t *testing.T, api *configPolicyAPI) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		api.requests = append(api.requests, r.Method+" "+r.RequestURI)
		api.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.HasSuffix(r.URL.Path, "/decision/settings"):
			api.handleSettings(t, w, r)
		case strings.HasSuffix(r.URL.Path, "/policy-bundle"):
			api.handleBundle(t, w, r)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

func (a *configPolicyAPI) handleBundle(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()

	switch r.Method {
	case http.MethodPost:
		if a.uploadStatus != 0 {
			w.WriteHeader(a.uploadStatus)
			_, _ = w.Write([]byte(`{"error":"config policies require the Scale plan"}`))

			return
		}

		var body struct {
			Policies map[string]string `json:"policies"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("upload body is not JSON: %v", err)
		}
		if body.Policies == nil {
			t.Error("upload body omitted the policies field; an empty bundle must be sent as {}")
		}

		a.mu.Lock()
		// Replacement, not a merge: the request body becomes the bundle.
		a.bundles[r.URL.Path] = body.Policies
		a.mu.Unlock()

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"created":[],"modified":[],"deleted":[]}`))
	case http.MethodGet:
		a.mu.Lock()
		bundle := a.bundles[r.URL.Path]
		a.mu.Unlock()

		// The bundle response keys each policy name to a Policy document, and an
		// empty context answers 200 with {} rather than 404.
		documents := make(map[string]any, len(bundle))
		for name, content := range bundle {
			documents[name] = map[string]any{
				"name":       name,
				"content":    content,
				"created_at": "2024-01-02T03:04:05Z",
				"created_by": "alice",
			}
		}

		_ = json.NewEncoder(w).Encode(documents)
	default:
		t.Errorf("unexpected bundle request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *configPolicyAPI) handleSettings(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()

	if a.settingsStatus != 0 {
		w.WriteHeader(a.settingsStatus)
		_, _ = w.Write([]byte(`{"error":"config policies require the Scale plan"}`))

		return
	}

	switch r.Method {
	case http.MethodPatch:
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("settings body is not JSON: %v", err)
		}

		a.mu.Lock()
		if body.Enabled != nil {
			a.settings[r.URL.Path] = *body.Enabled
		}
		enabled := a.settings[r.URL.Path]
		a.mu.Unlock()

		_ = json.NewEncoder(w).Encode(map[string]any{"enabled": enabled})
	case http.MethodGet:
		a.mu.Lock()
		enabled := a.settings[r.URL.Path]
		a.mu.Unlock()

		_ = json.NewEncoder(w).Encode(map[string]any{"enabled": enabled})
	default:
		t.Errorf("unexpected settings request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// recorded returns the request lines seen so far.
func (a *configPolicyAPI) recorded() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]string(nil), a.requests...)
}

// bundle returns the stored bundle for a policy context.
func (a *configPolicyAPI) bundle(policyContext string) map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()

	path := "/api/v2/owner/" + testPolicyOwner + "/context/" + policyContext + "/policy-bundle"
	stored := a.bundles[path]

	copied := make(map[string]string, len(stored))
	for name, content := range stored {
		copied[name] = content
	}

	return copied
}

// emptyBundle clears a policy context, simulating policies removed outside
// Terraform.
func (a *configPolicyAPI) emptyBundle(policyContext string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	path := "/api/v2/owner/" + testPolicyOwner + "/context/" + policyContext + "/policy-bundle"
	a.bundles[path] = map[string]string{}
}

func configPolicyBundleConfig(host, policies string) string {
	return governanceProviderConfig(host) + fmt.Sprintf(`
resource "circleci_config_policy_bundle" "test" {
  owner_id = %q
  policies = %s
}
`, testPolicyOwner, policies)
}

func TestAccConfigPolicyBundleResource(t *testing.T) {
	api := newConfigPolicyAPI()
	srv := newConfigPolicyServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configPolicyBundleConfig(srv.URL, `{
    "allow-docker.rego"     = "package org\nallow_docker = true\n"
    "require-approval.rego" = "package org\n"
  }`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_config_policy_bundle.test",
						tfjsonpath.New("owner_id"),
						knownvalue.StringExact(testPolicyOwner),
					),
					// The policy context defaults to "config", never to a CircleCI
					// context id.
					statecheck.ExpectKnownValue(
						"circleci_config_policy_bundle.test",
						tfjsonpath.New("policy_context"),
						knownvalue.StringExact("config"),
					),
					statecheck.ExpectKnownValue(
						"circleci_config_policy_bundle.test",
						tfjsonpath.New("policies").AtMapKey("allow-docker.rego"),
						knownvalue.StringExact("package org\nallow_docker = true\n"),
					),
				},
			},
			// Import testing: the ID is the owner ID alone, which assumes the
			// default policy context.
			{
				ResourceName:                         "circleci_config_policy_bundle.test",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "owner_id",
				ImportStateId:                        testPolicyOwner,
			},
			// Delete testing automatically occurs in TestCase.
		},
	})

	// Destroy empties the context: there is no delete route, so an empty upload
	// is how a bundle is removed.
	if got := api.bundle("config"); len(got) != 0 {
		t.Errorf("bundle after destroy = %#v, want empty", got)
	}

	var sawUpload, sawGet bool
	for _, req := range api.recorded() {
		switch req {
		case "POST /api/v2/owner/" + testPolicyOwner + "/context/config/policy-bundle":
			sawUpload = true
		case "GET /api/v2/owner/" + testPolicyOwner + "/context/config/policy-bundle":
			sawGet = true
		}
	}

	if !sawUpload {
		t.Errorf("no upload to the config policy context, got %v", api.recorded())
	}
	if !sawGet {
		t.Errorf("no read of the config policy context, got %v", api.recorded())
	}
}

// TestAccConfigPolicyBundleReplacesWholeBundle is the load-bearing test for the
// modelling decision. The upload route replaces every policy at once, so
// removing a policy from the map must remove it from CircleCI — and a per-policy
// resource would have made two Terraform resources fight over the same bundle.
func TestAccConfigPolicyBundleReplacesWholeBundle(t *testing.T) {
	api := newConfigPolicyAPI()
	srv := newConfigPolicyServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configPolicyBundleConfig(srv.URL, `{
    "a.rego" = "package org.a"
    "b.rego" = "package org.b"
  }`),
			},
			{
				// Dropping b.rego and rewriting a.rego: the whole bundle is
				// replaced, so b.rego must be gone from CircleCI afterwards.
				Config: configPolicyBundleConfig(srv.URL, `{
    "a.rego" = "package org.a.v2"
  }`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_config_policy_bundle.test",
						tfjsonpath.New("policies"),
						knownvalue.MapExact(map[string]knownvalue.Check{
							"a.rego": knownvalue.StringExact("package org.a.v2"),
						}),
					),
				},
				// Assert against the fake API mid-test rather than afterwards: the
				// test framework destroys the resource at the end, which empties
				// the bundle.
				Check: func(*terraform.State) error {
					want := map[string]string{"a.rego": "package org.a.v2"}
					if got := api.bundle("config"); !mapsEqual(got, want) {
						return fmt.Errorf("bundle after the second apply = %#v, want %#v", got, want)
					}

					return nil
				},
			},
		},
	})
}

// TestAccConfigPolicyBundleEmptyBundle covers a deliberately empty bundle, which
// must stay in state rather than be treated as a missing resource.
func TestAccConfigPolicyBundleEmptyBundle(t *testing.T) {
	api := newConfigPolicyAPI()
	srv := newConfigPolicyServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configPolicyBundleConfig(srv.URL, `{}`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_config_policy_bundle.test",
						tfjsonpath.New("policies"),
						knownvalue.MapExact(map[string]knownvalue.Check{}),
					),
				},
			},
			{
				// A refresh of an empty bundle must not drop the resource: the
				// configuration asked for exactly this.
				RefreshState: true,
			},
		},
	})
}

// TestAccConfigPolicyBundleEmptiedOutsideTerraform covers drift detection. An
// empty policy context answers 200 with {}, never 404, so an unexpectedly empty
// bundle is the only "gone" signal available.
func TestAccConfigPolicyBundleEmptiedOutsideTerraform(t *testing.T) {
	api := newConfigPolicyAPI()
	srv := newConfigPolicyServer(t, api)

	config := configPolicyBundleConfig(srv.URL, `{
    "a.rego" = "package org.a"
  }`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig:          func() { api.emptyBundle("config") },
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccConfigPolicyBundleCustomContext covers the non-default policy context,
// and pins that the segment carries the policy context rather than anything
// resembling a CircleCI context id.
func TestAccConfigPolicyBundleCustomContext(t *testing.T) {
	t.Parallel()

	// CircleCI documents a "custom" policy context, but every the API route
	// validates its context with validation.In(internal.Config) and rejects
	// anything else with a 400. Offering it in the schema invited a failure at
	// apply, so the validator refuses it at plan time instead.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: governanceProviderConfig("http://127.0.0.1:1") + `
resource "circleci_config_policy_bundle" "test" {
  owner_id       = "00000000-1111-2222-3333-444444444444"
  policy_context = "custom"
  policies       = { "a.rego" = "package org" }
}
`,
			ExpectError: regexp.MustCompile(`(?s)policy_context`),
		}},
	})
}

func TestConfigPolicyBundleRejectsUnknownContext(t *testing.T) {
	api := newConfigPolicyAPI()
	srv := newConfigPolicyServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: governanceProviderConfig(srv.URL) + fmt.Sprintf(`
resource "circleci_config_policy_bundle" "test" {
  owner_id       = %q
  policy_context = "00000000-0000-0000-0000-000000000001"
  policies       = { "a.rego" = "package org" }
}
`, testPolicyOwner),
				ExpectError: regexp.MustCompile(`(?s)Attribute policy_context value must be one of`),
			},
		},
	})
}

// TestConfigPolicyBundleUploadErrorMentionsPlan covers the diagnostic: a rejected
// upload is most often a plan problem, and the message has to say so.
func TestConfigPolicyBundleUploadErrorMentionsPlan(t *testing.T) {
	api := newConfigPolicyAPI()
	api.uploadStatus = http.StatusForbidden
	srv := newConfigPolicyServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      configPolicyBundleConfig(srv.URL, `{ "a.rego" = "package org" }`),
				ExpectError: regexp.MustCompile(`(?s)Scale plan`),
			},
		},
	})
}

// mapsEqual compares two string maps.
func mapsEqual(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}

	for key, value := range want {
		if got[key] != value {
			return false
		}
	}

	return true
}
