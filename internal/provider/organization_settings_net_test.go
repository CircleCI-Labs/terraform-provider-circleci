// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

// This file is the real-API counterpart to organization_settings_resource_test.go,
// which is entirely [FAKE]. The earlier one-time pass deliberately never wrote
// to this resource against a real organization — TESTING.md lists
// circleci_organization_settings among the resources with an "org-wide ...
// blast radius" — which was the right call, but it left the write path with no
// real-API coverage at all. This file adds that coverage while keeping the
// blast radius to toggles that cannot affect anything else running against
// these shared fixture organizations.
//
// TOGGLE SELECTION. Of the fifteen modelled toggles, only enable_minor_ai_features
// and enable_ai_error_summarization are ever written here. Every other toggle is
// deliberately left untouched, because it gates something else in this
// provider's own test suite or in CircleCI's platform behavior for the
// organization as a whole:
//
//   - is_context_group_restriction_required and is_runner_terms_of_service_accepted:
//     the task that produced this file explicitly names these as off-limits
//     while other agents may be working in these organizations (context
//     restrictions and runner onboarding).
//   - is_running_disabled: setting this true halts every pipeline in the
//     organization — an extreme, organization-wide blast radius no other test
//     anywhere in this suite could tolerate sharing an organization with.
//   - is_user_checkout_keys_disabled: circleci_checkout_key's own acceptance
//     tests (checkout_key_net_test.go) depend on user checkout keys working in
//     this same organization.
//   - enable_private_orbs, enable_certified_public_orbs, enable_uncertified_public_orbs:
//     this suite has separate orb-family acceptance coverage that depends on
//     orb visibility rules not moving under it mid-run.
//   - enable_image_brownouts, enable_resource_class_brownouts: these can make a
//     pipeline that would otherwise succeed start failing during a brownout
//     window, which is exactly the kind of interference a pipeline- or
//     resource-class-running test elsewhere in this suite must not have to
//     tolerate.
//   - enable_unversioned_config: governs whether a pipeline can be triggered
//     via the API with inline configuration, which pipeline/trigger
//     acceptance tests elsewhere may depend on.
//   - is_bitbucket_workspace_member_org_member: inert for every fixture
//     organization this suite has (none is Bitbucket-linked), so testing it
//     here would prove nothing; left alone rather than flipped for no reason.
//
// enable_minor_ai_features and enable_ai_error_summarization gate nothing else
// in this codebase: they are UI-only conveniences with no interaction with any
// other resource this provider manages. That is what makes them safe to flip
// concurrently with whatever else is running against the same organization.
//
// SAFETY / CONCURRENCY. Both toggles' pre-test values are captured and
// restored in t.Cleanup regardless of how the test steps go, with any restore
// failure reported via t.Errorf rather than swallowed. A third toggle
// (enable_private_orbs) is captured too, purely as a read-only witness: it is
// never written by this test, and the test fails if it ever changes anyway,
// which is what proves the provider is sending only the toggles the
// configuration actually sets rather than the whole settings object.
func TestAccOrganizationSettingsResourceNet_Lifecycle(t *testing.T) {
	testAccPreCheck(t)
	orgID := testOrgID(t)
	// Organization settings behave identically regardless of VCS integration
	// or organization class — TESTING.md and the task brief both record that
	// every organization of both classes answers with all fifteen toggles
	// present and non-null.
	testRequireVCSType(t, acceptanceVCSTypes...)

	client := circleci.New(circleci.Config{Token: os.Getenv("CIRCLE_TOKEN")})
	ctx := context.Background()

	before, err := client.GetOrganizationSettings(ctx, orgID)
	if err != nil {
		t.Fatalf("reading the pre-existing organization settings for %s: %v", orgID, err)
	}

	originalMinorAI := boolValue(before.EnableMinorAIFeatures)
	originalErrorSummarization := boolValue(before.EnableAIErrorSummarization)
	originalPrivateOrbs := before.EnablePrivateOrbs

	t.Cleanup(func() {
		restore := circleci.OrganizationSettings{
			EnableMinorAIFeatures:      &originalMinorAI,
			EnableAIErrorSummarization: &originalErrorSummarization,
		}
		if _, err := client.UpdateOrganizationSettings(context.Background(), orgID, restore); err != nil {
			t.Errorf("restoring organization settings for %s after the test: %v", orgID, err)
		}
	})

	notMinorAI := !originalMinorAI
	notErrorSummarization := !originalErrorSummarization

	managed := func(minorAI, errorSummarization bool) string {
		return fmt.Sprintf(`
resource "circleci_organization_settings" "net_test" {
  organization_id                = %q
  enable_minor_ai_features       = %t
  enable_ai_error_summarization  = %t
}
`, orgID, minorAI, errorSummarization)
	}

	// bare manages the organization but no toggle at all, which is exactly
	// the state ImportState below always produces (see
	// organizationSettingsResource.ImportState's own comment: every toggle
	// is deliberately left null on import). Applying it after the two
	// managed-toggle steps below abandons them, putting the primary state
	// into that same all-toggles-null shape so the subsequent import step
	// can assert a genuine, ignore-free round trip instead of working
	// around the mismatch with ImportStateVerifyIgnore.
	bare := fmt.Sprintf(`
resource "circleci_organization_settings" "net_test" {
  organization_id = %q
}
`, orgID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: managed(notMinorAI, notErrorSummarization),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_organization_settings.net_test", tfjsonpath.New("enable_minor_ai_features"),
						knownvalue.Bool(notMinorAI),
					),
					statecheck.ExpectKnownValue(
						"circleci_organization_settings.net_test", tfjsonpath.New("enable_ai_error_summarization"),
						knownvalue.Bool(notErrorSummarization),
					),
				},
				Check: func(*terraform.State) error {
					got, err := client.GetOrganizationSettings(ctx, orgID)
					if err != nil {
						return fmt.Errorf("reading live organization settings: %w", err)
					}
					if boolValue(got.EnableMinorAIFeatures) != notMinorAI {
						return fmt.Errorf(
							"live enable_minor_ai_features = %v, want %v", boolValue(got.EnableMinorAIFeatures), notMinorAI,
						)
					}
					if boolValue(got.EnableAIErrorSummarization) != notErrorSummarization {
						return fmt.Errorf(
							"live enable_ai_error_summarization = %v, want %v",
							boolValue(got.EnableAIErrorSummarization), notErrorSummarization,
						)
					}
					// The unmanaged witness toggle must be untouched: only the
					// toggles the configuration sets may ever be written (see
					// OrganizationSettings' omitempty-pointer doc comment).
					if boolValue(got.EnablePrivateOrbs) != boolValue(originalPrivateOrbs) {
						return fmt.Errorf(
							"enable_private_orbs changed from %v to %v; this resource must send only the "+
								"toggles its configuration sets", boolValue(originalPrivateOrbs), boolValue(got.EnablePrivateOrbs),
						)
					}

					return nil
				},
			},
			// Update in place: every setting is updatable, so this must not be a
			// replacement. Flips both toggles back to their original values.
			{
				Config: managed(originalMinorAI, originalErrorSummarization),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_organization_settings.net_test", tfjsonpath.New("enable_minor_ai_features"),
						knownvalue.Bool(originalMinorAI),
					),
					statecheck.ExpectKnownValue(
						"circleci_organization_settings.net_test", tfjsonpath.New("enable_ai_error_summarization"),
						knownvalue.Bool(originalErrorSummarization),
					),
				},
			},
			// Abandon both toggles: see bare's own comment above.
			{
				Config: bare,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_organization_settings.net_test", tfjsonpath.New("enable_minor_ai_features"), knownvalue.Null(),
					),
					statecheck.ExpectKnownValue(
						"circleci_organization_settings.net_test", tfjsonpath.New("enable_ai_error_summarization"), knownvalue.Null(),
					),
				},
			},
			// Import round-trips to an empty plan with no ImportStateVerifyIgnore
			// at all: both the applied state (bare, above) and the imported state
			// have every toggle null, which is the only shape Import can ever
			// produce, so there is nothing here for an ignore list to paper over.
			{
				ResourceName:                         "circleci_organization_settings.net_test",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "organization_id",
				ImportStateId:                        orgID,
			},
			// Delete testing (a documented no-op that leaves every value as it
			// stands) automatically occurs at the end of TestCase.
		},
	})
}

// boolValue reports the value of a *bool, treating nil as false. Every
// production response for this resource carries non-nil pointers (see
// OrganizationSettings' doc comment: the API always answers with all fifteen
// toggles present), so nil here would itself be a surprise worth surfacing via
// a wrong comparison rather than a panic.
func boolValue(v *bool) bool {
	return v != nil && *v
}
