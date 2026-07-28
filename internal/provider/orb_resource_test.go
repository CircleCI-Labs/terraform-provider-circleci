// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
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
