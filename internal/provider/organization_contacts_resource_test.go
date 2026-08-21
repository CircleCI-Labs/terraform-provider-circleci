// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	sdkresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

// fakeOrgContactsAPI is a stateful stand-in for GET/PUT
// /api/private/organization/{orgID}/contacts.
//
// Two things it deliberately does that the real service is known to do (see
// internal/circleci/org_contacts.go, which this fake's shape is checked
// against):
//
//   - It reorders the lists it stores, so a test that only ever sends the
//     same order back would never notice `primary_contacts`/`security_contacts`
//     needing to be sets rather than lists.
//   - It rejects a write with more than 5 addresses in either list with HTTP
//     422, which the real service is expected to as well.
type fakeOrgContactsAPI struct {
	mu       sync.Mutex
	primary  []string
	security []string
	paths    []string
	puts     []map[string]any
	missing  bool
}

func newFakeOrgContactsAPI(t *testing.T) (*httptest.Server, *fakeOrgContactsAPI) {
	t.Helper()

	api := &fakeOrgContactsAPI{primary: []string{}, security: []string{}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()

		api.paths = append(api.paths, r.Method+" "+r.URL.Path)

		if api.missing {
			// [NET, reproduced against the live API on 2026-08-21] GET
			// .../contacts for an organization the caller cannot see (or that
			// does not exist) answers 404 with exactly this message — its own
			// wording already names the ambiguity that GetOrganization's "Org
			// not found." (organization.go) only documents.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"message": "Organization not found, or user does not have access.",
			})

			return
		}

		if r.Method == http.MethodPut {
			var body struct {
				Primary  []string `json:"primary"`
				Security []string `json:"security"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding PUT body: %v", err)
			}

			api.puts = append(api.puts, map[string]any{
				"primary":  append([]string(nil), body.Primary...),
				"security": append([]string(nil), body.Security...),
			})

			if len(body.Primary) > 5 || len(body.Security) > 5 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"too many contacts"}`))

				return
			}

			api.primary = reordered(body.Primary)
			api.security = reordered(body.Security)
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"primary":  api.primary,
			"security": api.security,
		}); err != nil {
			t.Errorf("encoding contacts response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	return srv, api
}

// reordered answers like the real service: the stored order need not match
// the submitted order. Reversing is enough to catch a List where a Set was
// needed.
func reordered(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[len(values)-1-i] = v
	}

	return out
}

// lastPut returns the body of the most recent PUT request.
func (a *fakeOrgContactsAPI) lastPut(t *testing.T) map[string]any {
	t.Helper()

	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.puts) == 0 {
		t.Fatal("the provider sent no PUT request")
	}

	return a.puts[len(a.puts)-1]
}

func (a *fakeOrgContactsAPI) recordedPaths() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]string(nil), a.paths...)
}

// setMissing switches the fake to answer every request with the 404 the real
// contacts route gives whether the organization was deleted or the caller can
// no longer view it.
func (a *fakeOrgContactsAPI) setMissing(missing bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.missing = missing
}

// testOrgContactsOrgID is the organization the fake API answers for. The fake
// ignores the id, so any well-formed UUID works.
const testOrgContactsOrgID = "00000000-1111-2222-3333-444444444444"

func orgContactsConfig(host, orgID string, primary, security []string) string {
	quote := func(values []string) string {
		quoted := make([]string, len(values))
		for i, v := range values {
			quoted[i] = fmt.Sprintf("%q", v)
		}

		return strings.Join(quoted, ", ")
	}

	return fmt.Sprintf(`
provider "circleci" {
  host = %q
  key  = "fake"
}
resource "circleci_organization_contacts" "test" {
  org_id            = %q
  primary_contacts  = [%s]
  security_contacts = [%s]
}
`, host, orgID, quote(primary), quote(security))
}

