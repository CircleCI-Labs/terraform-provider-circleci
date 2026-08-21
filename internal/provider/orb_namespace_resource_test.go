// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"crypto/rand"
	"fmt"
	"net/http"
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

// TestAccOrbNamespaceResource_RenameForbiddenGivesSupportTicketGuidance drives
// a rename through the exact response [NET] shows a real account gets — 403
// Forbidden — and requires the diagnostic to say why retrying will not help,
// rather than reading as a generic, retryable API error.
//
// Without namespaceForbiddenDetail, the diagnostic is only circleci.Detail's
// passthrough of the API body ("Forbidden."), which does not mention a support
// ticket and would fail this test's ExpectError.
func TestAccOrbNamespaceResource_RenameForbiddenGivesSupportTicketGuidance(t *testing.T) {
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
				PreConfig: func() { api.setFailRenameNamespaceStatus(http.StatusForbidden) },
				Config:    config("acme-renamed"),
				ExpectError: regexp.MustCompile(
					`(?s)Forbidden.*support ticket.*support\.circleci\.com`,
				),
			},
		},
	})

	// The rename was attempted — this is not a validator short-circuiting
	// before any request — but it must not have been retried.
	if got := api.requestsFor("POST", "/rename"); len(got) != 1 {
		t.Errorf("rename requests = %d, want exactly 1", len(got))
	}
	// And the namespace itself must be untouched: a 403 from the API, wrapped
	// in a better message, is not license to guess at a different outcome.
	for _, ns := range api.namespaces {
		if ns.Name != "acme" {
			t.Errorf("namespace name = %q, want %q: a failed rename must not change it", ns.Name, "acme")
		}
	}
}

// TestAccOrbNamespaceResource_DeleteForbiddenFailsDestroy drives a destroy
// through the same 403 [NET] shows a real delete gets, and requires
// `terraform destroy` to fail loudly with the support-ticket guidance rather
// than reporting a removal that did not happen.
func TestAccOrbNamespaceResource_DeleteForbiddenFailsDestroy(t *testing.T) {
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
				PreConfig: func() { api.setFailDeleteNamespaceStatus(http.StatusForbidden) },
				Config:    config,
				Destroy:   true,
				ExpectError: regexp.MustCompile(
					`(?s)Forbidden.*support ticket.*support\.circleci\.com`,
				),
			},
			{
				// Confirms the namespace survived the failed destroy above: if
				// Delete had wrongly dropped it from state, this step would plan
				// to recreate it instead of finding nothing to do. Also lets the
				// TestCase's own final destroy succeed.
				PreConfig: func() { api.setFailDeleteNamespaceStatus(0) },
				Config:    config,
				PlanOnly:  true,
			},
		},
	})
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
