// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"terraform-provider-circleci/internal/circleci"
)

const orbTestOrgID = "22222222-2222-2222-2222-222222222222"

// orbCaptureAttr stores an attribute value so a later step can compare against
// it. Kept here for other resources in this package that share the "orb"
// helper prefix (see notification_channel_config_resource_test.go); this file
// no longer needs it now that renaming is blocked before it can leave the id
// unchanged for a later step to check.
func orbCaptureAttr(address, key string, into *string) resource.TestCheckFunc {
	return func(state *terraform.State) error {
		res, ok := state.RootModule().Resources[address]
		if !ok {
			return fmt.Errorf("resource %s not found in state", address)
		}
		*into = res.Primary.Attributes[key]

		return nil
	}
}

// orbExpectAttr asserts an attribute still holds a previously captured value.
// See orbCaptureAttr.
func orbExpectAttr(address, key string, want *string) resource.TestCheckFunc {
	return func(state *terraform.State) error {
		res, ok := state.RootModule().Resources[address]
		if !ok {
			return fmt.Errorf("resource %s not found in state", address)
		}
		if got := res.Primary.Attributes[key]; got != *want {
			return fmt.Errorf("%s.%s = %q, want %q", address, key, got, *want)
		}

		return nil
	}
}

// orbNamespaceSchema returns the resource's schema, for building tfsdk.State
// values directly in the unit tests below that call Delete without going
// through resource.UnitTest.
func orbNamespaceSchema(t *testing.T) rschema.Schema {
	t.Helper()

	resp := &fwresource.SchemaResponse{}
	NewOrbNamespaceResource().Schema(t.Context(), fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema method diagnostics: %+v", resp.Diagnostics)
	}

	return resp.Schema
}

// orbNamespaceState builds a state value holding model.
func orbNamespaceState(t *testing.T, schema rschema.Schema, model orbNamespaceResourceModel) tfsdk.State {
	t.Helper()

	state := tfsdk.State{Schema: schema}
	if diags := state.Set(t.Context(), model); diags.HasError() {
		t.Fatalf("could not build a state value: %+v", diags)
	}

	return state
}

// TestAccOrbNamespaceResource_CreateReadImport covers the create/read cycle
// and importing by name, then lets the TestCase's own final destroy run.
// Renaming is exercised separately below, because it is now blocked before
// Update is ever reached rather than being a genuine in-place update.
func TestAccOrbNamespaceResource_CreateReadImport(t *testing.T) {
	api := newOrbFakeAPI(t)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb_namespace" "test" {
  name            = "acme"
  organization_id = %q
}
`, orbTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("circleci_orb_namespace.test", "name", "acme"),
					resource.TestCheckResourceAttr("circleci_orb_namespace.test", "organization_id", orbTestOrgID),
					resource.TestCheckResourceAttrSet("circleci_orb_namespace.test", "id"),
				),
			},
			{
				ResourceName: "circleci_orb_namespace.test",
				ImportState:  true,
				// The organization is part of the import id because the API never
				// reports which organization owns a namespace.
				ImportStateId:     orbTestOrgID + "/acme",
				ImportStateVerify: true,
			},
		},
	})

	// The TestCase's own final destroy must not have called the delete route
	// at all — see TestAccOrbNamespaceResource_DestroyLeavesNamespaceInPlace,
	// which asserts that directly. This only guards against the create/read/
	// import cycle above making an unexpected rename or delete call of its own.
	if got := api.requestsFor("POST", "/rename"); len(got) != 0 {
		t.Errorf("rename requests = %d, want 0: nothing in this test renames the namespace", len(got))
	}
}

// TestAccOrbNamespaceResource_ImportByID accepts a namespace UUID in place of the
// name, since either identifies the namespace.
func TestAccOrbNamespaceResource_ImportByID(t *testing.T) {
	api := newOrbFakeAPI(t)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb_namespace" "test" {
  name            = "acme"
  organization_id = %q
}
`, orbTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				Config:       config,
				ResourceName: "circleci_orb_namespace.test",
				ImportState:  true,
				ImportStateIdFunc: func(state *terraform.State) (string, error) {
					res, ok := state.RootModule().Resources["circleci_orb_namespace.test"]
					if !ok {
						return "", fmt.Errorf("resource not found in state")
					}

					return orbTestOrgID + "/" + res.Primary.Attributes["id"], nil
				},
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccOrbNamespaceResource_ImportWithoutOrganization must fail loudly rather
// than importing a namespace whose organization_id is null, because the next plan
// would then want to replace it and a replacement deletes every orb in it.
func TestAccOrbNamespaceResource_ImportWithoutOrganization(t *testing.T) {
	api := newOrbFakeAPI(t)
	api.seedNamespace("acme")

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb_namespace" "test" {
  name            = "acme"
  organization_id = %q
}
`, orbTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:        config,
				ResourceName:  "circleci_orb_namespace.test",
				ImportState:   true,
				ImportStateId: "acme",
				ExpectError:   regexp.MustCompile(`(?s)Invalid import ID.*organization_id`),
			},
		},
	})
}

// TestAccOrbNamespaceResource_RejectsQualifiedName guards the validator: a
// namespace name is the prefix of "<namespace>/<orb>", never a path.
func TestAccOrbNamespaceResource_RejectsQualifiedName(t *testing.T) {
	api := newOrbFakeAPI(t)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb_namespace" "test" {
  name            = "acme/node"
  organization_id = %q
}
`, orbTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`slash`),
		}},
	})
}

