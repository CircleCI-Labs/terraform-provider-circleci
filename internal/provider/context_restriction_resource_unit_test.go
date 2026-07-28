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

// These are fake-server-backed unit tests for context_restriction_resource.go:
// they need no TF_ACC and no CircleCI credentials, unlike
// TestAccContextRestrictionResource in context_restriction_resource_test.go.

const contextRestrictionUnitContextID = "ctx-fixed-1"

// contextRestrictionResourceUnitConfig builds a circleci_context_restriction
// config. Every call site in this package points context_id at
// contextRestrictionUnitContextID; that is fixed here rather than threaded
// through as a parameter that would never vary.
func contextRestrictionResourceUnitConfig(host, restrictionType, value string) string {
	return legacyContextProviderConfig(host) + fmt.Sprintf(`
resource "circleci_context_restriction" "test" {
  context_id = %[1]q
  type       = %[2]q
  value      = %[3]q
}
`, contextRestrictionUnitContextID, restrictionType, value)
}

// seedFixedContext seeds a context whose id is stable across test runs, so
// restriction tests do not depend on the create-context flow.
func seedFixedContext(api *contextLegacyAPI) {
	api.seedContext(contextRestrictionUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")
}

func TestContextRestrictionResourceUnit_ProjectType(t *testing.T) {
	api, host := newContextLegacyAPI(t)
	seedFixedContext(api)

	projectID := "33333333-3333-3333-3333-333333333333"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextRestrictionResourceUnitConfig(host, "project", projectID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_context_restriction.test", tfjsonpath.New("type"), knownvalue.StringExact("project")),
					statecheck.ExpectKnownValue("circleci_context_restriction.test", tfjsonpath.New("value"), knownvalue.StringExact(projectID)),
					statecheck.ExpectKnownValue("circleci_context_restriction.test", tfjsonpath.New("project_id"), knownvalue.StringExact(projectID)),
					statecheck.ExpectKnownValue("circleci_context_restriction.test", tfjsonpath.New("context_id"), knownvalue.StringExact(contextRestrictionUnitContextID)),
					// BUG (context_restriction_resource.go ~line 137-139): the create
					// response never carries a restriction's name (the API's
					// context_restriction_post.go response struct has no "name" field),
					// so immediately after create the state always has name = "".
					statecheck.ExpectKnownValue("circleci_context_restriction.test", tfjsonpath.New("name"), knownvalue.StringExact("")),
				},
			},
			{
				// A subsequent refresh calls GetRestrictions (the list route), which
				// does report the name, so the SAME restriction now reads back with a
				// different "name" than the create step produced. Terraform allows
				// this silently because "name" is Computed with no
				// UseStateForUnknown/consistency requirement, but it means a
				// practitioner who reads "name" right after create/apply will always
				// see "" until the next refresh.
				PreConfig: func() {
					// Give the restriction its real name out of band, as the API does.
					api.mu.Lock()
					for _, r := range api.contexts[contextRestrictionUnitContextID].restrictions {
						r.name = "acme/api"
					}
					api.mu.Unlock()
				},
				Config: contextRestrictionResourceUnitConfig(host, "project", projectID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_context_restriction.test", tfjsonpath.New("name"), knownvalue.StringExact("acme/api")),
				},
			},
		},
	})
}

func TestContextRestrictionResourceUnit_ExpressionType(t *testing.T) {
	api, host := newContextLegacyAPI(t)
	seedFixedContext(api)

	expr := `pipeline.git.branch == "main"`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextRestrictionResourceUnitConfig(host, "expression", expr),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_context_restriction.test", tfjsonpath.New("type"), knownvalue.StringExact("expression")),
					statecheck.ExpectKnownValue("circleci_context_restriction.test", tfjsonpath.New("value"), knownvalue.StringExact(expr)),
					// project_id is empty for a non-project restriction, never a copy
					// of restriction_value.
					statecheck.ExpectKnownValue("circleci_context_restriction.test", tfjsonpath.New("project_id"), knownvalue.StringExact("")),
				},
			},
		},
	})
}

