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

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	sdkresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"terraform-provider-circleci/internal/circleci"
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
	missing bool
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

		if api.missing {
			// [NET, reproduced against the live API on 2026-08-21] GET
			// .../settings for an organization id the service cannot resolve
			// answers 404 with exactly this v3 envelope — the same "Org not
			// found." wording GetOrganization documents as anti-enumeration
			// (organization.go), answered identically whether the organization
			// was actually deleted or the caller simply cannot view it.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{"type": "404", "title": "Org not found."},
			})

			return
		}

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

// setMissing switches the fake to answer every request with the organization
// route's "Org not found." 404, the same response the real service gives
// whether the organization was deleted or the caller can no longer view it.
func (a *fakeOrgSettingsAPI) setMissing(missing bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.missing = missing
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

// organizationSettingsSchema builds the resource's schema for direct-method
// tests, the same technique storage_retention_resource_test.go uses.
func organizationSettingsSchema(t *testing.T) rschema.Schema {
	t.Helper()

	resp := &fwresource.SchemaResponse{}
	NewOrganizationSettingsResource().Schema(t.Context(), fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema method diagnostics: %+v", resp.Diagnostics)
	}

	return resp.Schema
}

func organizationSettingsState(
	t *testing.T, schema rschema.Schema, model organizationSettingsResourceModel,
) tfsdk.State {
	t.Helper()

	state := tfsdk.State{Schema: schema}
	if diags := state.Set(t.Context(), model); diags.HasError() {
		t.Fatalf("could not build a state value: %+v", diags)
	}

	return state
}

// TestOrganizationSettingsResourceUnit_ReadNotFoundWarnsBeforeDroppingState is
// the regression test for a gap found while auditing the organization family:
// GetOrganization's own 404 ("Org not found.") is documented as
// anti-enumeration — the API answers it identically whether the organization
// was actually deleted or the caller can simply no longer view it (see
// internal/circleci/organization.go). circleci_organization's own Read
// already surfaces a warning before calling RemoveResource so a practitioner
// has a chance to notice a permission problem rather than a real deletion.
//
// circleci_organization_settings's Read used to call RemoveResource on that
// same 404 with no diagnostic at all: the resource would vanish from state
// with total silence. For circleci_organization (a real create/destroy
// resource, vcs_type "circleci") that silent drop is dangerous beyond a
// missed notice — the next apply issues a genuine POST /api/v2/organization
// and creates a *second*, distinct standalone organization with the same
// name, since standalone create is not idempotent on name. This resource
// cannot itself create a duplicate organization, but the missing diagnostic
// is the same class of gap, and every other Read in this family already
// warns, so a token that merely lost view access on the organization now
// gets exactly the silent, no-explanation vanish this project has already
// paid for once (see group_resource.go's 403-vs-404 handling for the general
// pattern, and organization_resource.go's Read for the sibling that already
// does this right).
func TestOrganizationSettingsResourceUnit_ReadNotFoundWarnsBeforeDroppingState(t *testing.T) {
	srv, api := newFakeOrgSettingsAPI(t)
	api.setMissing(true)

	client := circleci.New(circleci.Config{Host: srv.URL, Token: "fake"})
	schema := organizationSettingsSchema(t)
	r := &organizationSettingsResource{client: client}

	prior := organizationSettingsResourceModel{
		OrganizationID:    types.StringValue(testOrgSettingsOrgID),
		OrgID:             types.StringValue(testOrgSettingsOrgID),
		EnablePrivateOrbs: types.BoolValue(true),
		EnableAIAgents:    types.BoolNull(),
	}

	resp := &fwresource.ReadResponse{State: organizationSettingsState(t, schema, prior)}
	r.Read(t.Context(), fwresource.ReadRequest{
		State: organizationSettingsState(t, schema, prior),
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Read diagnostics: %+v", resp.Diagnostics)
	}

	if !resp.State.Raw.IsNull() {
		t.Fatalf("Read did not remove the resource from state on a 404")
	}

	if resp.Diagnostics.WarningsCount() == 0 {
		t.Errorf("Read dropped circleci_organization_settings from state on a 404 with no warning at all. " +
			"The same 404 covers both \"deleted\" and \"caller can no longer view the organization\" " +
			"(see GetOrganization's doc comment): a practitioner deserves a warning naming that " +
			"ambiguity, the same as circleci_organization's own Read already gives, not a silent vanish.")
	}
}
