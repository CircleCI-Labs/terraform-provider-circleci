// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"net/http"
	"regexp"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

// orbBuildCategoryID and orbNotifyCategoryID are the fake's fixed categories.
var (
	orbBuildCategoryID  = orbFakeCategories[0].ID
	orbNotifyCategoryID = orbFakeCategories[1].ID
)

// TestAccOrbResource covers creating an orb, the two in-place updates the API
// supports (listed status and category membership), and that destroying it makes
// no delete call, because the API has no route to delete an orb.
func TestAccOrbResource(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")

	config := func(listed bool, categoryID string) string {
		return orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb" "test" {
  namespace_id = %q
  name         = "node"
  is_listed    = %t
  category_ids = [%q]
}
`, ns.ID, listed, categoryID)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(false, orbBuildCategoryID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("circleci_orb.test", "id"),
					// The bare name is what the resource stores; the qualified name
					// is what the API stores and what a config.yml refers to.
					resource.TestCheckResourceAttr("circleci_orb.test", "name", "node"),
					resource.TestCheckResourceAttr("circleci_orb.test", "full_name", "acme/node"),
					resource.TestCheckResourceAttr("circleci_orb.test", "namespace", "acme"),
					resource.TestCheckResourceAttr("circleci_orb.test", "namespace_id", ns.ID),
					resource.TestCheckResourceAttr("circleci_orb.test", "is_private", "false"),
					resource.TestCheckResourceAttr("circleci_orb.test", "is_listed", "false"),
					resource.TestCheckResourceAttr("circleci_orb.test", "created_at", orbFakeCreatedAt),
					resource.TestCheckResourceAttr("circleci_orb.test", "category_ids.#", "1"),
					resource.TestCheckResourceAttr("circleci_orb.test", "categories.#", "1"),
					resource.TestCheckResourceAttr("circleci_orb.test", "categories.0.name", "Build"),
				),
			},
			{
				Config: config(true, orbNotifyCategoryID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("circleci_orb.test", "is_listed", "true"),
					resource.TestCheckResourceAttr("circleci_orb.test", "categories.#", "1"),
					resource.TestCheckResourceAttr("circleci_orb.test", "categories.0.name", "Notifications"),
				),
			},
			{
				Config:            config(true, orbNotifyCategoryID),
				ResourceName:      "circleci_orb.test",
				ImportState:       true,
				ImportStateId:     "acme/node",
				ImportStateVerify: true,
				// category_ids is unmanaged after an import, since the resource
				// cannot tell whether the practitioner wants to manage it.
				ImportStateVerifyIgnore: []string{"category_ids"},
			},
		},
	})

	// Creating with is_listed = false has to unlist after creating, because the
	// create route has no is_listed field.
	if got := api.requestsFor("POST", "/set-listed"); len(got) != 2 {
		t.Errorf("set-listed requests = %d, want 2 (one per step)", len(got))
	}
	if got := api.requestsFor("POST", "/add-category"); len(got) != 2 {
		t.Errorf("add-category requests = %d, want 2", len(got))
	}
	if got := api.requestsFor("POST", "/remove-category"); len(got) != 1 {
		t.Errorf("remove-category requests = %d, want 1", len(got))
	}
	// An orb cannot be deleted, so the destroy at the end of the test must not try.
	if got := api.requestsFor("DELETE", "/orb/packages"); len(got) != 0 {
		t.Errorf("orb deletes = %d, want 0: the API has no delete route for an orb", len(got))
	}
}

// TestAccOrbResource_UnmanagedCategories checks that omitting category_ids leaves
// existing categories alone instead of removing them.
func TestAccOrbResource_UnmanagedCategories(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")
	api.seedOrb("acme/node", ns.ID, orbBuildCategoryID)

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb" "test" {
  namespace_id = %q
  name         = "node"
}
`, ns.ID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:        config,
				ResourceName:  "circleci_orb.test",
				ImportState:   true,
				ImportStateId: "acme/node",
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("imported %d instances, want 1", len(states))
					}
					if got := states[0].Attributes["categories.0.name"]; got != "Build" {
						return fmt.Errorf("categories.0.name = %q, want Build", got)
					}
					if _, set := states[0].Attributes["category_ids.#"]; set {
						return fmt.Errorf("category_ids was populated, want it null so categories stay unmanaged")
					}

					return nil
				},
			},
		},
	})

	if got := api.requestsFor("POST", "/remove-category"); len(got) != 0 {
		t.Errorf("remove-category requests = %d, want 0 when category_ids is unset", len(got))
	}
}

