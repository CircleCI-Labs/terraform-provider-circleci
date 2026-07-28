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

// These are fake-server-backed unit tests for context_resource.go: they need no
// TF_ACC and no CircleCI credentials, unlike TestAccContextResource in
// context_resource_test.go.

// contextResourceUnitConfig builds a circleci_context config. Every call site
// in this package names the context "build"; that is fixed here rather than
// threaded through as a parameter that would never vary.
func contextResourceUnitConfig(host, orgID string) string {
	return legacyContextProviderConfig(host) + fmt.Sprintf(`
resource "circleci_context" "test" {
  organization_id = %[1]q
  name            = "build"
}
`, orgID)
}

const contextUnitOrgID = "org-11111111-1111-1111-1111-111111111111"

func TestContextResourceUnit_CRUD(t *testing.T) {
	api, host := newContextLegacyAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextResourceUnitConfig(host, contextUnitOrgID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_context.test", tfjsonpath.New("name"), knownvalue.StringExact("build"),
					),
					statecheck.ExpectKnownValue(
						"circleci_context.test", tfjsonpath.New("organization_id"), knownvalue.StringExact(contextUnitOrgID),
					),
					statecheck.ExpectKnownValue(
						"circleci_context.test", tfjsonpath.New("created_at"), knownvalue.StringExact("2024-01-02T03:04:05.000Z"),
					),
					statecheck.ExpectKnownValue(
						"circleci_context.test", tfjsonpath.New("id"), knownvalue.StringExact("ctx-1"),
					),
				},
			},
		},
	})

	var sawCreate, sawRead, sawDelete bool
	for _, req := range api.recorded() {
		switch req {
		case "POST /api/v2/context":
			sawCreate = true
		case "GET /api/v2/context/ctx-1":
			sawRead = true
		case "DELETE /api/v2/context/ctx-1":
			sawDelete = true
		}
	}
	if !sawCreate {
		t.Errorf("no create request with the expected URI, got %q", api.recorded())
	}
	if !sawRead {
		t.Errorf("no read request with the expected URI, got %q", api.recorded())
	}
	if !sawDelete {
		t.Errorf("no delete request with the expected URI, got %q", api.recorded())
	}
}

func TestContextResourceUnit_Import(t *testing.T) {
	_, host := newContextLegacyAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextResourceUnitConfig(host, contextUnitOrgID),
			},
			{
				ResourceName:      "circleci_context.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs := s.RootModule().Resources["circleci_context.test"]

					return fmt.Sprintf("%s/%s", rs.Primary.Attributes["organization_id"], rs.Primary.Attributes["id"]), nil
				},
			},
		},
	})
}

func TestContextResourceUnit_ImportInvalidID(t *testing.T) {
	_, host := newContextLegacyAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextResourceUnitConfig(host, contextUnitOrgID),
			},
			{
				ResourceName:  "circleci_context.test",
				ImportState:   true,
				ImportStateId: "not-a-valid-composite-id",
				ExpectError:   regexp.MustCompile(`Invalid Import ID Format`),
			},
		},
	})
}

// TestContextResourceUnit_RemovedOutsideTerraform documents a production bug
// rather than fixing it (source is owned elsewhere): the established provider
// pattern (see TestAccCheckoutKeyResource_RemovedOutsideTerraform) is that a
// 404 on read drops the resource from state so the next plan recreates it.
// context_resource.go does not follow that pattern. Its Read (around lines
// 128-157) only special-cases a nil *Context with a nil error to call
// RemoveResource — but ccicontext.ContextService.Get never returns that
// combination; on a non-2xx response it returns a nil *Context AND a non-nil
// error, so Read always takes the `err != nil` branch and reports a hard
// "Unable to Read CircleCI context" error instead of removing the resource
// from state. A context deleted outside Terraform therefore breaks every
// subsequent plan/refresh until a practitioner manually removes it from
// state, rather than being transparently recreated.
func TestContextResourceUnit_RemovedOutsideTerraform(t *testing.T) {
	api, host := newContextLegacyAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextResourceUnitConfig(host, contextUnitOrgID),
			},
			{
				PreConfig:   func() { api.setMissing("ctx-1", true) },
				Config:      contextResourceUnitConfig(host, contextUnitOrgID),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)Unable to Read CircleCI context with id ctx-1.*404 Not Found`),
			},
			{
				// Restore the context so the framework's destroy step succeeds.
				PreConfig: func() { api.setMissing("ctx-1", false) },
				Config:    contextResourceUnitConfig(host, contextUnitOrgID),
			},
		},
	})
}

// TestContextResourceUnit_CreateAPIError proves a 4xx from the API surfaces as a
// Terraform diagnostic rather than a panic.
func TestContextResourceUnit_CreateAPIError(t *testing.T) {
	api, host := newContextLegacyAPI(t)
	api.fail(400, "Invalid owner type - only organization is supported at present")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      contextResourceUnitConfig(host, contextUnitOrgID),
			ExpectError: regexp.MustCompile(`(?s)Error creating CircleCI context.*only organization is supported`),
		}},
	})
}

// TestContextResourceUnit_OrganizationIDChangeIsInconsistent documents a
// production bug rather than fixing it (source is owned elsewhere): the
// "organization_id" attribute in context_resource.go has no
// stringplanmodifier.RequiresReplace, unlike "name", so changing it in
// configuration plans an in-place Update rather than a replace. But Update()
// (context_resource.go, around line 176-177) is a complete no-op — it never
// calls resp.State.Set — so the framework's default behavior leaves the prior
// state in place (see terraform-plugin-framework's server_updateresource.go,
// which seeds UpdateResponse.State from req.PriorState). The result is that
// core detects the applied state does not match the planned state and fails
// with "Provider produced inconsistent result after apply" instead of either
// updating the resource or forcing a replacement.
func TestContextResourceUnit_OrganizationIDChangeIsInconsistent(t *testing.T) {
	_, host := newContextLegacyAPI(t)

	otherOrgID := "org-22222222-2222-2222-2222-222222222222"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextResourceUnitConfig(host, contextUnitOrgID),
			},
			{
				Config:      contextResourceUnitConfig(host, otherOrgID),
				ExpectError: regexp.MustCompile(`(?s)Provider produced inconsistent result after apply.*organization_id`),
			},
		},
	})
}
