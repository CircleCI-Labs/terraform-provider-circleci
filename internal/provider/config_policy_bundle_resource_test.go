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
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

const testPolicyOwner = "00000000-0000-0000-0000-000000000000"

// policyNamePattern extracts a Rego policy's declared name from
// `policy_name["some_name"]`, the rule every real bundle entry is required to
// declare as its first rule. See extractPolicyName.
var policyNamePattern = regexp.MustCompile(`policy_name\["([^"]+)"\]`)

// extractPolicyName reproduces enough of the real upload route's Rego
// validation for the fake to key a bundle the way CircleCI actually does.
//
// [NET, measured 2026-08-21] The real API keys every bundle entry by the name
// declared in its content's required first rule, `policy_name["some_name"]`,
// and discards whatever map key the upload used — confirmed by uploading
// {"probe.rego": policy_name["probe_policy"]} and reading back
// {"probe_policy": {...}} with "probe.rego" appearing nowhere. Content with no
// rule at all, or whose first rule is not this declaration, is rejected with
// HTTP 400. This helper is the fake's stand-in for that parse: it is
// deliberately much looser than the real Rego parser (no "first rule" or
// "package" requirement), because the fake only needs to reproduce the
// key-renaming behavior, not reimplement Rego.
func extractPolicyName(content string) (name string, ok bool) {
	match := policyNamePattern.FindStringSubmatch(content)
	if match == nil {
		return "", false
	}

	return match[1], true
}

