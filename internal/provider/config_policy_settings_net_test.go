// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

// This file is the real-API counterpart to config_policy_settings_resource_test.go,
// which is entirely [FAKE]. circleci_config_policy_settings is a singleton
// settings object per (owner_id, policy_context) — there is nothing to create
// or delete, only a value to flip — so the safety concern here is different
// from the bundle resource: it is "leave the organization's enforcement flag
// exactly where this test found it", not "don't destroy something you didn't
// create".
//
// SAFETY / CONCURRENCY. The pre-test value is captured and restored in
// t.Cleanup regardless of how the test steps go, and any restore failure is
// reported (t.Errorf), never swallowed. Safe against a different
// integration's copy of this test: owner_id differs. Safe against itself
// after a crash: a crashed run may have left enforcement at whatever its last
// successful write was, but that is the same "the org is not necessarily back
// at some hypothetical origin" limitation every boolean-toggle acceptance test
// like this one has — this run still captures and restores relative to
// whatever it actually found, which is the honest thing to do. The bundle
// this setting would enforce is empty for the duration of this run in
// practice (see config_policy_bundle_net_test.go, which restores its own
// bundle in cleanup and does not leave a bundle behind for other tests to
// interact with), so flipping this flag has no blast radius even while it is
// live.
func TestAccConfigPolicySettingsResourceNet_Lifecycle(t *testing.T) {
	testAccPreCheck(t)
	ownerID := testOrgID(t)
	// This resource behaves identically regardless of VCS integration or
	// organization class — it is keyed purely on owner_id.
	testRequireVCSType(t, acceptanceVCSTypes...)

	client := policyBundleTestClient()
	ctx := context.Background()

	before, err := client.GetPolicyDecisionSettings(ctx, ownerID, circleci.PolicyContextConfig)
	if err != nil {
		t.Fatalf("reading the pre-existing policy decision settings for organization %s: %v", ownerID, err)
	}
	originalEnabled := before.Enabled != nil && *before.Enabled

	t.Cleanup(func() {
		restore := originalEnabled
		if _, err := client.SetPolicyDecisionSettings(
			context.Background(), ownerID, circleci.PolicyContextConfig,
			circleci.PolicyDecisionSettings{Enabled: &restore},
		); err != nil {
			t.Errorf(
				"restoring policy decision settings (enabled=%v) for organization %s after the test: %v",
				restore, ownerID, err,
			)
		}
	})

	config := func(enabled bool) string {
		return fmt.Sprintf(`
resource "circleci_config_policy_settings" "net_test" {
  owner_id = %q
  enabled  = %t
}
`, ownerID, enabled)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(true),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_config_policy_settings.net_test", tfjsonpath.New("enabled"), knownvalue.Bool(true),
					),
				},
				Check: func(*terraform.State) error {
					got, err := client.GetPolicyDecisionSettings(ctx, ownerID, circleci.PolicyContextConfig)
					if err != nil {
						return fmt.Errorf("reading live decision settings: %w", err)
					}
					if got.Enabled == nil || !*got.Enabled {
						return fmt.Errorf("live decision settings enabled = %v, want true", got.Enabled)
					}

					return nil
				},
			},
			// Update in place: there is no replacement, only a PATCH.
			{
				Config: config(false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_config_policy_settings.net_test", tfjsonpath.New("enabled"), knownvalue.Bool(false),
					),
				},
				Check: func(*terraform.State) error {
					got, err := client.GetPolicyDecisionSettings(ctx, ownerID, circleci.PolicyContextConfig)
					if err != nil {
						return fmt.Errorf("reading live decision settings: %w", err)
					}
					if got.Enabled == nil || *got.Enabled {
						return fmt.Errorf("live decision settings enabled = %v, want false", got.Enabled)
					}

					return nil
				},
			},
			{
				ResourceName:                         "circleci_config_policy_settings.net_test",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "owner_id",
				ImportStateId:                        ownerID,
			},
			// Delete testing (a documented no-op that leaves enforcement in
			// place) automatically occurs at the end of TestCase.
		},
	})

	// TestCase's own destroy leaves enforcement at whatever the last apply set
	// (false, from the step above) — that is this resource's documented
	// behavior (configPolicySettingsResource.Delete never calls the API). The
	// t.Cleanup registered above still restores the value this test found
	// before it started, which may differ from false.
	after, err := client.GetPolicyDecisionSettings(ctx, ownerID, circleci.PolicyContextConfig)
	if err != nil {
		t.Fatalf("reading policy decision settings after destroy: %v", err)
	}
	if after.Enabled == nil || *after.Enabled {
		t.Errorf(
			"destroy left enabled=%v; this resource's Delete makes no API call, so it should have left "+
				"enabled at the last value this test applied (false)", after.Enabled,
		)
	}
}
