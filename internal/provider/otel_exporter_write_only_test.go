// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// These tests cover `headers_wo` + `headers_wo_version` on
// circleci_otel_exporter (otel_exporter_write_only.go).
//
// Every case that puts `headers_wo` in a configuration declares a minimum
// Terraform version through writeOnlySupported() — shared with the environment
// variable and webhook write-only tests — because write-only attributes are a
// 1.11 feature and without the check the failure on an older CLI is an opaque
// "Unsupported argument" rather than a skip.

// --- configurations -----------------------------------------------------------

// otelWriteOnlyConfig is otelExporterConfig with the collector credentials
// supplied as `headers_wo` plus a version. Everything else is identical, which is
// what makes the two configurations comparable.
func otelWriteOnlyConfig(host, headerValue string, version int) string {
	return governanceProviderConfig(host) + fmt.Sprintf(`
resource "circleci_otel_exporter" "test" {
  organization_id = %[1]q
  endpoint        = "otel.example.com:4317"
  protocol        = "grpc"

  headers_wo = {
    "x-api-key" = %[2]q
  }
  headers_wo_version = %[3]d
}
`, testOTelOrg, headerValue, version)
}

// otelStatefulHeadersConfig is the same exporter with the same credentials on the
// state-backed path.
func otelStatefulHeadersConfig(host, headerValue string) string {
	return governanceProviderConfig(host) + fmt.Sprintf(`
resource "circleci_otel_exporter" "test" {
  organization_id = %[1]q
  endpoint        = "otel.example.com:4317"
  protocol        = "grpc"

  headers = {
    "x-api-key" = %[2]q
  }
}
`, testOTelOrg, headerValue)
}

// --- fake accessors -----------------------------------------------------------

// createdHeaders returns the headers carried by the fake's most recent create
// body, i.e. what the provider actually put on the wire.
func (a *otelAPI) createdHeaders() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.createBodies) == 0 {
		return nil
	}

	headers, _ := a.createBodies[len(a.createBodies)-1]["headers"].(map[string]any)

	return headers
}

// lastCreateBody returns the fake's most recent create body in full.
func (a *otelAPI) lastCreateBody() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.createBodies) == 0 {
		return nil
	}

	return a.createBodies[len(a.createBodies)-1]
}

// snapshotOTelCreatedHeaders reads the headers the fake received as a step check,
// which runs while the exporter still exists. Reading them after
// resource.UnitTest returns would be a weaker assertion, because the harness
// destroys everything on the way out.
func snapshotOTelCreatedHeaders(api *otelAPI, into *map[string]any) func(*terraform.State) error {
	return func(*terraform.State) error {
		*into = api.createdHeaders()

		return nil
	}
}

// --- both names reach the same request ----------------------------------------

// TestOTelExporterWriteOnly_SameRequestAsHeaders proves `headers` and
// `headers_wo` are two spellings of one argument: the same headers, in the same
// request body, to the same route. The two paths run against separate fakes and
// the recorded bodies are compared, so a difference in any key fails.
func TestOTelExporterWriteOnly_SameRequestAsHeaders(t *testing.T) {
	const headerValue = "super-secret"

	observed := map[string]map[string]any{}

	for name, config := range map[string]func(host string) string{
		"headers": func(host string) string {
			return otelStatefulHeadersConfig(host, headerValue)
		},
		"headers_wo": func(host string) string {
			return otelWriteOnlyConfig(host, headerValue, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			api := newOTelAPI()
			srv := newOTelServer(t, api)

			var sent map[string]any

			resource.UnitTest(t, resource.TestCase{
				TerraformVersionChecks:   writeOnlySupported(),
				ProtoV6ProviderFactories: governanceProviderFactories,
				Steps: []resource.TestStep{{
					Config: config(srv.URL),
					Check:  snapshotOTelCreatedHeaders(api, &sent),
					ConfigStateChecks: []statecheck.StateCheck{
						// The write-only attribute is never persisted, whichever name was
						// used: the framework nulls it in plan and state.
						statecheck.ExpectKnownValue("circleci_otel_exporter.test", tfjsonpath.New("headers_wo"), knownvalue.Null()),
					},
				}},
			})

			if sent["x-api-key"] != headerValue {
				t.Fatalf("the %s path sent headers[x-api-key] = %v, want %q", name, sent["x-api-key"], headerValue)
			}

			observed[name] = api.lastCreateBody()
		})
	}

	if fmt.Sprint(observed["headers_wo"]) != fmt.Sprint(observed["headers"]) {
		t.Errorf("headers_wo reached the API differently from headers:\n  headers_wo: %v\n  headers:    %v",
			observed["headers_wo"], observed["headers"])
	}
}

