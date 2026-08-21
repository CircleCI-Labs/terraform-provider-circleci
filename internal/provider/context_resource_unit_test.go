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

// TestContextResourceUnit_ImportBareContextID proves the import is authoritative
// from the API rather than from the practitioner's typing.
//
// A bare context id carries no organization at all, so the only way the resulting
// state can name the right one is by reading it from GET /context/{id}. The state
// is persisted and the following step asserts an EMPTY plan, which is the whole
// point: org_id forces replacement, so an import that stored the wrong
// organization would show up here as a destroy-and-create rather than as a
// no-op.
//
// Before the fix this failed at the import step — a bare id was rejected as
// "Invalid Import ID Format", because the organization could only ever come from
// the id string.
func TestContextResourceUnit_ImportBareContextID(t *testing.T) {
	api, host := newContextFakeAPI(t)

	// The context already exists at CircleCI and Terraform has never seen it,
	// which is the situation an import actually addresses. Seeding it (rather
	// than creating it through Terraform and importing over the top) is what
	// lets the import land in an EMPTY state, so the import is the only thing
	// that can put an organization there.
	api.seedContext("ctx-seeded", contextUnitOrgID, "2024-01-02T03:04:05.000Z")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Provider only: nothing in state yet.
				Config: contextFakeProviderConfig(host),
			},
			{
				Config:             contextResourceUnitConfig(host, contextUnitOrgID),
				ResourceName:       "circleci_context.test",
				ImportState:        true,
				ImportStatePersist: true,
				// Deliberately just the context id: no organization anywhere in
				// the import id.
				ImportStateId: "ctx-seeded",
			},
			{
				Config: contextResourceUnitConfig(host, contextUnitOrgID),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})

	// The import must have gone to the API to learn the organization; there is
	// nowhere else it could have come from.
	var sawRead bool
	for _, req := range api.recorded() {
		if req == "GET /api/v2/context/ctx-seeded" {
			sawRead = true
		}
	}
	if !sawRead {
		t.Errorf("import did not read the context from the API, got %q", api.recorded())
	}
}

// TestContextResourceUnit_ImportWrongOrgIsRejected is the regression test for the
// silent data loss.
//
// The import id here is well-formed and names a real context, but its
// organization is wrong by one character. The old implementation split the string
// and wrote that organization into state with no validation of any kind, so the
// import reported success; because org_id forces replacement, the very next plan
// then read "1 to add, 1 to destroy" and applying it destroyed a live context
// along with every environment variable and restriction on it. Nothing in that
// sequence looked like a failure — the import succeeded and the plan looked like
// a legitimate replacement.
//
// The error must name BOTH organizations, because either could be the mistake:
// the practitioner may have mistyped the organization, or may be importing a
// context they did not mean to.
//
// Before the fix there was no error at all and this failed with "Error running
// import: ... expected an error but got none".
func TestContextResourceUnit_ImportWrongOrgIsRejected(t *testing.T) {
	_, host := newContextFakeAPI(t)

	// contextUnitOrgID with its final digit changed: the single mistyped
	// character that used to be enough to destroy a context.
	const mistypedOrgID = "org-11111111-1111-1111-1111-111111111112"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextResourceUnitConfig(host, contextUnitOrgID),
			},
			{
				ResourceName:  "circleci_context.test",
				ImportState:   true,
				ImportStateId: mistypedOrgID + "/ctx-1",
				// Both the supplied and the real organization must appear.
				ExpectError: regexp.MustCompile(
					`(?s)Import ID organization does not match the API.*` +
						regexp.QuoteMeta(mistypedOrgID) +
						`.*` + regexp.QuoteMeta(contextUnitOrgID),
				),
			},
		},
	})
}

// TestContextResourceUnit_ImportUnresolvableID covers the two remaining shapes of
// a bad import id: one with nothing where the context id belongs, and one naming
// a context the API will not resolve.
//
// The second case is why "not-a-valid-composite-id" no longer produces a format
// error: a bare id is now legitimate, so an id with no slash in it is taken as a
// context id and fails when the API declines to resolve it. That is a better
// diagnostic than the old format complaint, which fired without ever asking the
// API whether the id was real.
func TestContextResourceUnit_ImportUnresolvableID(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		importID  string
		wantError *regexp.Regexp
	}{
		// The empty import id is deliberately not a case here: the test harness
		// treats an unset ImportStateId as "use the resource's own id", so it
		// would exercise a successful import and pass for the wrong reason.
		// This covers the same guard with a composite id whose context half is
		// empty.
		"organization with no context id": {
			importID:  contextUnitOrgID + "/",
			wantError: regexp.MustCompile(`Invalid Import ID Format`),
		},
		"context the API will not resolve": {
			importID:  "not-a-valid-composite-id",
			wantError: regexp.MustCompile(`Unable to import CircleCI context not-a-valid-composite-id`),
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

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
						ImportStateId: test.importID,
						ExpectError:   test.wantError,
					},
				},
			})
		})
	}
}

// TestContextResourceUnit_ReadTakesOrganizationFromAPI proves Read no longer
// copies state's organization back over itself.
//
// Read used to preserve whatever state held, on the stated grounds that the read
// route "does not report which organization a context belongs to" — a comment
// that was simply false. The consequence was that a wrong organization in state,
// however it got there, was invisible forever: no refresh could correct it, and
// it kept forcing a replacement on every plan.
//
// Here the API's answer changes under Terraform. A plan must notice. Before the
// fix the plan was empty, because Read never looked.
func TestContextResourceUnit_ReadTakesOrganizationFromAPI(t *testing.T) {
	api, host := newContextFakeAPI(t)

	const otherOrgID = "org-22222222-2222-2222-2222-222222222222"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contextResourceUnitConfig(host, contextUnitOrgID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_context.test", tfjsonpath.New("org_id"), knownvalue.StringExact(contextUnitOrgID),
					),
				},
			},
			{
				PreConfig: func() {
					// The context is now reported as owned by a different
					// organization than the one in state and in config.
					api.setContextOrg("ctx-1", otherOrgID)
				},
				Config: contextResourceUnitConfig(host, contextUnitOrgID),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						// Not an empty plan: the configured organization no
						// longer matches reality, and there is no route that
						// moves a context, so replacement is the only way to
						// satisfy the configuration.
						plancheck.ExpectResourceAction(
							"circleci_context.test", plancheck.ResourceActionDestroyBeforeCreate,
						),
					},
				},
			},
		},
	})
}

// TestContextResourceUnit_ForbiddenIsNotSilentlyRemoved proves the deliberate
// choice documented on context_resource.go's Read and internal/circleci's
// GetContext: a context this token cannot resolve answers 403, the same
// response the API gives for "deleted", "belongs to another organization"
// and "no permission" alike. Silently
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
// rare race where the id resolves fine but the read itself then answers a
// literal 404. That must still drop the resource from state and recreate
// cleanly rather than erroring — unlike
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