func TestContextRestrictionResourceUnit_GroupType(t *testing.T) {
	api, host := newContextLegacyAPI(t)
	seedFixedContext(api)

	groupID := "44444444-4444-4444-4444-444444444444"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextRestrictionResourceUnitConfig(host, "group", groupID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_context_restriction.test", tfjsonpath.New("type"), knownvalue.StringExact("group")),
					statecheck.ExpectKnownValue("circleci_context_restriction.test", tfjsonpath.New("value"), knownvalue.StringExact(groupID)),
					statecheck.ExpectKnownValue("circleci_context_restriction.test", tfjsonpath.New("project_id"), knownvalue.StringExact("")),
				},
			},
		},
	})
}

func TestContextRestrictionResourceUnit_RejectsInvalidType(t *testing.T) {
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      contextRestrictionResourceUnitConfig("http://127.0.0.1:1", "organization", "whatever"),
			ExpectError: regexp.MustCompile(`(?s)type.*(project|group|expression)`),
		}},
	})
}

func TestContextRestrictionResourceUnit_Delete(t *testing.T) {
	api, host := newContextLegacyAPI(t)
	seedFixedContext(api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextRestrictionResourceUnitConfig(host, "expression", `true`),
			},
			// An empty config destroys the resource.
			{
				Config: legacyContextProviderConfig(host),
			},
		},
	})

	var sawCreate, sawDelete bool
	for _, req := range api.recorded() {
		switch req {
		case "POST /api/v2/context/" + contextRestrictionUnitContextID + "/restrictions":
			sawCreate = true
		case "DELETE /api/v2/context/" + contextRestrictionUnitContextID + "/restrictions/rst-1":
			sawDelete = true
		}
	}
	if !sawCreate {
		t.Errorf("no create request with the expected URI, got %q", api.recorded())
	}
	if !sawDelete {
		t.Errorf("no delete request with the expected URI, got %q", api.recorded())
	}
}

// TestContextRestrictionResourceUnit_Import mirrors the workaround already
// present in TestAccContextRestrictionResource
// (context_restriction_resource_test.go): "name" must be ignored on import
// verification because create and import/read populate it differently (see the
// BUG note in TestContextRestrictionResourceUnit_ProjectType).
func TestContextRestrictionResourceUnit_Import(t *testing.T) {
	api, host := newContextLegacyAPI(t)
	seedFixedContext(api)

	projectID := "55555555-5555-5555-5555-555555555555"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextRestrictionResourceUnitConfig(host, "project", projectID),
			},
			{
				ResourceName:            "circleci_context_restriction.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"name"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs := s.RootModule().Resources["circleci_context_restriction.test"]

					return fmt.Sprintf("%s/%s", rs.Primary.Attributes["context_id"], rs.Primary.Attributes["id"]), nil
				},
			},
		},
	})
}

func TestContextRestrictionResourceUnit_ImportInvalidID(t *testing.T) {
	api, host := newContextLegacyAPI(t)
	seedFixedContext(api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextRestrictionResourceUnitConfig(host, "expression", "true"),
			},
			{
				ResourceName:  "circleci_context_restriction.test",
				ImportState:   true,
				ImportStateId: "no-slash-here",
				ExpectError:   regexp.MustCompile(`Invalid Import ID Format`),
			},
		},
	})
}

// TestContextRestrictionResourceUnit_CreateAPIError proves a 4xx from the API
// surfaces as a Terraform diagnostic rather than a panic.
func TestContextRestrictionResourceUnit_CreateAPIError(t *testing.T) {
	api, host := newContextLegacyAPI(t)
	seedFixedContext(api)
	api.fail(409, "The restriction you're trying to add already exists.")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      contextRestrictionResourceUnitConfig(host, "expression", "true"),
			ExpectError: regexp.MustCompile(`(?s)Error creating CircleCI context.*already exists`),
		}},
	})
}