// configPolicyAPI is an in-memory stand-in for the v2 config policy endpoints.
//
// It reproduces the two properties that shape the resource: an upload
// replaces the whole bundle for a policy context, so nothing merges, and a
// bundle entry is keyed by its Rego-declared policy_name rather than by
// whatever map key an upload used (see extractPolicyName).
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

		// Re-key every submitted policy by its declared policy_name, exactly as
		// the real API does, discarding the submitted map key. A submission
		// whose content declares no policy_name is the fake's stand-in for the
		// real 400 that content without that rule gets.
		renamed := make(map[string]string, len(body.Policies))
		for submittedKey, content := range body.Policies {
			name, ok := extractPolicyName(content)
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid rego content: failed to parse policy file(s): ` +
					`failed to parse file: \"` + submittedKey + `\": must declare rule \"policy_name\" ` +
					`but module contains no rules"}`))

				return
			}

			renamed[name] = content
		}

		a.mu.Lock()
		previous := a.bundles[r.URL.Path]
		// Replacement, not a merge: the request body becomes the bundle.
		a.bundles[r.URL.Path] = renamed
		a.mu.Unlock()

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(bundleDiff(previous, renamed))
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
				// The map key matches each policy's declared policy_name exactly,
				// which is required: see extractPolicyName and
				// configPolicyBundleResource.warnOnKeyMismatch. A key of
				// "allow-docker.rego" holding `policy_name["allow_docker"]` would be
				// silently re-keyed by the real API and is covered separately by
				// TestConfigPolicyBundleWarnsOnKeyMismatch.
				Config: configPolicyBundleConfig(srv.URL, `{
    "allow_docker"     = "package org\n\npolicy_name[\"allow_docker\"]\n"
    "require_approval" = "package org\n\npolicy_name[\"require_approval\"]\n"
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
						tfjsonpath.New("policies").AtMapKey("allow_docker"),
						knownvalue.StringExact("package org\n\npolicy_name[\"allow_docker\"]\n"),
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
    "org_a" = "package org\n\npolicy_name[\"org_a\"]\n"
    "org_b" = "package org\n\npolicy_name[\"org_b\"]\n"
  }`),
			},
			{
				// Dropping org_b and rewriting org_a: the whole bundle is replaced,
				// so org_b must be gone from CircleCI afterwards.
				Config: configPolicyBundleConfig(srv.URL, `{
    "org_a" = "package org\n\npolicy_name[\"org_a\"]\nversion = 2\n"
  }`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_config_policy_bundle.test",
						tfjsonpath.New("policies"),
						knownvalue.MapExact(map[string]knownvalue.Check{
							"org_a": knownvalue.StringExact("package org\n\npolicy_name[\"org_a\"]\nversion = 2\n"),
						}),
					),
				},
				// Assert against the fake API mid-test rather than afterwards: the
				// test framework destroys the resource at the end, which empties
				// the bundle.
				Check: func(*terraform.State) error {
					want := map[string]string{"org_a": "package org\n\npolicy_name[\"org_a\"]\nversion = 2\n"}
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
    "org_a" = "package org\n\npolicy_name[\"org_a\"]\n"
  }`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				// Read treats an emptied bundle as gone and drops it from state (see
				// configPolicyBundleResource.Read), so the plan that follows recreates
				// it rather than reporting no changes.
				PreConfig: func() { api.emptyBundle("config") },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_config_policy_bundle.test",
							plancheck.ResourceActionCreate,
						),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_config_policy_bundle.test",
						tfjsonpath.New("policies"),
						knownvalue.MapExact(map[string]knownvalue.Check{
							"org_a": knownvalue.StringExact("package org\n\npolicy_name[\"org_a\"]\n"),
						}),
					),
				},
			},
		},
	})
}

// TestAccConfigPolicyBundleCustomContext covers the non-default policy context,
// and pins that the segment carries the policy context rather than anything
// resembling a CircleCI context id.
func TestAccConfigPolicyBundleCustomContext(t *testing.T) {
	t.Parallel()

	// CircleCI documents a "custom" policy context, but the API rejects
	// anything other than the values it actually accepts with a 400.
	// Offering it in the schema invited a failure at apply, so the validator
	// refuses it at plan time instead.
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

// TestConfigPolicyBundleWarnsOnKeyMismatch is the regression test for the
// resource's central trap: a map key that does not equal its policy's
// declared policy_name is silently renamed by CircleCI (see
// PolicyBundle's [NET] comment in policy.go), producing a bundle whose keys
// never match this configuration's again. Without warnOnKeyMismatch, an apply
// like this one succeeds cleanly and the mismatch surfaces only on the next
// plan, indistinguishable from any other drift.
//
// This talks to the resource's upload method directly rather than through the
// full acceptance harness, because Terraform's testing helpers have no
// assertion for a warning diagnostic's text — see
// config_policy_settings_resource_test.go, which only ever asserts the
// warning's side effect, never its wording, for the same reason.
func TestConfigPolicyBundleWarnsOnKeyMismatch(t *testing.T) {
	t.Parallel()

	api := newConfigPolicyAPI()
	srv := newConfigPolicyServer(t, api)

	r := &configPolicyBundleResource{
		client: circleci.New(circleci.Config{Host: srv.URL, Token: "tok"}),
	}

	policies, diags := types.MapValue(types.StringType, map[string]attr.Value{
		"allow-docker.rego": types.StringValue("package org\n\npolicy_name[\"allow_docker\"]\n"),
	})
	if diags.HasError() {
		t.Fatalf("building the policies map: %+v", diags)
	}

	plan := configPolicyBundleResourceModel{
		OwnerID:       types.StringValue(testPolicyOwner),
		PolicyContext: types.StringValue("config"),
		Policies:      policies,
	}

	var applyDiags diag.Diagnostics
	if ok := r.upload(context.Background(), plan, "creating", &applyDiags); !ok {
		t.Fatalf("upload failed: %+v", applyDiags)
	}

	// The upload must still be treated as successful: CircleCI accepted it, and
	// the mismatch is a warning about future drift, not a reason to fail this
	// apply.
	if applyDiags.HasError() {
		t.Fatalf("upload reported an error for a mismatch that is only ever a warning: %+v", applyDiags)
	}

	var found bool
	for _, d := range applyDiags.Warnings() {
		if strings.Contains(d.Summary(), "renamed a policy's key") &&
			strings.Contains(d.Detail(), "allow-docker.rego") &&
			strings.Contains(d.Detail(), "allow_docker") {
			found = true
		}
	}
	if !found {
		t.Errorf("no warning named the allow-docker.rego/allow_docker mismatch, got %+v", applyDiags)
	}

	// And the bundle really was stored under the declared name, not the
	// configured key — confirming the warning is about something real.
	got := api.bundle("config")
	if _, ok := got["allow-docker.rego"]; ok {
		t.Errorf("bundle kept the configured key %q; want it discarded in favor of the declared name", "allow-docker.rego")
	}
	if got["allow_docker"] == "" {
		t.Errorf("bundle has no entry under the declared name %q, got %#v", "allow_docker", got)
	}
}

// TestConfigPolicyBundleNoWarningWhenKeyMatches is the negative case: a
// configuration that already names each policy after its own policy_name
// gets no warning, so the check does not cry wolf on the configuration this
// resource actually recommends.
func TestConfigPolicyBundleNoWarningWhenKeyMatches(t *testing.T) {
	t.Parallel()

	api := newConfigPolicyAPI()
	srv := newConfigPolicyServer(t, api)

	r := &configPolicyBundleResource{
		client: circleci.New(circleci.Config{Host: srv.URL, Token: "tok"}),
	}

	policies, diags := types.MapValue(types.StringType, map[string]attr.Value{
		"allow_docker": types.StringValue("package org\n\npolicy_name[\"allow_docker\"]\n"),
	})
	if diags.HasError() {
		t.Fatalf("building the policies map: %+v", diags)
	}

	plan := configPolicyBundleResourceModel{
		OwnerID:       types.StringValue(testPolicyOwner),
		PolicyContext: types.StringValue("config"),
		Policies:      policies,
	}

	var applyDiags diag.Diagnostics
	if ok := r.upload(context.Background(), plan, "creating", &applyDiags); !ok {
		t.Fatalf("upload failed: %+v", applyDiags)
	}

	if len(applyDiags.Warnings()) != 0 {
		t.Errorf("got warnings for a key that already matches its policy_name: %+v", applyDiags.Warnings())
	}
}

// bundleDiff computes the created/modified/deleted arrays the real upload
// route reports, in terms of the policy_name each entry is keyed by after
// extractPolicyName's renaming — matching the real API's diff, which names
// policies the same way (see SetPolicyBundle's [NET] comment).
func bundleDiff(previous, next map[string]string) map[string][]string {
	created, modified, deleted := []string{}, []string{}, []string{}

	for name, content := range next {
		old, existed := previous[name]
		switch {
		case !existed:
			created = append(created, name)
		case old != content:
			modified = append(modified, name)
		}
	}

	for name := range previous {
		if _, stillThere := next[name]; !stillThere {
			deleted = append(deleted, name)
		}
	}

	sort.Strings(created)
	sort.Strings(modified)
	sort.Strings(deleted)

	return map[string][]string{"created": created, "modified": modified, "deleted": deleted}
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