// TestOTelExporterWriteOnly_HeadersAreNullInState covers the other half of the
// trade the documentation describes: on the write-only path neither the headers
// nor anything derived from them is in state, while the version is.
func TestOTelExporterWriteOnly_HeadersAreNullInState(t *testing.T) {
	api := newOTelAPI()
	srv := newOTelServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   writeOnlySupported(),
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{{
			Config: otelWriteOnlyConfig(srv.URL, "super-secret", 4),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("circleci_otel_exporter.test", tfjsonpath.New("headers_wo"), knownvalue.Null()),
				// `headers` stays null too. Nothing may be copied across from the
				// write-only spelling into the persisted one, and a read must not adopt
				// the API's placeholder map either — see the refresh test below.
				statecheck.ExpectKnownValue("circleci_otel_exporter.test", tfjsonpath.New("headers"), knownvalue.Null()),
				statecheck.ExpectKnownValue("circleci_otel_exporter.test", tfjsonpath.New("headers_wo_version"), knownvalue.Int64Exact(4)),
			},
		}},
	})
}

// TestOTelExporterWriteOnly_RefreshLeavesHeadersNull is the write-only mirror of
// TestAccOTelExporterHeadersAreNotRefreshed, and it is the one that would break
// first if otelHeadersAfterRead were removed.
//
// A read answers with the header *names* in full and every value as the
// placeholder. On the write-only path there is no prior key set to compare those
// names against, so the provider must leave `headers` null. Adopting the API's map
// instead would write {"x-api-key": "xxxx"} into `headers` — a non-write-only
// attribute that forces replacement — and every later plan would want to recreate
// the exporter forever.
func TestOTelExporterWriteOnly_RefreshLeavesHeadersNull(t *testing.T) {
	api := newOTelAPI()
	srv := newOTelServer(t, api)

	config := otelWriteOnlyConfig(srv.URL, "super-secret", 1)

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   writeOnlySupported(),
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{RefreshState: true},
			{
				// An empty plan after the refresh is what proves the placeholder map was
				// not adopted: if it had been, `headers` would be set in state, null in
				// configuration, and RequiresReplace would want a new exporter.
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_otel_exporter.test", tfjsonpath.New("headers"), knownvalue.Null()),
				},
			},
		},
	})
}