func TestAccOrganizationContacts_CreateAndRead(t *testing.T) {
	srv, api := newFakeOrgContactsAPI(t)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{{
			Config: orgContactsConfig(
				srv.URL, testOrgContactsOrgID,
				[]string{"a@example.com", "b@example.com"},
				[]string{"c@example.com"},
			),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(
					"circleci_organization_contacts.test", tfjsonpath.New("org_id"),
					knownvalue.StringExact(testOrgContactsOrgID),
				),
				// SetExact, not ListExact: the fake stores these in reverse order (see
				// reordered), so this is what actually proves the attributes are sets.
				statecheck.ExpectKnownValue(
					"circleci_organization_contacts.test", tfjsonpath.New("primary_contacts"),
					knownvalue.SetExact([]knownvalue.Check{
						knownvalue.StringExact("a@example.com"),
						knownvalue.StringExact("b@example.com"),
					}),
				),
				statecheck.ExpectKnownValue(
					"circleci_organization_contacts.test", tfjsonpath.New("security_contacts"),
					knownvalue.SetExact([]knownvalue.Check{
						knownvalue.StringExact("c@example.com"),
					}),
				),
			},
		}},
	})

	got := api.lastPut(t)
	primary, _ := got["primary"].([]string)
	sort.Strings(primary)
	if want := []string{"a@example.com", "b@example.com"}; !equalStrings(primary, want) {
		t.Errorf("PUT body primary = %v, want %v", primary, want)
	}
}

func TestAccOrganizationContacts_NoOpReplanIsEmpty(t *testing.T) {
	// The shape of test the webhook `events` bug needed and originally lacked:
	// applying, then replanning the identical configuration against state the
	// fake deliberately returned in a different order, must show no diff.
	srv, _ := newFakeOrgContactsAPI(t)

	cfg := orgContactsConfig(
		srv.URL, testOrgContactsOrgID,
		[]string{"a@example.com", "b@example.com"},
		[]string{"c@example.com"},
	)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{
			{Config: cfg},
			{
				Config:             cfg,
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})
}

func TestAccOrganizationContacts_UpdateInPlace(t *testing.T) {
	srv, api := newFakeOrgContactsAPI(t)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{
			{
				Config: orgContactsConfig(
					srv.URL, testOrgContactsOrgID,
					[]string{"a@example.com"}, []string{"c@example.com"},
				),
			},
			{
				// Changing either list must be an in-place update, never a
				// replacement: org_id has not changed.
				Config: orgContactsConfig(
					srv.URL, testOrgContactsOrgID,
					[]string{"a@example.com", "b@example.com"}, []string{},
				),
				ConfigPlanChecks: sdkresource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_organization_contacts.test", plancheck.ResourceActionUpdate,
						),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_organization_contacts.test", tfjsonpath.New("security_contacts"),
						knownvalue.SetExact(nil),
					),
				},
			},
		},
	})

	// The second PUT must carry security as an empty array, not omit it and not
	// send null: clearing a list this resource owns is a legitimate write, and
	// the bug history this house is built on (see org_contacts.go) is exactly
	// an attribute that only takes effect on create silently no-oping on
	// update.
	got := api.lastPut(t)
	security, ok := got["security"].([]string)
	if !ok {
		t.Fatalf("PUT body security = %v (%T), want []string", got["security"], got["security"])
	}
	if len(security) != 0 {
		t.Errorf("PUT body security = %v, want empty", security)
	}
}

func TestAccOrganizationContacts_OrgIDRequiresReplace(t *testing.T) {
	srv, _ := newFakeOrgContactsAPI(t)

	other := "55555555-6666-7777-8888-999999999999"

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{
			{
				Config: orgContactsConfig(srv.URL, testOrgContactsOrgID, []string{"a@example.com"}, nil),
			},
			{
				Config: orgContactsConfig(srv.URL, other, []string{"a@example.com"}, nil),
				ConfigPlanChecks: sdkresource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_organization_contacts.test", plancheck.ResourceActionReplace,
						),
					},
				},
			},
		},
	})
}

