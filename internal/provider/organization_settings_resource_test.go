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

	sdkresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// orgSettingsToggleNames is every toggle the API serves, used to build a
// complete settings response.
var orgSettingsToggleNames = []string{
	"enable_ai_agents",
	"enable_ai_error_summarization",
	"enable_certified_public_orbs",
	"enable_chunk_ip_ranges",
	"enable_image_brownouts",
	"enable_minor_ai_features",
	"enable_private_orbs",
	"enable_resource_class_brownouts",
	"enable_uncertified_public_orbs",
	"enable_unversioned_config",
	"is_bitbucket_workspace_member_org_member",
	"is_context_group_restriction_required",
	"is_runner_terms_of_service_accepted",
	"is_running_disabled",
	"is_user_checkout_keys_disabled",
}

// fakeOrgSettingsAPI is a stateful stand-in for the v3 organization settings
// endpoints. It holds a value for every toggle, applies partial updates the way
// the real API does, and records the body of each update so a test can assert on
// what the provider actually sent.
type fakeOrgSettingsAPI struct {
	mu      sync.Mutex
	values  map[string]bool
	updates []map[string]any
	paths   []string
}

func newFakeOrgSettingsAPI(t *testing.T) (*httptest.Server, *fakeOrgSettingsAPI) {
	t.Helper()

	api := &fakeOrgSettingsAPI{values: make(map[string]bool, len(orgSettingsToggleNames))}
	for _, name := range orgSettingsToggleNames {
		api.values[name] = false
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()

		api.paths = append(api.paths, r.Method+" "+r.URL.Path)

		if strings.HasSuffix(r.URL.Path, "/update-settings") {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding update body: %v", err)
			}

			api.updates = append(api.updates, body)

			for key, raw := range body {
				value, ok := raw.(bool)
				if !ok {
					t.Errorf("update body %s = %v, want a bool", key, raw)

					continue
				}
				api.values[key] = value
			}
		}

		attributes := make(map[string]bool, len(api.values))
		for key, value := range api.values {
			attributes[key] = value
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"attributes": attributes},
		}); err != nil {
			t.Errorf("encoding settings response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	return srv, api
}

// lastUpdate returns the body of the most recent update-settings request.
func (a *fakeOrgSettingsAPI) lastUpdate(t *testing.T) map[string]any {
	t.Helper()

	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.updates) == 0 {
		t.Fatal("the provider sent no update-settings request")
	}

	return a.updates[len(a.updates)-1]
}

func (a *fakeOrgSettingsAPI) recordedPaths() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]string(nil), a.paths...)
}

// testOrgSettingsOrgID is the organization the fake API answers for. The fake
// ignores the id, so any well-formed UUID works.
const testOrgSettingsOrgID = "00000000-1111-2222-3333-444444444444"

func TestAccOrganizationSettings_OnlySendsConfiguredToggles(t *testing.T) {
	srv, api := newFakeOrgSettingsAPI(t)

	cfg := fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
resource "circleci_organization_settings" "t" {
  organization_id     = %q
  enable_private_orbs = true
  is_running_disabled = false
}
`, srv.URL, testOrgSettingsOrgID)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{{
			Config: cfg,
			Check: sdkresource.ComposeAggregateTestCheckFunc(
				sdkresource.TestCheckResourceAttr("circleci_organization_settings.t", "organization_id", testOrgSettingsOrgID),
				sdkresource.TestCheckResourceAttr("circleci_organization_settings.t", "enable_private_orbs", "true"),
				sdkresource.TestCheckResourceAttr("circleci_organization_settings.t", "is_running_disabled", "false"),
				// The toggles the configuration says nothing about must stay
				// absent from state. If they were Optional+Computed they would be
				// populated here, and the provider would start writing them.
				sdkresource.TestCheckNoResourceAttr("circleci_organization_settings.t", "enable_ai_agents"),
				sdkresource.TestCheckNoResourceAttr("circleci_organization_settings.t", "is_user_checkout_keys_disabled"),
				sdkresource.TestCheckNoResourceAttr("circleci_organization_settings.t", "is_runner_terms_of_service_accepted"),
			),
		}},
	})

	// The route must be the v3 verb route, and the read must precede the write.
	paths := api.recordedPaths()
	wantRead := "GET /api/v3/orgs/" + testOrgSettingsOrgID + "/settings"
	wantWrite := "POST /api/v3/orgs/" + testOrgSettingsOrgID + "/update-settings"
	if len(paths) == 0 {
		t.Fatal("the provider made no requests")
	}
	if paths[0] != wantRead {
		t.Errorf("first request = %q, want %q", paths[0], wantRead)
	}

	var sawWrite bool
	for _, path := range paths {
		if path == wantWrite {
			sawWrite = true
		}
	}
	if !sawWrite {
		t.Errorf("recorded requests = %v, want one to be %q", paths, wantWrite)
	}

	// And the payload must carry only the two configured toggles: sending false
	// for the rest would switch off settings nobody asked the provider to manage.
	body := api.lastUpdate(t)
	want := map[string]any{"enable_private_orbs": true, "is_running_disabled": false}
	if len(body) != len(want) {
		t.Errorf("update payload = %v, want exactly %v", body, want)
	}
	for key, value := range want {
		if body[key] != value {
			t.Errorf("update payload %s = %v, want %v", key, body[key], value)
		}
	}
}

func TestAccOrganizationSettings_UpdateInPlace(t *testing.T) {
	srv, api := newFakeOrgSettingsAPI(t)

	config := func(privateOrbs bool) string {
		return fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
resource "circleci_organization_settings" "t" {
  organization_id     = %q
  enable_private_orbs = %t
}
`, srv.URL, testOrgSettingsOrgID, privateOrbs)
	}

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{
			{
				Config: config(true),
				Check: sdkresource.TestCheckResourceAttr(
					"circleci_organization_settings.t", "enable_private_orbs", "true"),
			},
			{
				// Settings are updatable, so this must be an in-place update
				// rather than a replacement.
				Config: config(false),
				Check: sdkresource.TestCheckResourceAttr(
					"circleci_organization_settings.t", "enable_private_orbs", "false"),
			},
		},
	})

	body := api.lastUpdate(t)
	if body["enable_private_orbs"] != false {
		t.Errorf("final update payload = %v, want enable_private_orbs false", body)
	}
	if len(body) != 1 {
		t.Errorf("final update payload = %v, want only the one configured toggle", body)
	}
}

