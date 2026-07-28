// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const orbTestOrgID = "22222222-2222-2222-2222-222222222222"

// orbCaptureAttr stores an attribute value so a later step can compare against it.
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

// TestAccOrbNamespaceResource covers the create/read/delete cycle and, in the
// second step, that renaming is a genuine in-place update: the rename route is
// called and the namespace keeps its id, rather than the namespace being
// destroyed and recreated.
func TestAccOrbNamespaceResource(t *testing.T) {
	api := newOrbFakeAPI(t)

	var createdID string

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
			{
				Config: config("acme"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("circleci_orb_namespace.test", "name", "acme"),
					resource.TestCheckResourceAttr("circleci_orb_namespace.test", "organization_id", orbTestOrgID),
					resource.TestCheckResourceAttrSet("circleci_orb_namespace.test", "id"),
					orbCaptureAttr("circleci_orb_namespace.test", "id", &createdID),
				),
			},
			{
				Config: config("acme-renamed"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("circleci_orb_namespace.test", "name", "acme-renamed"),
					// The same namespace, renamed: name must not be RequiresReplace.
					orbExpectAttr("circleci_orb_namespace.test", "id", &createdID),
				),
			},
			{
				ResourceName: "circleci_orb_namespace.test",
				ImportState:  true,
				// The organization is part of the import id because the API never
				// reports which organization owns a namespace.
				ImportStateId:     orbTestOrgID + "/acme-renamed",
				ImportStateVerify: true,
			},
		},
	})

	if got := api.requestsFor("POST", "/rename"); len(got) != 1 {
		t.Errorf("rename requests = %d, want 1: renaming must use POST /namespaces/{id}/rename", len(got))
	}
	// A replacement would have deleted the namespace, taking its orbs with it.
	if got := api.requestsFor("DELETE", "/api/v3/namespaces/"); len(got) != 1 {
		t.Errorf("namespace deletes = %d, want 1 (only the final destroy), got %v", len(got), got)
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