// TestAccOrbResource_CategoriesOrderDoesNotDiff is the regression test for
// issue #5: `categories` is a Computed ListNestedAttribute, and the registry
// gives no ordering guarantee for the categories an orb belongs to.
//
// `categories` has no plan modifier, so the framework's default behaviour for
// an unconfigured Computed attribute carries the value in state forward as the
// PLANNED one whenever some other attribute is what triggers an update. That
// makes an update that leaves category membership untouched — here, flipping
// is_listed — the shape that actually breaks: Update() re-fetches the orb and
// rebuilds `categories` from that fresh response, and if the API answers with
// the same categories in a different order than the one Create last wrote to
// state, the apply's actual result no longer matches what was planned. Terraform
// reports that as "Provider produced inconsistent result after apply" — not a
// mere diff, an error every subsequent plan would keep hitting.
//
// The fake models this deliberately: orbDetail (orb_fake_test.go) renders
// add-category responses in insertion order but a set-listed response (like a
// GET) as a snapshot in the registry's own order, which is what lets an
// is_listed-only change surface a reordering that never touched category_ids.
// Two categories are required — a single-element list cannot show a reordering
// at all, which is exactly the gap that let this ship: every other orb test
// manages only one.
func TestAccOrbResource_CategoriesOrderDoesNotDiff(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")

	config := func(listed bool) string {
		return orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb" "test" {
  namespace_id = %q
  name         = "node"
  is_listed    = %t
  category_ids = [%q, %q]
}
`, ns.ID, listed, orbBuildCategoryID, orbNotifyCategoryID)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_orb.test",
						tfjsonpath.New("categories").AtSliceIndex(0).AtMapKey("name"),
						knownvalue.StringExact("Build")),
					statecheck.ExpectKnownValue("circleci_orb.test",
						tfjsonpath.New("categories").AtSliceIndex(1).AtMapKey("name"),
						knownvalue.StringExact("Notifications")),
				},
			},
			// is_listed flips; category_ids does not. Update() still re-fetches the
			// orb and rebuilds `categories` from whatever order the fake's set-listed
			// response happens to use, which is deliberately not the order Create
			// wrote. Without sorting, this either fails apply outright with
			// "Provider produced inconsistent result after apply" or leaves a
			// permanent diff; with it, both responses collapse to the same order and
			// this step — and the empty replan after it — succeed.
			{
				Config: config(true),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_orb.test",
						tfjsonpath.New("categories").AtSliceIndex(0).AtMapKey("name"),
						knownvalue.StringExact("Build")),
					statecheck.ExpectKnownValue("circleci_orb.test",
						tfjsonpath.New("categories").AtSliceIndex(1).AtMapKey("name"),
						knownvalue.StringExact("Notifications")),
				},
			},
			{
				Config:             config(true),
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})
}

// TestAccOrbResource_RejectsQualifiedName guards the validator: the namespace
// comes from namespace_id, so name must not repeat it.
func TestAccOrbResource_RejectsQualifiedName(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb" "test" {
  namespace_id = %q
  name         = "acme/node"
}
`, ns.ID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`namespace prefix`),
		}},
	})
}

// TestAccOrbResource_UnknownNamespaceFails reports the namespace lookup rather
// than a confusing orb-create failure.
func TestAccOrbResource_UnknownNamespaceFails(t *testing.T) {
	api := newOrbFakeAPI(t)

	config := orbProviderConfig(api.URL()) + `
resource "circleci_orb" "test" {
  namespace_id = "00000000-0000-0000-0000-000000000000"
  name         = "node"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`(?s)orb namespace.*not found`),
		}},
	})
}

// TestAccOrbResource_DriftRecreates drops the orb from state when it is gone.
func TestAccOrbResource_DriftRecreates(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb" "test" {
  namespace_id = %q
  name         = "node"
}
`, ns.ID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					api.mu.Lock()
					defer api.mu.Unlock()

					api.orbs = map[string]*orbFakeOrb{}
				},
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccOrbResource_UnlistedOrbIsStable covers an orb that must stay hidden from
// the registry listing.
//
// The listed status is applied by a separate /set-listed call rather than by the
// create request, so the risk is a redundant call on every subsequent plan. The
// second step asserts the plan is empty and that nothing further was sent.
func TestAccOrbResource_UnlistedOrbIsStable(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("hidden-ns")

	config := orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb" "test" {
  namespace_id = %q
  name         = "private-tools"
  is_listed    = false
}
`, ns.ID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_orb.test",
						tfjsonpath.New("is_listed"), knownvalue.Bool(false)),
					statecheck.ExpectKnownValue("circleci_orb.test",
						tfjsonpath.New("full_name"), knownvalue.StringExact("hidden-ns/private-tools")),
				},
			},
			// An unlisted orb that already matches the configuration is not drift.
			{
				Config:   config,
				PlanOnly: true,
			},
		},
	})

	// Exactly one unlist: at create. The refresh in the second step must not
	// repeat it.
	if got := api.requestsFor("POST", "/set-listed"); len(got) != 1 {
		t.Errorf("set-listed requests = %d, want 1", len(got))
	}
}

// TestAccOrbResource_ListsAnUnlistedOrb exercises the transition from hidden to
// listed, which goes through the separate /set-listed route.
func TestAccOrbResource_ListsAnUnlistedOrb(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("promote-ns")

	config := func(listed bool) string {
		return orbProviderConfig(api.URL()) + fmt.Sprintf(`