// TestAccOrbNamespaceResource_DriftRecreates drops the resource from state when
// the namespace is gone, rather than failing the refresh.
func TestAccOrbNamespaceResource_DriftRecreates(t *testing.T) {
	api := newOrbFakeAPI(t)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb_namespace" "test" {
  name            = "acme"
  organization_id = %q
}
`, orbTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					api.mu.Lock()
					defer api.mu.Unlock()

					// Deleted outside Terraform.
					api.namespaces = map[string]*orbFakeNamespace{}
				},
				Config:             config,
				ExpectNonEmptyPlan: true,
				PlanOnly:           true,
			},
		},
	})
}

// TestOrbNamespaceNameImmutable_PlanModifyString unit-tests the plan modifier
// in isolation: it must error only when a prior name exists and the planned
// value genuinely differs from it, and must not error on Create (null state),
// on an unresolved value (unknown plan), or when nothing actually changed.
func TestOrbNamespaceNameImmutable_PlanModifyString(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		stateValue types.String
		planValue  types.String
		wantError  bool
	}{
		{"create has no prior state", types.StringNull(), types.StringValue("acme"), false},
		{"unchanged name", types.StringValue("acme"), types.StringValue("acme"), false},
		{"plan value not yet known", types.StringValue("acme"), types.StringUnknown(), false},
		{"genuine rename", types.StringValue("acme"), types.StringValue("acme-two"), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := planmodifier.StringRequest{
				StateValue: tc.stateValue,
				PlanValue:  tc.planValue,
			}
			resp := &planmodifier.StringResponse{PlanValue: tc.planValue}

			orbNamespaceNameImmutable{}.PlanModifyString(t.Context(), req, resp)

			if got := resp.Diagnostics.HasError(); got != tc.wantError {
				t.Errorf("HasError = %v, want %v (diagnostics: %+v)", got, tc.wantError, resp.Diagnostics)
			}
		})
	}
}

// TestAccOrbNamespaceResource_RenameBlockedAtPlanTime proves the rename is
// refused before Terraform ever reaches Update, not merely that
// orbNamespaceNameImmutable itself returns an error when called directly
// (TestOrbNamespaceNameImmutable_PlanModifyString covers that in isolation).
// Asserting zero rename requests reached the fake is what a RequiresReplace
// plan modifier or an apply-time-only check would not guarantee: either of
// those would still let the plan proceed to apply, and RequiresReplace would
// additionally try to destroy the namespace first.
func TestAccOrbNamespaceResource_RenameBlockedAtPlanTime(t *testing.T) {
	api := newOrbFakeAPI(t)

	config := func(name string) string {
		return orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb_namespace" "test" {
  name            = %q
  organization_id = %q
}
`, name, orbTestOrgID)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config("acme")},
			{
				Config: config("acme-renamed"),
				// The diagnostic has to say the claim is permanent, global and
				// one-per-organization, and point at a support ticket rather than
				// reading as a generic, retryable validation error.
				ExpectError: regexp.MustCompile(
					`(?s)cannot be changed.*permanent.*global.*one-per-organization.*support ticket`,
				),
			},
		},
	})

	if got := api.requestsFor("POST", "/rename"); len(got) != 0 {
		t.Errorf("rename requests = %d, want 0: a plan-time-blocked rename must never reach the API", len(got))
	}
	for _, ns := range api.namespaces {
		if ns.Name != "acme" {
			t.Errorf("namespace name = %q, want %q: a blocked plan must not change it", ns.Name, "acme")
		}
	}
}