func TestAccOrganizationContacts_TooManyAddressesRejectedAtPlanTime(t *testing.T) {
	// setvalidator.SizeAtMost(5) must catch this before any request is sent —
	// the API is expected to reject a 6th address with HTTP 422.
	srv, api := newFakeOrgContactsAPI(t)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{{
			Config: orgContactsConfig(srv.URL, testOrgContactsOrgID, []string{
				"a@x.com", "b@x.com", "c@x.com", "d@x.com", "e@x.com", "f@x.com",
			}, nil),
			ExpectError: regexp.MustCompile(`(?s)primary_contacts.*(?i)at most 5`),
		}},
	})

	if paths := api.recordedPaths(); len(paths) != 0 {
		t.Errorf("the provider made requests %v for a config the validator should have rejected, want none", paths)
	}
}

func TestAccOrganizationContacts_RejectsServerDeployment(t *testing.T) {
	srv, api := newFakeOrgContactsAPI(t)

	cfg := fmt.Sprintf(`
provider "circleci" {
  host       = %q
  key        = "fake"
  deployment = "server"
}
resource "circleci_organization_contacts" "test" {
  org_id            = %q
  primary_contacts  = ["a@example.com"]
  security_contacts = []
}
`, srv.URL, testOrgContactsOrgID)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`(?s)circleci_organization_contacts requires CircleCI Cloud`),
		}},
	})

	if paths := api.recordedPaths(); len(paths) != 0 {
		t.Errorf("the provider made requests %v on a server deployment, want none", paths)
	}
}

// TestAccOrganizationContacts_RejectsServerDeploymentAtPlanTime asserts the
// specific thing ModifyPlan exists for: the error must surface from
// `terraform plan` itself, never only from apply. PlanOnly makes this step a
// plan with no apply, so if ModifyPlan's gate were ever removed and only
// Create's remained, this step would stop erroring even though
// TestAccOrganizationContacts_RejectsServerDeployment (a full apply) would
// still catch the regression via Create's own gate.
func TestAccOrganizationContacts_RejectsServerDeploymentAtPlanTime(t *testing.T) {
	srv, api := newFakeOrgContactsAPI(t)

	cfg := fmt.Sprintf(`
provider "circleci" {
  host       = %q
  key        = "fake"
  deployment = "server"
}
resource "circleci_organization_contacts" "test" {
  org_id            = %q
  primary_contacts  = ["a@example.com"]
  security_contacts = []
}
`, srv.URL, testOrgContactsOrgID)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{{
			Config:      cfg,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`(?s)circleci_organization_contacts requires CircleCI Cloud`),
		}},
	})

	if paths := api.recordedPaths(); len(paths) != 0 {
		t.Errorf("the provider made requests %v during plan on a server deployment, want none", paths)
	}
}

func TestAccOrganizationContacts_Import(t *testing.T) {
	srv, _ := newFakeOrgContactsAPI(t)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{
			{
				Config: orgContactsConfig(
					srv.URL, testOrgContactsOrgID, []string{"a@example.com"}, []string{"c@example.com"},
				),
			},
			{
				ResourceName:                         "circleci_organization_contacts.test",
				ImportState:                          true,
				ImportStateId:                        testOrgContactsOrgID,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "org_id",
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("imported %d instances, want 1", len(states))
					}
					if got := states[0].Attributes["org_id"]; got != testOrgContactsOrgID {
						return fmt.Errorf("imported org_id = %q, want %q", got, testOrgContactsOrgID)
					}

					return nil
				},
			},
		},
	})
}