// TestOTelExporterWriteOnly_HeaderAddedOutsideTerraformRecreates is the
// write-only mirror of TestAccOTelExporterHeaderAddedOutsideTerraform.
//
// This used to be invisible: on the write-only path there was no key set in
// state to compare CircleCI's returned names against. headers_wo_names is that
// key set now — populated by Create, refreshed by
// otelRefreshWriteOnlyHeaderNames — so a header added elsewhere is detected the
// same way it already was on the `headers` path, and for the same underlying
// reason: `headers` already forces replacement, and detecting the drift adopts
// CircleCI's map into it.
//
// The assertion is a PlanOnly step with plancheck.ExpectResourceAction, not
// ExpectNonEmptyPlan on the RefreshState step. The latter was tried first and
// found to pass unconditionally — with or without api.addHeader actually
// called — because a bare RefreshState step has no config to plan against, so
// "non-empty" there asserts nothing. TestAccOTelExporterHeaderAddedOutsideTerraform
// has the same shape and the same blind spot; it is not this test's to fix.
func TestOTelExporterWriteOnly_HeaderAddedOutsideTerraformRecreates(t *testing.T) {
	api := newOTelAPI()
	srv := newOTelServer(t, api)

	config := otelWriteOnlyConfig(srv.URL, "super-secret", 1)

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   writeOnlySupported(),
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_otel_exporter.test",
						tfjsonpath.New("headers_wo_names"),
						knownvalue.SetExact([]knownvalue.Check{
							knownvalue.StringExact("x-api-key"),
						}),
					),
				},
			},
			{
				// Not RefreshState followed by a separate plan step: a normal apply step
				// refreshes before planning on its own, and — unlike a bare RefreshState
				// step — ConfigPlanChecks.PreApply can inspect what that refresh produced.
				// (ExpectNonEmptyPlan on a RefreshState step was tried first and found to
				// pass unconditionally, drift or not, because a bare RefreshState step has
				// no config to plan against.)
				PreConfig: func() { api.addHeader("x-tenant") },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_otel_exporter.test",
							plancheck.ResourceActionDestroyBeforeCreate,
						),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_otel_exporter.test",
						tfjsonpath.New("id"),
						knownvalue.StringExact("00000000-0000-0000-0000-000000000002"),
					),
					// The replacement's own Create starts clean: the new exporter's
					// headers_wo_names matches what was just configured, with no drift of
					// its own.
					statecheck.ExpectKnownValue(
						"circleci_otel_exporter.test",
						tfjsonpath.New("headers_wo_names"),
						knownvalue.SetExact([]knownvalue.Check{
							knownvalue.StringExact("x-api-key"),
						}),
					),
				},
			},
		},
	})
}

// TestOTelExporterWriteOnly_NoDriftIsAnEmptyPlan is the control for the test
// above: refreshing with nothing changed outside Terraform must not itself
// start reporting drift just because headers_wo_names now exists.
func TestOTelExporterWriteOnly_NoDriftIsAnEmptyPlan(t *testing.T) {
	api := newOTelAPI()
	srv := newOTelServer(t, api)

	config := otelWriteOnlyConfig(srv.URL, "super-secret", 1)

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   writeOnlySupported(),
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{RefreshState: true},
			{
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}

// --- rotation -----------------------------------------------------------------

// TestOTelExporterWriteOnly_RotationNeedsAVersionBump covers both halves of the
// version contract: a changed `headers_wo` alone is invisible to Terraform and is
// therefore not sent, and bumping `headers_wo_version` is what sends it.
//
// The middle step is the one worth having. It fails if the resource ever grows
// something that leaks the headers into state, because then the change alone would
// produce a diff and the version would be pointless.
//
// The bump replaces rather than updates, because CircleCI has no update route —
// the same behaviour `headers` already had.
func TestOTelExporterWriteOnly_RotationNeedsAVersionBump(t *testing.T) {
	api := newOTelAPI()
	srv := newOTelServer(t, api)

	var rotated map[string]any

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   writeOnlySupported(),
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: otelWriteOnlyConfig(srv.URL, "super-secret", 1),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_otel_exporter.test",
						tfjsonpath.New("id"),
						knownvalue.StringExact("00000000-0000-0000-0000-000000000001"),
					),
				},
			},
			{
				// New header value, same version: no diff, so no request.
				Config:   otelWriteOnlyConfig(srv.URL, "rotated", 1),
				PlanOnly: true,
			},
			{
				Config: otelWriteOnlyConfig(srv.URL, "rotated", 2),
				Check:  snapshotOTelCreatedHeaders(api, &rotated),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_otel_exporter.test",
							plancheck.ResourceActionDestroyBeforeCreate,
						),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_otel_exporter.test",
						tfjsonpath.New("headers_wo_version"),
						knownvalue.Int64Exact(2),
					),
					// A new exporter, because there is no route that rewrites headers.
					statecheck.ExpectKnownValue(
						"circleci_otel_exporter.test",
						tfjsonpath.New("id"),
						knownvalue.StringExact("00000000-0000-0000-0000-000000000002"),
					),
				},
			},
		},
	})

	if rotated["x-api-key"] != "rotated" {
		t.Errorf(`the rotation sent headers[x-api-key] = %v, want "rotated" — a rotation that does not `+
			`reach the wire leaves the old credentials live while state claims otherwise`, rotated["x-api-key"])
	}

	// Exactly two creates: the original and the rotation. The unbumped change in
	// between must not have been sent.
	var creates int
	for _, req := range api.recorded() {
		if req == "POST /api/v2/otel/exporters" {
			creates++
		}
	}

	if creates != 2 {
		t.Errorf("the provider sent %d create(s), want 2 (the original and the version bump; the "+
			"unbumped change must not be sent): %v", creates, api.recorded())
	}
}