func TestAccOrganizationSettings_OrganizationIDRequiresReplace(t *testing.T) {
	srv, _ := newFakeOrgSettingsAPI(t)

	config := func(orgID string) string {
		return fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
resource "circleci_organization_settings" "t" {
  organization_id     = %q
  enable_private_orbs = true
}
`, srv.URL, orgID)
	}

	other := "55555555-6666-7777-8888-999999999999"

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{
			{Config: config(testOrgSettingsOrgID)},
			{
				Config: config(other),
				Check: sdkresource.TestCheckResourceAttr(
					"circleci_organization_settings.t", "organization_id", other),
			},
		},
	})
}

func TestAccOrganizationSettings_Import(t *testing.T) {
	srv, _ := newFakeOrgSettingsAPI(t)

	cfg := fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
resource "circleci_organization_settings" "t" {
  organization_id     = %q
  enable_private_orbs = true
}
`, srv.URL, testOrgSettingsOrgID)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{
			{Config: cfg},
			{
				ResourceName:                         "circleci_organization_settings.t",
				ImportState:                          true,
				ImportStateId:                        testOrgSettingsOrgID,
				ImportStateVerifyIdentifierAttribute: "organization_id",
				// Import leaves the toggles null on purpose, so the imported
				// state is not expected to match the applied state attribute for
				// attribute.
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("imported %d instances, want 1", len(states))
					}
					if got := states[0].Attributes["organization_id"]; got != testOrgSettingsOrgID {
						return fmt.Errorf("imported organization_id = %q, want %q", got, testOrgSettingsOrgID)
					}

					return nil
				},
			},
		},
	})
}

// TestAccOrganizationSettings_RejectsServerDeployment is the important guard:
// CircleCI Server does not route /api/v3, so this resource must say so plainly
// instead of letting the request fail as an opaque 404.
func TestAccOrganizationSettings_RejectsServerDeployment(t *testing.T) {
	srv, api := newFakeOrgSettingsAPI(t)

	cfg := fmt.Sprintf(`
provider "circleci" {
  host       = %q
  key        = "fake"
  deployment = "server"
}
resource "circleci_organization_settings" "t" {
  organization_id     = %q
  enable_private_orbs = true
}
`, srv.URL, testOrgSettingsOrgID)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`(?s)circleci_organization_settings requires CircleCI Cloud`),
		}},
	})

	if paths := api.recordedPaths(); len(paths) != 0 {
		t.Errorf("the provider made requests %v on a server deployment, want none", paths)
	}
}

func TestAccOrganizationSettingsDataSource_RejectsServerDeployment(t *testing.T) {
	srv, _ := newFakeOrgSettingsAPI(t)

	cfg := fmt.Sprintf(`
provider "circleci" {
  host       = %q
  key        = "fake"
  deployment = "server"
}
data "circleci_organization_settings" "t" {
  organization_id = %q
}
`, srv.URL, testOrgSettingsOrgID)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`(?s)circleci_organization_settings requires CircleCI Cloud`),
		}},
	})
}

func TestAccOrganizationSettingsDataSource_ReadsEveryToggle(t *testing.T) {
	srv, api := newFakeOrgSettingsAPI(t)

	api.mu.Lock()
	api.values["enable_private_orbs"] = true
	api.values["is_runner_terms_of_service_accepted"] = true
	api.mu.Unlock()

	cfg := fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
data "circleci_organization_settings" "t" {
  organization_id = %q
}
`, srv.URL, testOrgSettingsOrgID)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{{
			Config: cfg,
			Check: sdkresource.ComposeAggregateTestCheckFunc(
				// A read-only view reports every toggle, including the ones the
				// resource would leave null.
				sdkresource.TestCheckResourceAttr("data.circleci_organization_settings.t", "enable_private_orbs", "true"),
				sdkresource.TestCheckResourceAttr("data.circleci_organization_settings.t", "is_runner_terms_of_service_accepted", "true"),
				sdkresource.TestCheckResourceAttr("data.circleci_organization_settings.t", "enable_ai_agents", "false"),
				sdkresource.TestCheckResourceAttr("data.circleci_organization_settings.t", "is_user_checkout_keys_disabled", "false"),
			),
		}},
	})

	paths := api.recordedPaths()
	want := "GET /api/v3/orgs/" + testOrgSettingsOrgID + "/settings"
	for _, got := range paths {
		if got != want {
			t.Errorf("request = %q, want %q: a data source must never write", got, want)
		}
	}
}