// TestAccOrganizationContacts_DeleteMakesNoAPICall asserts the documented
// destroy behavior: removing the resource stops Terraform from tracking it
// without clearing the organization's lists, unlike
// circleci_audit_log_config's real DELETE. If Delete ever grows an API call,
// this must be updated deliberately rather than by accident.
func TestAccOrganizationContacts_DeleteMakesNoAPICall(t *testing.T) {
	srv, api := newFakeOrgContactsAPI(t)

	cfg := orgContactsConfig(srv.URL, testOrgContactsOrgID, []string{"a@example.com"}, nil)

	sdkresource.UnitTest(t, sdkresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []sdkresource.TestStep{
			{Config: cfg},
			{
				Config: `provider "circleci" {
  host = "` + srv.URL + `"
  key  = "fake"
}
`,
			},
		},
	})

	// Exactly the one PUT from the create step, and nothing else: no DELETE
	// equivalent, no PUT with empty lists sneaked in by Delete.
	paths := api.recordedPaths()
	var puts int
	for _, p := range paths {
		if strings.HasPrefix(p, "PUT ") {
			puts++
		}
	}
	if puts != 1 {
		t.Errorf("recorded PUT requests = %d across %v, want exactly 1 (from create only)", puts, paths)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// organizationContactsSchema builds the resource's schema for direct-method
// tests, the same technique storage_retention_resource_test.go uses.
func organizationContactsSchema(t *testing.T) rschema.Schema {
	t.Helper()

	resp := &fwresource.SchemaResponse{}
	NewOrganizationContactsResource().Schema(t.Context(), fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema method diagnostics: %+v", resp.Diagnostics)
	}

	return resp.Schema
}

func organizationContactsState(
	t *testing.T, schema rschema.Schema, orgID string, primary, security []string,
) tfsdk.State {
	t.Helper()

	ctx := t.Context()

	primarySet, diags := types.SetValueFrom(ctx, types.StringType, primary)
	if diags.HasError() {
		t.Fatalf("building primary set: %+v", diags)
	}
	securitySet, diags := types.SetValueFrom(ctx, types.StringType, security)
	if diags.HasError() {
		t.Fatalf("building security set: %+v", diags)
	}

	model := organizationContactsResourceModel{
		OrgID:    types.StringValue(orgID),
		Primary:  primarySet,
		Security: securitySet,
	}

	state := tfsdk.State{Schema: schema}
	if diags := state.Set(ctx, model); diags.HasError() {
		t.Fatalf("could not build a state value: %+v", diags)
	}

	return state
}

// TestOrganizationContactsResourceUnit_ReadNotFoundWarnsBeforeDroppingState is
// the sibling regression test to
// TestOrganizationSettingsResourceUnit_ReadNotFoundWarnsBeforeDroppingState:
// GetOrganization's 404 ("Org not found.") is the API's documented
// anti-enumeration response, answered identically whether the organization
// was deleted or the caller can simply no longer view it (see
// internal/circleci/organization.go). circleci_organization_contacts's Read
// used to call RemoveResource on that 404 with no diagnostic at all, so a
// token that merely lost view access on the organization would see this
// resource vanish from state with no explanation — exactly the silent drop
// circleci_organization's own Read (and group_resource.go's 403/404
// handling) already knows to warn about instead.
func TestOrganizationContactsResourceUnit_ReadNotFoundWarnsBeforeDroppingState(t *testing.T) {
	srv, api := newFakeOrgContactsAPI(t)
	api.setMissing(true)

	client := circleci.New(circleci.Config{Host: srv.URL, Token: "fake"})
	schema := organizationContactsSchema(t)
	r := &organizationContactsResource{client: client}

	resp := &fwresource.ReadResponse{
		State: organizationContactsState(t, schema, testOrgContactsOrgID, []string{"a@example.com"}, nil),
	}
	r.Read(t.Context(), fwresource.ReadRequest{
		State: organizationContactsState(t, schema, testOrgContactsOrgID, []string{"a@example.com"}, nil),
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Read diagnostics: %+v", resp.Diagnostics)
	}

	if !resp.State.Raw.IsNull() {
		t.Fatalf("Read did not remove the resource from state on a 404")
	}

	if resp.Diagnostics.WarningsCount() == 0 {
		t.Errorf("Read dropped circleci_organization_contacts from state on a 404 with no warning at all. " +
			"The same 404 covers both \"deleted\" and \"caller can no longer view the organization\" " +
			"(see GetOrganization's doc comment): a practitioner deserves a warning naming that " +
			"ambiguity, the same as circleci_organization's own Read already gives, not a silent vanish.")
	}
}