// --- attribute combinations ---------------------------------------------------

// TestOTelExporterWriteOnly_HeaderAttributeCombinations covers
// otelHeadersConfigValidator and the AlsoRequires/AtLeast validators on the
// version.
//
// The first case is the one that distinguishes this resource from the other three:
// neither attribute set is *valid* here, because an exporter with no headers is
// ordinary. That is why the validator is Conflicting rather than ExactlyOneOf, and
// it is asserted rather than assumed — ExactlyOneOf would start rejecting two of
// the three exporters in this resource's own documented example.
func TestOTelExporterWriteOnly_HeaderAttributeCombinations(t *testing.T) {
	body := func(headers string) string {
		return fmt.Sprintf(`
resource "circleci_otel_exporter" "test" {
  organization_id = %q
  endpoint        = "otel.example.com:4317"
  protocol        = "grpc"
%s
}
`, testOTelOrg, headers)
	}

	tests := map[string]struct {
		headers string
		error   *regexp.Regexp
		// valid marks the case that must plan cleanly rather than be refused, which
		// means the plan is not empty: an exporter is waiting to be created.
		valid bool
	}{
		"neither set is valid": {
			headers: ``,
			error:   nil,
			valid:   true,
		},
		"both set": {
			headers: "  headers = { a = \"1\" }\n  headers_wo = { a = \"2\" }\n  headers_wo_version = 1",
			error:   regexp.MustCompile(`Invalid Attribute Combination`),
		},
		"version without headers_wo": {
			headers: "  headers = { a = \"1\" }\n  headers_wo_version = 1",
			error:   regexp.MustCompile(`Invalid Attribute Combination`),
		},
		"headers_wo without a version": {
			// The silent-no-op case: without the reverse AlsoRequires this validates
			// fine, is written once, and can then never be rotated, with Terraform
			// reporting "no changes" on every later edit.
			headers: "  headers_wo = { a = \"1\" }",
			error:   regexp.MustCompile(`Invalid Attribute Combination`),
		},
		"version below one": {
			headers: "  headers_wo = { a = \"1\" }\n  headers_wo_version = 0",
			error:   regexp.MustCompile(`Invalid Attribute Value`),
		},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			api := newOTelAPI()
			srv := newOTelServer(t, api)

			resource.UnitTest(t, resource.TestCase{
				TerraformVersionChecks:   writeOnlySupported(),
				ProtoV6ProviderFactories: governanceProviderFactories,
				Steps: []resource.TestStep{{
					Config:             governanceProviderConfig(srv.URL) + body(testCase.headers),
					PlanOnly:           true,
					ExpectNonEmptyPlan: testCase.valid,
					ExpectError:        testCase.error,
				}},
			})

			// Validation happens before anything is written, and PlanOnly writes
			// nothing in the valid case either.
			for _, req := range api.recorded() {
				if req == "POST /api/v2/otel/exporters" {
					t.Errorf("the provider created an exporter during a plan-only step: %v", api.recorded())
				}
			}
		})
	}
}

// --- the guard --------------------------------------------------------------