// TestOrbNamespaceResourceUnit_DeleteWarnsAndDoesNotCallAPI asserts the
// documented destroy behavior directly against Delete, the same way
// TestStorageRetentionResourceUnit_DeleteMakesNoAPICallAndWarns does for
// circleci_storage_retention: removing the resource from Terraform state
// must not reset anything in CircleCI (there is no route that would let it),
// and the warning must actually be present — not just the absence of an
// error — naming the namespace and pointing at a support ticket.
//
// The client is pointed at an address nothing listens on, deliberately: if
// Delete is ever changed to call the API again, this test fails with a
// connection error rather than silently passing.
func TestOrbNamespaceResourceUnit_DeleteWarnsAndDoesNotCallAPI(t *testing.T) {
	t.Parallel()

	client := circleci.New(circleci.Config{Host: "http://127.0.0.1:1", Token: "fake"})
	r := &orbNamespaceResource{client: client}

	schema := orbNamespaceSchema(t)
	model := orbNamespaceResourceModel{
		Id:             types.StringValue("11111111-1111-1111-1111-111111111111"),
		Name:           types.StringValue("acme"),
		OrganizationId: types.StringValue(orbTestOrgID),
		OrgId:          types.StringValue(orbTestOrgID),
	}

	resp := &fwresource.DeleteResponse{}
	r.Delete(t.Context(), fwresource.DeleteRequest{
		State: orbNamespaceState(t, schema, model),
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Delete diagnostics: %+v", resp.Diagnostics)
	}

	if resp.Diagnostics.WarningsCount() != 1 {
		t.Fatalf("expected exactly 1 warning, got %+v", resp.Diagnostics)
	}

	detail := resp.Diagnostics.Warnings()[0].Detail()
	for _, want := range []string{"acme", "11111111-1111-1111-1111-111111111111", "support ticket", "not self-service"} {
		if !strings.Contains(detail, want) {
			t.Errorf("Delete warning does not mention %q: %s", want, detail)
		}
	}
}

// TestAccOrbNamespaceResource_DestroyLeavesNamespaceInPlace drives a destroy
// through the full plan/apply pipeline (unlike the Delete-only unit test
// above, which never touches ModifyPlan, Terraform's diffing, or state
// removal) and asserts the namespace survives it: destroy must succeed
// without ever calling DELETE, and the fake must still hold the namespace
// afterward.
func TestAccOrbNamespaceResource_DestroyLeavesNamespaceInPlace(t *testing.T) {
	api := newOrbFakeAPI(t)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb_namespace" "test" {
  name            = "acme"
  organization_id = %q
}
`, orbTestOrgID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				Config:  config,
				Destroy: true,
			},
		},
	})

	if got := api.requestsFor("DELETE", "/api/v3/namespaces/"); len(got) != 0 {
		t.Errorf("namespace deletes = %d, want 0: destroy must not call the API", len(got))
	}
	if len(api.namespaces) != 1 {
		t.Fatalf("namespaces remaining = %d, want 1: the namespace must survive `terraform destroy`",
			len(api.namespaces))
	}
	for _, ns := range api.namespaces {
		if ns.Name != "acme" {
			t.Errorf("surviving namespace name = %q, want %q", ns.Name, "acme")
		}
	}
}

// TestAccOrbNamespaceResource_SecondCreateNamesTheExistingNamespace is [NET]:
// it runs against a real account (skips otherwise, via testAccPreCheck and
// testOrgID/testOrgName) rather than the fake, because this is exactly the
// diagnostic the task called out as mattering most — a practitioner who
// fat-fingers a second `circleci_orb_namespace` for an organization that
// already has one cannot undo whatever happens next, so the error had better
// name the namespace that already exists rather than reading as a generic
// failure.
//
// The primary test organization for the active integration is known, from
// manual investigation during this task, to already own a namespace named
// after the organization itself; every disposable fixture organization
// available did. If that ever stops being true for a given integration, this
// test fails clearly (no error at all, since the create would then succeed)
// rather than silently passing on a namespace it just leaked.
func TestAccOrbNamespaceResource_SecondCreateNamesTheExistingNamespace(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "circleci_orb_namespace" "test" {
  name            = %q
  organization_id = %q
}
`, "tfp-second-create-"+rand.Text()[:12], testOrgID(t)),
			ExpectError: regexp.MustCompile(
				`(?s)only create one namespace.*` + regexp.QuoteMeta(testOrgName(t)),
			),
		}},
	})
}

// TestAccOrbNamespaceResource_NameAlreadyClaimedByAnotherOrg is [NET], for the
// same reason as TestAccOrbNamespaceResource_SecondCreateNamesTheExistingNamespace:
// the other undoable mistake this resource can make is claiming a name a
// different organization already owns, and the diagnostic needs to say so
// plainly.
//
// It needs a target organization with no namespace of its own — every
// pre-provisioned fixture this investigation found already owns one, which is
// what made TestAccOrbNamespaceResource_SecondCreateNamesTheExistingNamespace
// possible in the first place — so it creates one, then asks that fresh
// organization for the name testOrgName's organization already owns. Both
// resources are in the same config so a single apply exercises the conflict
// and the TestCase's automatic destroy cleans up the organization afterward.
func TestAccOrbNamespaceResource_NameAlreadyClaimedByAnotherOrg(t *testing.T) {
	claimedName := testOrgName(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "circleci_organization" "fresh" {
  name     = %q
  vcs_type = "circleci"
}

resource "circleci_orb_namespace" "test" {
  name            = %q
  organization_id = circleci_organization.fresh.id
}
`, "tfp-orb-namespace-conflict-"+rand.Text()[:12], claimedName),
			// The measured message names the rejected name before saying why:
			// `Cannot create namespace 'X': a namespace with that name already
			// exists.` — the quoted name has to come first in the pattern too.
			// Terraform's own error rendering can wrap that message onto a new
			// line between "already" and "exists", hence \s+ rather than a
			// literal space.
			ExpectError: regexp.MustCompile(
				`(?s)` + regexp.QuoteMeta(claimedName) + `.*already\s+exists`,
			),
		}},
	})
}
