// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

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
	return triggerFakeProviderConfig(host) + fmt.Sprintf(`
resource "circleci_trigger" "test" {
  project_id                    = %[1]q
  %[2]s = %[3]q
  event_source_provider         = "github_app"
  event_source_repo_external_id = "ext-1"
  event_preset                  = "all-pushes"
  checkout_ref                  = "main"
  config_ref                    = "main"
}
`, fakeTriggerProjectID, attr, fakeTriggerPipelineID)
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
  event_source_repo_external_id = "ext-1"
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
  event_source_repo_external_id = "ext-1"
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
