// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// Tests for pipeline_definition_id_deprecation.go: `pipeline_id` ->
// `pipeline_definition_id` on circleci_trigger.

// triggerPipelineAttributeConfig renders a circleci_trigger naming its pipeline
// definition under attr, which is either name of the pair.
func triggerPipelineAttributeConfig(host, attr string) string {
	return triggerPipelineAttributeConfigWithID(host, attr, fakeTriggerPipelineID)
}

// triggerPipelineAttributeConfigWithID is the same, for a named definition id.
func triggerPipelineAttributeConfigWithID(host, attr, pipelineDefinitionID string) string {
	return triggerFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_trigger" "test" {
  project_id                    = %[1]q
  %[2]s = %[3]q
  event_source_provider         = "github_app"
  event_source_repo_external_id = "1234"
  event_preset                  = "all-pushes"
  checkout_ref                  = "main"
  config_ref                    = "main"
}
`, fakeTriggerProjectID, attr, pipelineDefinitionID)
}

// TestTriggerResourceUnit_BothPipelineAttributeNamesReachTheSameRoute asserts on the
// request the fake received, not merely that both configurations apply. The whole
// point of the pair is that they resolve to the same path segment, and a mistake here
// — reading only one of the two — would create the trigger under an empty definition
// id, which the fake's catch-all 404 would report as an unrecognized route rather than
// as a wrong value.
func TestTriggerResourceUnit_BothPipelineAttributeNamesReachTheSameRoute(t *testing.T) {
	for _, attr := range []string{"pipeline_id", "pipeline_definition_id"} {
		t.Run(attr, func(t *testing.T) {
			api, host := newFakeTriggerAPI(t)

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: triggerPipelineAttributeConfig(host, attr),
					ConfigStateChecks: []statecheck.StateCheck{
						// Both are populated whichever name was written, so the value is
						// readable under either and neither is left null in state.
						statecheck.ExpectKnownValue(
							"circleci_trigger.test",
							tfjsonpath.New("pipeline_id"),
							knownvalue.StringExact(fakeTriggerPipelineID),
						),
						statecheck.ExpectKnownValue(
							"circleci_trigger.test",
							tfjsonpath.New("pipeline_definition_id"),
							knownvalue.StringExact(fakeTriggerPipelineID),
						),
					},
				}},
			})

			// The definition id is a path segment on create and nowhere else, so this
			// is the only place the resolved value is observable from outside.
			api.lastRequest(t, "POST",
				"/api/v2/projects/"+fakeTriggerProjectID+"/pipeline-definitions/"+
					fakeTriggerPipelineID+"/triggers")
		})
	}
}

// TestTriggerResourceUnit_SwitchingPipelineAttributeIsNoop is the test this
// deprecation lives or dies by.
//
// `pipeline_id` is Required today, so a practitioner following the deprecation notice
// deletes it and writes `pipeline_definition_id` instead. Had the replacement been a
// plain Optional attribute, Terraform would plan the old name as null, see a change,
// and — because no Update can move a trigger between definitions — the migration would
// be worse than the breaking rename it exists to avoid. Optional+Computed makes
// Terraform retain the prior value instead.
//
// The assertion is an empty plan, not merely "not a destroy": anything else means the
// two names are not interchangeable and every practitioner sees churn on upgrade.
func TestTriggerResourceUnit_SwitchingPipelineAttributeIsNoop(t *testing.T) {
	_, host := newFakeTriggerAPI(t)

	bothPopulated := []statecheck.StateCheck{
		statecheck.ExpectKnownValue(
			"circleci_trigger.test",
			tfjsonpath.New("pipeline_id"),
			knownvalue.StringExact(fakeTriggerPipelineID),
		),
		statecheck.ExpectKnownValue(
			"circleci_trigger.test",
			tfjsonpath.New("pipeline_definition_id"),
			knownvalue.StringExact(fakeTriggerPipelineID),
		),
	}

	noChange := resource.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{
			plancheck.ExpectResourceAction("circleci_trigger.test", plancheck.ResourceActionNoop),
			plancheck.ExpectEmptyPlan(),
		},
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// The state a practitioner already has today.
				Config:            triggerPipelineAttributeConfig(host, "pipeline_id"),
				ConfigStateChecks: bothPopulated,
			},
			{
				// The migration: same definition, new attribute name.
				Config:            triggerPipelineAttributeConfig(host, "pipeline_definition_id"),
				ConfigPlanChecks:  noChange,
				ConfigStateChecks: bothPopulated,
			},
			{
				// And back again, so the deprecation is not a one-way door.
				Config:            triggerPipelineAttributeConfig(host, "pipeline_id"),
				ConfigPlanChecks:  noChange,
				ConfigStateChecks: bothPopulated,
			},
		},
	})
}

// TestTriggerResourceUnit_ChangingPipelineDefinitionIDReplaces covers this bug
// under both names.
//
// The definition id is a path segment on the create and appears nowhere else: the
// update route is /projects/{project_id}/triggers/{trigger_id} and its body has no
// field for it. So this used to plan an in-place update, the API accepted the PATCH
// and ignored the value, apply reported success — and the trigger was still
// attached to the old definition, with state saying otherwise and no Read able to
// tell, because the API never returns the definition. A silent no-op.
//
// The assertions are deliberately about the *request*, not only the plan action: a
// replacement that recreated the trigger under the old definition would satisfy
// ExpectResourceAction and still be the same bug.
func TestTriggerResourceUnit_ChangingPipelineDefinitionIDReplaces(t *testing.T) {
	const otherPipelineID = "eeeeeeee-1111-2222-3333-444444444444"

	for _, attr := range []string{"pipeline_id", "pipeline_definition_id"} {
		t.Run(attr, func(t *testing.T) {
			api, host := newFakeTriggerAPI(t)

			bothPopulated := func(want string) []statecheck.StateCheck {
				return []statecheck.StateCheck{
					statecheck.ExpectKnownValue("circleci_trigger.test",
						tfjsonpath.New("pipeline_id"), knownvalue.StringExact(want)),
					statecheck.ExpectKnownValue("circleci_trigger.test",
						tfjsonpath.New("pipeline_definition_id"), knownvalue.StringExact(want)),
				}
			}

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{
						Config:            triggerPipelineAttributeConfigWithID(host, attr, fakeTriggerPipelineID),
						ConfigStateChecks: bothPopulated(fakeTriggerPipelineID),
					},
					{
						Config: triggerPipelineAttributeConfigWithID(host, attr, otherPipelineID),
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PreApply: []plancheck.PlanCheck{
								plancheck.ExpectResourceAction(
									"circleci_trigger.test",
									plancheck.ResourceActionDestroyBeforeCreate,
								),
							},
						},
						ConfigStateChecks: bothPopulated(otherPipelineID),
					},
				},
			})

			// Two creates, the second under the *new* definition: that is the whole
			// claim. A PATCH anywhere would mean the change was sent to the route that
			// silently drops it.
			var creates []string

			for _, request := range api.recorded() {
				switch request.Method {
				case "POST":
					creates = append(creates, request.Path)
				case "PATCH":
					t.Errorf("the provider sent a PATCH for a changed pipeline definition id, which the "+
						"update endpoint has no field for and silently ignores: %+v", request)
				}
			}

			route := func(pipelineDefinitionID string) string {
				return "/api/v2/projects/" + fakeTriggerProjectID + "/pipeline-definitions/" +
					pipelineDefinitionID + "/triggers"
			}

			want := []string{route(fakeTriggerPipelineID), route(otherPipelineID)}
			if !slices.Equal(creates, want) {
				t.Errorf("triggers were created at %v, want %v: the second create is the replacement, and "+
					"it has to name the new definition", creates, want)
			}
		})
	}
}

// TestTriggerResourceUnit_DestroyWithPipelineDefinitionID runs a real destroy.
//
// A destroy plans a null object *and* a null configuration, so ModifyPlan must ask
// whether it has anything to reconcile before reading the configuration: Get-ing a
// null config into triggerResourceModel fails with a value-conversion error naming the
// model type, which reads like a schema bug rather than the missing guard it is. That
// mistake was made once already, in org_id_deprecation.go, and is why
// pipelineDefinitionIDPlanNeedsReconcile is a separate function.
func TestTriggerResourceUnit_DestroyWithPipelineDefinitionID(t *testing.T) {
	api, host := newFakeTriggerAPI(t)

	config := triggerPipelineAttributeConfig(host, "pipeline_definition_id")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			api.mu.Lock()
			defer api.mu.Unlock()

			if len(api.triggers) != 0 {
				return fmt.Errorf("%d trigger(s) left on the API after destroy", len(api.triggers))
			}

			return nil
		},
		Steps: []resource.TestStep{
			{Config: config},
			{Config: config, Destroy: true},
		},
	})
}

// TestTriggerResourceUnit_ImportPopulatesBothPipelineAttributes covers the one place
// the definition id is written outside Create: the import address.
//
// Both attributes are Computed, so filling only one leaves the other null in state and
// the first plan after an import shows a diff the practitioner cannot resolve — the API
// never returns the definition a trigger belongs to, so no Read can repair it.
func TestTriggerResourceUnit_ImportPopulatesBothPipelineAttributes(t *testing.T) {
	_, host := newFakeTriggerAPI(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: triggerPipelineAttributeConfig(host, "pipeline_id")},
			{
				ResourceName:      "circleci_trigger.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: triggerImportID(),
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported instance, got %d", len(states))
					}

					for _, attr := range []string{"pipeline_id", "pipeline_definition_id"} {
						if got := states[0].Attributes[attr]; got != fakeTriggerPipelineID {
							return fmt.Errorf("imported %s = %q, want %q", attr, got, fakeTriggerPipelineID)
						}
					}

					return nil
				},
			},
		},
	})
}

// TestTriggerResourceUnit_RequiresExactlyOnePipelineAttribute keeps the ambiguous and
// the empty cases plan-time errors. Both set would be a silent choice between two
// definitions; neither leaves the trigger with nothing to attach to, and the failure
// would otherwise surface as a 404 from a route with an empty path segment.
func TestTriggerResourceUnit_RequiresExactlyOnePipelineAttribute(t *testing.T) {
	_, host := newFakeTriggerAPI(t)

	cases := map[string]struct {
		config    string
		wantError *regexp.Regexp
	}{
		"both": {
			config: triggerFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_trigger" "test" {
  project_id                    = %[1]q
  pipeline_id                   = %[2]q
  pipeline_definition_id        = %[2]q
  event_source_provider         = "github_app"
  event_source_repo_external_id = "1234"
  event_preset                  = "all-pushes"
}
`, fakeTriggerProjectID, fakeTriggerPipelineID),
			wantError: regexp.MustCompile(
				`(?s)Invalid Attribute Combination.*pipeline_id,pipeline_definition_id`,
			),
		},
		"neither": {
			config: triggerFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_trigger" "test" {
  project_id                    = %[1]q
  event_source_provider         = "github_app"
  event_source_repo_external_id = "1234"
  event_preset                  = "all-pushes"
}
`, fakeTriggerProjectID),
			wantError: regexp.MustCompile(
				`(?s)Missing Attribute Configuration.*pipeline_id,pipeline_definition_id`,
			),
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      testCase.config,
					ExpectError: testCase.wantError,
				}},
			})
		})
	}
}

// TestTriggerPlanUpgradingFrom04StateDoesNotReplace proves the first plan after
// upgrading does not destroy anybody's triggers.
//
// `pipeline_definition_id` did not exist at v0.4.0, so every trigger in state
// written by that version holds `pipeline_id` and a **null**
// `pipeline_definition_id` — and nothing repairs it, because Read cannot: the API
// never returns the definition a trigger belongs to. Now that a change to either
// name forces replacement, that null is a liability: an attribute planned as
// something other than its (null) prior value on a resource where nothing was
// configured would be read as a change, and answered with a destroy and recreate —
// a new trigger id, and for a webhook event source a new URL, for every trigger in
// the world, on an upgrade that changed no configuration.
//
// Two things prevent it, which is why the case with an unrelated change is here as
// well as the empty one. When anything else in the resource changes, the framework
// rewrites every Computed attribute that is null in configuration to unknown, so the
// planned value is no longer trivially equal to the prior one:
//
//   - UseStateForUnknown writes the prior value back over that unknown — including
//     when the prior value is null, which is the case that matters here.
//   - RequiresReplaceIfConfigured declines regardless, because the configuration
//     value for the name the practitioner did not write is null.
//
// Either alone is sufficient today, so this test does not distinguish
// RequiresReplaceIfConfigured from plain RequiresReplace: with UseStateForUnknown in
// front of it, plain RequiresReplace passes too. Take UseStateForUnknown off and
// they part company — plain RequiresReplace then fails the second case below with
// RequiresReplace = [AttributeName("pipeline_definition_id")], and
// RequiresReplaceIfConfigured still passes. That is the reason for the choice: the
// safety of the deprecation should not rest on the order of two plan modifiers on
// the same attribute.
//
// It drives the real PlanResourceChange RPC because a Terraform-level test cannot
// produce the input: there is no way to write pre-0.5.0-shaped state from a
// configuration, which is exactly why this hazard is easy to miss.
func TestTriggerPlanUpgradingFrom04StateDoesNotReplace(t *testing.T) {
	t.Parallel()

	server := newProviderServerForTest(t)
	schema := resourceSchemaFromServer(t, server, "circleci_trigger")

	objectType, isObject := schema.ValueType().(tftypes.Object)
	if !isObject {
		t.Fatalf("schema type is %T, want tftypes.Object", schema.ValueType())
	}

	str := func(value string) tftypes.Value { return tftypes.NewValue(tftypes.String, value) }
	boolean := func(value bool) tftypes.Value { return tftypes.NewValue(tftypes.Bool, value) }

	// State as v0.4.0 wrote it: pipeline_id set, pipeline_definition_id absent and
	// therefore null under the current schema.
	priorAttributes := map[string]tftypes.Value{
		"id":                            str("22222222-3333-4444-5555-000000000001"),
		"project_id":                    str(fakeTriggerProjectID),
		"pipeline_id":                   str(fakeTriggerPipelineID),
		"created_at":                    str("2024-06-01T00:00:00.000Z"),
		"event_source_provider":         str("github_app"),
		"event_source_repo_external_id": str("1234"),
		"event_source_repo_full_name":   str("acme/api"),
		"event_source_web_hook_url":     str(""),
		"event_preset":                  str("all-pushes"),
		"checkout_ref":                  str("main"),
		"config_ref":                    str("main"),
		"disabled":                      boolean(false),
	}

	// The configuration, still using the deprecated name. Computed-only attributes
	// are null in a configuration, always.
	configAttributes := map[string]tftypes.Value{
		"project_id":                    str(fakeTriggerProjectID),
		"pipeline_id":                   str(fakeTriggerPipelineID),
		"event_source_provider":         str("github_app"),
		"event_source_repo_external_id": str("1234"),
		"event_preset":                  str("all-pushes"),
		"checkout_ref":                  str("main"),
		"config_ref":                    str("main"),
	}

	cases := map[string]struct {
		// change is applied to both the configuration and Terraform's proposed new
		// state, as core would.
		change map[string]tftypes.Value
	}{
		"nothing else changed": {},
		"an unrelated attribute changed in the same plan": {
			change: map[string]tftypes.Value{"disabled": boolean(true)},
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			prior := triggerObjectValue(objectType, priorAttributes)

			// What Terraform core proposes: the configuration, with prior values kept
			// for whatever the configuration leaves null and the schema computes.
			proposed := triggerObjectValue(objectType, merged(priorAttributes, testCase.change))
			config := triggerObjectValue(objectType, merged(configAttributes, testCase.change))

			priorValue, err := tfprotov6.NewDynamicValue(objectType, prior)
			if err != nil {
				t.Fatalf("could not encode prior state: %v", err)
			}
			proposedValue, err := tfprotov6.NewDynamicValue(objectType, proposed)
			if err != nil {
				t.Fatalf("could not encode the proposed new state: %v", err)
			}
			configValue, err := tfprotov6.NewDynamicValue(objectType, config)
			if err != nil {
				t.Fatalf("could not encode configuration: %v", err)
			}

			resp, err := server.PlanResourceChange(t.Context(), &tfprotov6.PlanResourceChangeRequest{
				TypeName:         "circleci_trigger",
				PriorState:       &priorValue,
				ProposedNewState: &proposedValue,
				Config:           &configValue,
			})
			if err != nil {
				t.Fatalf("PlanResourceChange returned an error: %v", err)
			}

			for _, diagnostic := range resp.Diagnostics {
				if diagnostic.Severity == tfprotov6.DiagnosticSeverityError {
					t.Fatalf("planning against v0.4.0-shaped state reported an error: %s: %s",
						diagnostic.Summary, diagnostic.Detail)
				}
			}

			if len(resp.RequiresReplace) != 0 {
				t.Errorf("the plan wants to replace the trigger: RequiresReplace = %v.\n\n"+
					"The definition id did not change: the attribute named in the plan is null in state "+
					"only because it did not exist when that state was written, and no Read can fill it "+
					"in. Replacing here recreates the trigger with a new id and, for a webhook event "+
					"source, a new URL. Use RequiresReplaceIfConfigured.", resp.RequiresReplace)
			}
		})
	}
}

// merged returns base with overrides applied, leaving base untouched.
func merged(base, overrides map[string]tftypes.Value) map[string]tftypes.Value {
	out := make(map[string]tftypes.Value, len(base)+len(overrides))
	for name, value := range base {
		out[name] = value
	}
	for name, value := range overrides {
		out[name] = value
	}

	return out
}

// triggerObjectValue builds a value of the resource's object type: the named
// attributes as given, every other attribute null. State and configuration both
// have to carry every attribute in the schema, whatever their values.
func triggerObjectValue(objectType tftypes.Object, values map[string]tftypes.Value) tftypes.Value {
	attributes := make(map[string]tftypes.Value, len(objectType.AttributeTypes))

	for name, attributeType := range objectType.AttributeTypes {
		if value, given := values[name]; given {
			attributes[name] = value

			continue
		}

		attributes[name] = tftypes.NewValue(attributeType, nil)
	}

	return tftypes.NewValue(objectType, attributes)
}
