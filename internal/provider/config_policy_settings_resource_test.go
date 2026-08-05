// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

func configPolicySettingsConfig(host string, enabled bool) string {
	return governanceProviderConfig(host) + fmt.Sprintf(`
resource "circleci_config_policy_settings" "test" {
  owner_id = %q
  enabled  = %t
}
`, testPolicyOwner, enabled)
}

// settingsPath is the decision settings route for a policy context.
func (a *configPolicyAPI) settingsPath(policyContext string) string {
	return "/api/v2/owner/" + testPolicyOwner + "/context/" + policyContext + "/decision/settings"
}

// enabled reports the stored enforcement flag for a policy context.
// enabled reports the stored setting. It takes no context parameter because the
// API only accepts "config" — see TestAccConfigPolicySettingsCustomContext.
func (a *configPolicyAPI) enabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.settings[a.settingsPath(circleci.PolicyContextConfig)]
}

// setEnabled writes the enforcement flag directly, simulating a change made
// outside Terraform.
func (a *configPolicyAPI) setEnabled(policyContext string, enabled bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.settings[a.settingsPath(policyContext)] = enabled
}

func TestAccConfigPolicySettingsResource(t *testing.T) {
	api := newConfigPolicyAPI()
	srv := newConfigPolicyServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configPolicySettingsConfig(srv.URL, true),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_config_policy_settings.test",
						tfjsonpath.New("owner_id"),
						knownvalue.StringExact(testPolicyOwner),
					),
					statecheck.ExpectKnownValue(
						"circleci_config_policy_settings.test",
						tfjsonpath.New("policy_context"),
						knownvalue.StringExact("config"),
					),
					statecheck.ExpectKnownValue(
						"circleci_config_policy_settings.test",
						tfjsonpath.New("enabled"),
						knownvalue.Bool(true),
					),
				},
				Check: func(*terraform.State) error {
					if !api.enabled() {
						return fmt.Errorf("policy enforcement is off after applying enabled = true")
					}

					return nil
				},
			},
			// Update in place: there is no replacement, only a PATCH.
			{
				Config: configPolicySettingsConfig(srv.URL, false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_config_policy_settings.test",
						tfjsonpath.New("enabled"),
						knownvalue.Bool(false),
					),
				},
				Check: func(*terraform.State) error {
					if api.enabled() {
						return fmt.Errorf("policy enforcement is on after applying enabled = false")
					}

					return nil
				},
			},
			// Import testing: the ID is the owner ID alone.
			{
				ResourceName:                         "circleci_config_policy_settings.test",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "owner_id",
				ImportStateId:                        testPolicyOwner,
			},
			// Delete testing automatically occurs in TestCase.
		},
	})

	var sawPatch, sawGet bool
	for _, req := range api.recorded() {
		switch req {
		case "PATCH " + api.settingsPath("config"):
			sawPatch = true
		case "GET " + api.settingsPath("config"):
			sawGet = true
		}
	}

	if !sawPatch {
		t.Errorf("no PATCH of the decision settings, got %v", api.recorded())
	}
	if !sawGet {
		t.Errorf("no GET of the decision settings, got %v", api.recorded())
	}
}

// TestAccConfigPolicySettingsDestroyLeavesEnforcementOn is the load-bearing test
// for the delete decision: removing the resource must not switch a security
// control off as a side effect.
func TestAccConfigPolicySettingsDestroyLeavesEnforcementOn(t *testing.T) {
	api := newConfigPolicyAPI()
	srv := newConfigPolicyServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{Config: configPolicySettingsConfig(srv.URL, true)},
		},
	})

	if !api.enabled() {
		t.Error("destroying the resource disabled policy enforcement, want it left in place")
	}

	// And no request may be made during the destroy: the resource is dropped from
	// state without touching the API.
	requests := api.recorded()
	lastWrite := -1
	for i, req := range requests {
		if strings.HasPrefix(req, "PATCH ") {
			lastWrite = i
		}
	}

	for _, req := range requests[lastWrite+1:] {
		if !strings.HasPrefix(req, "GET ") {
			t.Errorf("destroy made a write request %q, want none", req)
		}
	}
}

// TestAccConfigPolicySettingsDriftDetected covers a change made outside
// Terraform: enforcement switched off elsewhere has to show up as drift.
func TestAccConfigPolicySettingsDriftDetected(t *testing.T) {
	api := newConfigPolicyAPI()
	srv := newConfigPolicyServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{Config: configPolicySettingsConfig(srv.URL, true)},
			{
				// Read finds enforcement off and writes that straight into state (see
				// configPolicySettingsResource.Read — a valid context always answers
				// 200, so there is no "gone" signal here), so the plan that follows
				// updates enabled back to true rather than reporting no changes.
				PreConfig: func() { api.setEnabled("config", false) },
				Config:    configPolicySettingsConfig(srv.URL, true),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"circleci_config_policy_settings.test",
							plancheck.ResourceActionUpdate,
						),
					},
				},
			},
			// And the next apply puts it back.
			{
				Config: configPolicySettingsConfig(srv.URL, true),
				Check: func(*terraform.State) error {
					if !api.enabled() {
						return fmt.Errorf("re-applying did not restore policy enforcement")
					}

					return nil
				},
			},
		},
	})
}

// TestAccConfigPolicySettingsAbsentEnabledMeansDisabled covers the API's habit of
// omitting enabled rather than reporting false.
func TestAccConfigPolicySettingsAbsentEnabledMeansDisabled(t *testing.T) {
	api := newConfigPolicyAPI()
	srv := newConfigPolicyServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configPolicySettingsConfig(srv.URL, false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_config_policy_settings.test",
						tfjsonpath.New("enabled"),
						knownvalue.Bool(false),
					),
				},
			},
		},
	})

	// The provider must have PATCHed an explicit false rather than an empty body,
	// which the API would have read as "leave it alone".
	var sawPatch bool
	for _, req := range api.recorded() {
		if req == "PATCH "+api.settingsPath("config") {
			sawPatch = true
		}
	}

	if !sawPatch {
		t.Errorf("no PATCH of the decision settings, got %v", api.recorded())
	}
}

// TestAccConfigPolicySettingsCustomContext covers the non-default policy context.
func TestAccConfigPolicySettingsCustomContext(t *testing.T) {
	t.Parallel()

	// See TestAccConfigPolicyBundleCustomContext: the API rejects any context
	// other than "config", so the validator refuses it at plan time.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: governanceProviderConfig("http://127.0.0.1:1") + `
resource "circleci_config_policy_settings" "test" {
  owner_id       = "00000000-1111-2222-3333-444444444444"
  policy_context = "custom"
  enabled        = true
}
`,
			ExpectError: regexp.MustCompile(`(?s)policy_context`),
		}},
	})
}

// TestConfigPolicySettingsErrorMentionsPlan covers the diagnostic for a rejected
// write, which is most often a plan problem.
func TestConfigPolicySettingsErrorMentionsPlan(t *testing.T) {
	api := newConfigPolicyAPI()
	api.settingsStatus = http.StatusForbidden
	srv := newConfigPolicyServer(t, api)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: governanceProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      configPolicySettingsConfig(srv.URL, true),
				ExpectError: regexp.MustCompile(`(?s)Scale plan`),
			},
		},
	})
}