// TestOTelExporterWriteOnly_GuardsAgainstMissingHeaders drives Create directly,
// because a plan cannot reach this state: the validators reject a
// `headers_wo_version` with no `headers_wo` at validation time. The guard exists
// for the gap after that — a write-only value that resolved to nothing by apply —
// where the alternative is an exporter created with no credentials, accepted by
// CircleCI and then rejected by the collector on every export. There is nothing
// to fall back on: a read answers with the placeholder rather than the values.
//
// Update is not exercised: the resource has none. Every configurable attribute
// carries RequiresReplace because CircleCI has no update route, so
// otelExporterResource.Update is an empty method and the vault#2900 hazard has
// nowhere to occur here.
func TestOTelExporterWriteOnly_GuardsAgainstMissingHeaders(t *testing.T) {
	t.Parallel()

	schema := otelExporterResourceSchemaForTest(t)

	// headers and headers_wo both null, the version set: the shape of an exporter
	// already managed through the write-only path whose headers have gone missing.
	model := otelExporterResourceModel{
		ID:               types.StringValue("00000000-0000-0000-0000-000000000001"),
		OrganizationID:   types.StringValue(testOTelOrg),
		OrgID:            types.StringValue(testOTelOrg),
		Endpoint:         types.StringValue("otel.example.com:4317"),
		Protocol:         types.StringValue("grpc"),
		Insecure:         types.BoolValue(false),
		Headers:          types.MapNull(types.StringType),
		HeadersWO:        types.MapNull(types.StringType),
		HeadersWOVersion: types.Int64Value(1),
		HeadersWONames:   types.SetNull(types.StringType),
		Issues:           types.ListNull(types.StringType),
	}

	config := configForTest(t, schema, model)

	createResp := fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	// A nil client: reaching the API at all would panic, so "an error was reported"
	// and "nothing panicked" together prove the request was never issued.
	(&otelExporterResource{}).Create(t.Context(), fwresource.CreateRequest{
		Config: config,
		Plan:   tfsdk.Plan{Schema: schema, Raw: config.Raw},
	}, &createResp)

	reported := fmt.Sprint(createResp.Diagnostics)

	if !regexp.MustCompile(`Missing OTLP exporter headers`).MatchString(reported) {
		t.Fatalf("Create reported no missing-headers error; an exporter with no credentials would have been created: %s",
			reported)
	}
	if !regexp.MustCompile(`headers_wo_version`).MatchString(reported) {
		t.Errorf("Create's error does not explain the write-only path: %s", reported)
	}
}

// TestOTelExporterWriteOnly_NoHeadersIsNotAnError is the other side of the guard:
// with no version set, no headers at all resolves to an empty map and the exporter
// is created. This is the case ExactlyOneOf would have broken.
func TestOTelExporterWriteOnly_NoHeadersIsNotAnError(t *testing.T) {
	api := newOTelAPI()
	srv := newOTelServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{{
			Config: otelExporterConfig(srv.URL, "otel.example.com:4317", "grpc"),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("circleci_otel_exporter.test", tfjsonpath.New("headers"), knownvalue.Null()),
				statecheck.ExpectKnownValue("circleci_otel_exporter.test", tfjsonpath.New("headers_wo"), knownvalue.Null()),
				statecheck.ExpectKnownValue("circleci_otel_exporter.test", tfjsonpath.New("headers_wo_version"), knownvalue.Null()),
			},
		}},
	})

	if headers := api.createdHeaders(); len(headers) != 0 {
		t.Errorf("an exporter configured with no headers sent headers = %v, want none", headers)
	}
}

// --- helpers ----------------------------------------------------------------

func otelExporterResourceSchemaForTest(t *testing.T) rschema.Schema {
	t.Helper()

	resp := &fwresource.SchemaResponse{}
	(&otelExporterResource{}).Schema(t.Context(), fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema diagnostics: %+v", resp.Diagnostics)
	}

	// WriteOnly is legal on a map attribute — only set nested attributes and set
	// blocks reject it — and Terraform runs this validation during
	// GetProviderSchema, so a mistake here would break every operation rather than
	// one path. Asserting it names the failure.
	if diags := resp.Schema.ValidateImplementation(t.Context()); diags.HasError() {
		t.Fatalf("the schema does not validate: %+v", diags)
	}

	return resp.Schema
}