resource "circleci_orb" "test" {
  namespace_id = %q
  name         = "promote-me"
  is_listed    = %t
}
`, ns.ID, listed)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_orb.test",
						tfjsonpath.New("is_listed"), knownvalue.Bool(false)),
				},
			},
			{
				Config: config(true),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_orb.test",
						tfjsonpath.New("is_listed"), knownvalue.Bool(true)),
				},
			},
		},
	})

	// Listing an unlisted orb is an in-place update, not a replacement.
	if got := api.requestsFor("POST", "/api/v3/orb/packages"); len(got) != 3 {
		t.Errorf("orb POSTs = %d, want 3 (create plus two set-listed), got %v", len(got), got)
	}
}

// orbResourceSchemaForTest returns the resource's schema, the same way
// budgetResourceSchemaForTest does.
func orbResourceSchemaForTest(t *testing.T) rschema.Schema {
	t.Helper()

	resp := &fwresource.SchemaResponse{}
	(&orbResource{}).Schema(t.Context(), fwresource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}

	return resp.Schema
}

// orbStateForTest builds a tfsdk.State (or, read as a Plan's raw value, the
// identical thing) from a fully-populated model.
func orbStateForTest(t *testing.T, schema rschema.Schema, model orbResourceModel) tfsdk.State {
	t.Helper()

	state := tfsdk.State{Schema: schema}
	if diags := state.Set(t.Context(), model); diags.HasError() {
		t.Fatalf("could not build a state value: %+v", diags)
	}

	return state
}

// TestOrbResourceUnit_CreateWritesStateWhenSetListedFails is a regression test
// for issue #37.
//
// CreateOrbPackage has no is_listed field, so making a new orb unlisted (or,
// as here, keeping a private-looking one listed) takes a second call,
// SetOrbListed, against the orb CreateOrbPackage just made. The API has no
// route to delete an orb (see Delete's own doc comment), so a name a create
// has already claimed cannot be reclaimed by a retry. Before the fix, a
// failure from SetOrbListed returned before resp.State.Set, so an orb
// CircleCI had just created permanently had no record in Terraform state at
// all — the next apply would try to create it again and collide with the
// name it could never delete.
//
// Driven directly through Create, rather than resource.UnitTest, for the same
// reason TestProjectResourceUnit_UpdateRejectsBadSlug is: asserting "still in
// state after a failed apply" is awkward through the plugin-testing
// framework.
func TestOrbResourceUnit_CreateWritesStateWhenSetListedFails(t *testing.T) {
	api := newOrbFakeAPI(t)
	ns := api.seedNamespace("acme")
	api.setFailSetOrbListedStatus(http.StatusInternalServerError)

	schema := orbResourceSchemaForTest(t)
	client := circleci.New(circleci.Config{Host: api.URL(), Token: "fake"})
	r := &orbResource{client: client}

	plan := orbStateForTest(t, schema, orbResourceModel{
		Id:            types.StringUnknown(),
		NamespaceId:   types.StringValue(ns.ID),
		Namespace:     types.StringUnknown(),
		Name:          types.StringValue("node"),
		FullName:      types.StringUnknown(),
		IsPrivate:     types.BoolValue(false),
		IsListed:      types.BoolValue(false),
		CategoryIds:   types.SetNull(types.StringType),
		Categories:    types.ListUnknown(orbCategoryObjectType),
		CreatedAt:     types.StringUnknown(),
		HomeUrl:       types.StringUnknown(),
		LatestVersion: types.StringUnknown(),
	})

	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	r.Create(t.Context(), fwresource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: plan.Raw}}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("Create returned no diagnostics for a failed set-listed call, want one")
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("Create left state empty after CreateOrbPackage succeeded; the orb now exists " +
			"permanently at CircleCI (there is no delete route) with nothing in Terraform tracking it")
	}

	var out orbResourceModel
	if diags := resp.State.Get(t.Context(), &out); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if out.Id.ValueString() == "" {
		t.Error("state id is empty, want the id CreateOrbPackage returned")
	}
	if out.FullName.ValueString() != "acme/node" {
		t.Errorf("state full_name = %q, want acme/node", out.FullName.ValueString())
	}

	// Exact path equality, not requestsFor's substring match: "/api/v3/orb/packages"
	// is also a prefix of the set-listed route, and the client's own transport
	// retries a 500 up to three more times (see httpcl.NewClient's RetryMax),
	// so a substring count would conflate one create with several retried
	// set-listed attempts.
	var creates int
	for _, req := range api.allRequests() {
		if req.Method == "POST" && req.Path == "/api/v3/orb/packages" {
			creates++
		}
	}
	if creates != 1 {
		t.Errorf("orb create requests = %d, want exactly 1: the failure was in set-listed, "+
			"not create, and create must not be retried on that account", creates)
	}
	if setListed := api.requestsFor("POST", "/set-listed"); len(setListed) == 0 {
		t.Error("no set-listed request was recorded, want at least one (the forced failure)")
	}
}
