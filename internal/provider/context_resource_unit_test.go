// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
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
	return contextFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_context" "test" {
  organization_id = %[1]q
  name            = "build"
}
`, orgID)
}

const contextUnitOrgID = "org-11111111-1111-1111-1111-111111111111"

func TestContextResourceUnit_CRUD(t *testing.T) {
	api, host := newContextFakeAPI(t)

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
	_, host := newContextFakeAPI(t)

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
	_, host := newContextFakeAPI(t)

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

// TestContextResourceUnit_ForbiddenIsNotSilentlyRemoved proves the deliberate
// choice documented on context_resource.go's Read and internal/circleci's
// GetContext: a context this token cannot resolve answers 403, the same
// response the API's context-resolution step gives for "deleted",
// "belongs to another organization" and "no permission" alike. Silently
// dropping the resource from state on 403 (the way a genuine 404 does) would
// mean a token that merely lost permission causes Terraform to recreate a
// live context on the next apply — so this must surface as a hard diagnostic
// naming the ambiguity instead, exactly like circleci_group's Read.
func TestContextResourceUnit_ForbiddenIsNotSilentlyRemoved(t *testing.T) {
	api, host := newContextFakeAPI(t)

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
				ExpectError: regexp.MustCompile(`(?s)Unable to read CircleCI context ctx-1.*denied access.*lacks permission`),
			},
			{
				// Restore the context so the framework's destroy step succeeds.
				PreConfig: func() { api.setMissing("ctx-1", false) },
				Config:    contextResourceUnitConfig(host, contextUnitOrgID),
			},
		},
	})
}

// TestContextResourceUnit_GenuineNotFoundRecreatesCleanly exercises the other
// branch of the same Read method: internal/circleci.GetContext documents a
// rare race where the context-resolution step resolves the id fine but the read itself then
// answers a literal 404 (the API.ErrNotFound). That must still drop
// the resource from state and recreate cleanly rather than erroring — unlike
// the 403 case above. contextFakeAPI always models the common 403 case, so
// this uses a small dedicated fake to force the rare one directly.
func TestContextResourceUnit_GenuineNotFoundRecreatesCleanly(t *testing.T) {
	var notFound bool

	const createBody = `{"id":"ctx-1","name":"build","created_at":"2024-01-02T03:04:05.000Z"}`

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/context", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, createBody)
	})
	mux.HandleFunc("GET /api/v2/context/{id}", func(w http.ResponseWriter, _ *http.Request) {
		if notFound {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"context not found"}`)

			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, createBody)
	})
	mux.HandleFunc("DELETE /api/v2/context/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"message":"Context deleted."}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextResourceUnitConfig(srv.URL, contextUnitOrgID),
			},
			{
				PreConfig:          func() { notFound = true },
				Config:             contextResourceUnitConfig(srv.URL, contextUnitOrgID),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// Restore so the framework's destroy step succeeds.
				PreConfig: func() { notFound = false },
				Config:    contextResourceUnitConfig(srv.URL, contextUnitOrgID),
			},
		},
	})
}

// TestContextResourceUnit_DestroyAlreadyGoneSucceeds proves Delete treats a
// 403 as the already-absent context it almost always means (see
// internal/circleci/context.go's DeleteContext), rather than failing a
// destroy that has nothing left to do.
//
// The test ends on an errored RefreshState step, the same shape
// TestContextResourceUnit_ForbiddenIsNotSilentlyRemoved uses, deliberately
// without a following "restore" step: terraform-plugin-testing's own
// end-of-test cleanup runs "terraform destroy" with refreshing disabled (see
// (*plugintest.WorkingDir).Destroy), so it calls Delete directly against the
// context id still recorded in state — without going through Read first —
// which is exactly the path that would fail outright if Delete did not
// tolerate 403 on its own.
func TestContextResourceUnit_DestroyAlreadyGoneSucceeds(t *testing.T) {
	api, host := newContextFakeAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextResourceUnitConfig(host, contextUnitOrgID),
			},
			{
				PreConfig:    func() { api.setMissing("ctx-1", true) },
				RefreshState: true,
				ExpectError:  regexp.MustCompile(`(?s)Unable to read CircleCI context ctx-1`),
			},
		},
	})
}

// TestContextResourceUnit_CreateAPIError proves a 4xx from the API surfaces as a
// Terraform diagnostic rather than a panic.
func TestContextResourceUnit_CreateAPIError(t *testing.T) {
	api, host := newContextFakeAPI(t)
	api.fail(400, "Invalid owner type - only organization is supported at present")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      contextResourceUnitConfig(host, contextUnitOrgID),
			ExpectError: regexp.MustCompile(`(?s)Error creating CircleCI context.*only organization is supported`),
		}},
	})
}

// TestContextResourceUnit_OrganizationIDChangeForcesReplacement is a
// regression test for a fixed bug: "organization_id" previously had no
// RequiresReplace plan modifier, unlike "name", so changing it in
// configuration planned a silent in-place Update — but Update() was a
// complete no-op, so Terraform core detected the applied state did not match
// the planned one and failed with "Provider produced inconsistent result
// after apply" instead of either updating the resource or forcing a
// replacement. There is no API route that moves a context between
// organizations, so replacement is the only correct plan.
func TestContextResourceUnit_OrganizationIDChangeForcesReplacement(t *testing.T) {
	_, host := newContextFakeAPI(t)

	otherOrgID := "org-22222222-2222-2222-2222-222222222222"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextResourceUnitConfig(host, contextUnitOrgID),
			},
			{
				Config: contextResourceUnitConfig(host, otherOrgID),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("circleci_context.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_context.test", tfjsonpath.New("organization_id"), knownvalue.StringExact(otherOrgID)),
				},
			},
		},
	})
}
