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
	return contextFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_context_restriction" "test" {
  context_id = %[1]q
  type       = %[2]q
  value      = %[3]q
}
`, contextRestrictionUnitContextID, restrictionType, value)
}

// seedFixedContext seeds a context whose id is stable across test runs, so
// restriction tests do not depend on the create-context flow.
func seedFixedContext(api *contextFakeAPI) {
	api.seedContext(contextRestrictionUnitContextID, contextUnitOrgID, "2024-01-02T03:04:05.000Z")
}

func TestContextRestrictionResourceUnit_ProjectType(t *testing.T) {
	api, host := newContextFakeAPI(t)
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
					// response never carries a restriction's name (the API's create
					// response has no "name" field at all), so immediately after
					// create the state always has name = "".
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
	api, host := newContextFakeAPI(t)
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

// TestContextRestrictionResourceUnit_GroupType covers the one value/context
// combination a "group" restriction ever succeeds against: an OAuth-backed
// organization, with value equal to that organization's own UUID. [NET,
// measured against a GitHub App, a GitLab and two GitHub OAuth organizations on
// 2026-08-21] — see circleci.ContextRestrictionTypeGroup's doc comment.
func TestContextRestrictionResourceUnit_GroupType(t *testing.T) {
	api, host := newContextFakeAPI(t)
	seedFixedContext(api)
	api.setOAuthBacked(contextRestrictionUnitContextID, true)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextRestrictionResourceUnitConfig(host, "group", contextUnitOrgID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_context_restriction.test", tfjsonpath.New("type"), knownvalue.StringExact("group")),
					statecheck.ExpectKnownValue("circleci_context_restriction.test", tfjsonpath.New("value"), knownvalue.StringExact(contextUnitOrgID)),
					statecheck.ExpectKnownValue("circleci_context_restriction.test", tfjsonpath.New("project_id"), knownvalue.StringExact("")),
					// The restriction's id is the organization's own UUID, not a
					// freshly minted one — it is idempotent with the "All members"
					// default, not a distinct restriction.
					statecheck.ExpectKnownValue("circleci_context_restriction.test", tfjsonpath.New("id"), knownvalue.StringExact(contextUnitOrgID)),
				},
			},
		},
	})
}

// TestContextRestrictionResourceUnit_GroupType_RequiresOAuthOrg proves the
// provider explains the API's real constraint on restriction_type = "group"
// rather than forwarding its bare, easy-to-miss message: on a standalone
// organization the API 400s no matter the value ([NET, measured against a
// GitHub App and a GitLab organization on 2026-08-21]), and the diagnostic
// must say so. Without contextRestrictionCreateErrorDetail's fix, the diagnostic is
// only circleci.Detail(err) — "This is only supported for OAuth orgs. (HTTP
// 400)" — which does not mention "OAuth-backed" or "standalone" together, so
// this regexp fails against the unpatched provider.
func TestContextRestrictionResourceUnit_GroupType_RequiresOAuthOrg(t *testing.T) {
	api, host := newContextFakeAPI(t)
	seedFixedContext(api)
	// oauthBacked defaults to false: this context stands in for a standalone org.

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      contextRestrictionResourceUnitConfig(host, "group", contextUnitOrgID),
			ExpectError: regexp.MustCompile(`(?s)Error creating CircleCI context restriction.*OAuth-backed.*standalone`),
		}},
	})
}

// TestContextRestrictionResourceUnit_GroupType_RequiresOwnOrgUUID proves the
// same explanation appears for the other half of the constraint: on an
// OAuth-backed organization, a value other than the organization's own UUID
// still 400s ([NET, measured on 2026-08-21]), and the diagnostic must say
// what value would have worked. The unpatched provider's diagnostic is only
// "Invalid restriction. (HTTP 400)", which says nothing about a UUID, so this
// regexp fails without the fix.
func TestContextRestrictionResourceUnit_GroupType_RequiresOwnOrgUUID(t *testing.T) {
	api, host := newContextFakeAPI(t)
	seedFixedContext(api)
	api.setOAuthBacked(contextRestrictionUnitContextID, true)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      contextRestrictionResourceUnitConfig(host, "group", "22222222-2222-2222-2222-222222222222"),
			ExpectError: regexp.MustCompile(`(?s)Error creating CircleCI context restriction.*organization's own UUID`),
		}},
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
	api, host := newContextFakeAPI(t)
	seedFixedContext(api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextRestrictionResourceUnitConfig(host, "expression", `true`),
			},
			// An empty config destroys the resource.
			{
				Config: contextFakeProviderConfig(host),
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
	api, host := newContextFakeAPI(t)
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

// TestContextRestrictionResourceUnit_Import_ExpressionType proves the same
// import round-trip holds for restriction_type = "expression", not only
// "project" (TestContextRestrictionResourceUnit_Import above). "name" is
// ignored for the same reason as there: it is always "" on create and only
// gains a value ("" here too, since expression restrictions have no name at
// all) on the read import triggers.
func TestContextRestrictionResourceUnit_Import_ExpressionType(t *testing.T) {
	api, host := newContextFakeAPI(t)
	seedFixedContext(api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextRestrictionResourceUnitConfig(host, "expression", `pipeline.git.branch == "main"`),
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

// TestContextRestrictionResourceUnit_Import_GroupType proves the same import
// round-trip holds for restriction_type = "group", the one restriction type
// whose id is not a freshly minted sequence number but the organization's own
// UUID (see TestContextRestrictionResourceUnit_GroupType) — import must still
// resolve it correctly.
func TestContextRestrictionResourceUnit_Import_GroupType(t *testing.T) {
	api, host := newContextFakeAPI(t)
	seedFixedContext(api)
	api.setOAuthBacked(contextRestrictionUnitContextID, true)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextRestrictionResourceUnitConfig(host, "group", contextUnitOrgID),
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
	api, host := newContextFakeAPI(t)
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
	api, host := newContextFakeAPI(t)
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

// TestContextRestrictionResourceUnit_RemovedOutsideTerraform proves drift
// detection: the restriction vanishing from the context's list drops the
// resource from state, so the next plan recreates it.
func TestContextRestrictionResourceUnit_RemovedOutsideTerraform(t *testing.T) {
	api, host := newContextFakeAPI(t)
	seedFixedContext(api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextRestrictionResourceUnitConfig(host, "expression", "true"),
			},
			{
				PreConfig:          func() { api.removeRestriction(contextRestrictionUnitContextID, "rst-1") },
				Config:             contextRestrictionResourceUnitConfig(host, "expression", "true"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestContextRestrictionResourceUnit_ForbiddenIsNotSilentlyRemoved mirrors
// context_resource_unit_test.go's test of the same name: a context this
// token cannot resolve answers 403 through the same middleware, so Read must
// surface a diagnostic naming the ambiguity rather than silently
// dropping the restriction (and recreating a possibly-live one) the way a
// genuine 404 does.
func TestContextRestrictionResourceUnit_ForbiddenIsNotSilentlyRemoved(t *testing.T) {
	api, host := newContextFakeAPI(t)
	seedFixedContext(api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextRestrictionResourceUnitConfig(host, "expression", "true"),
			},
			{
				PreConfig:   func() { api.setMissing(contextRestrictionUnitContextID, true) },
				Config:      contextRestrictionResourceUnitConfig(host, "expression", "true"),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)Unable to read CircleCI context restriction.*denied access.*lacks permission`),
			},
			{
				PreConfig: func() { api.setMissing(contextRestrictionUnitContextID, false) },
				Config:    contextRestrictionResourceUnitConfig(host, "expression", "true"),
			},
		},
	})
}

// TestContextRestrictionResourceUnit_DestroyAlreadyGoneSucceeds proves Delete
// treats a 403 (the context is gone, so the restriction cannot have survived
// it) as success rather than failing the destroy. See
// TestContextResourceUnit_DestroyAlreadyGoneSucceeds for why the test ends on
// an errored RefreshState step rather than an explicit destroy step: the
// framework's own end-of-test cleanup calls Delete directly, without
// refreshing first.
func TestContextRestrictionResourceUnit_DestroyAlreadyGoneSucceeds(t *testing.T) {
	api, host := newContextFakeAPI(t)
	seedFixedContext(api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextRestrictionResourceUnitConfig(host, "expression", "true"),
			},
			{
				PreConfig:    func() { api.setMissing(contextRestrictionUnitContextID, true) },
				RefreshState: true,
				ExpectError:  regexp.MustCompile(`(?s)Unable to read CircleCI context restriction`),
			},
		},
	})
}
