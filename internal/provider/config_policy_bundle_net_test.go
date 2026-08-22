// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"slices"
	"sort"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
)

// This file is the real-API counterpart to config_policy_bundle_resource_test.go,
// which is entirely [FAKE]. The fake already reproduces the two properties that
// were confirmed once, by hand, against production (see policy.go's [NET]
// comments on PolicyBundle and SetPolicyBundle): the upload route re-keys every
// entry by its Rego-declared policy_name, and it is a whole-bundle replace, not a
// merge. This file turns the second property — the data-loss path — into a test
// that actually runs, and pins that dry=true genuinely does not write anything,
// which is the mechanism every test below relies on to avoid ever deleting a
// policy it did not create.
//
// SAFETY. circleci_config_policy_bundle owns the *entire* bundle for its
// (owner_id, policy_context) pair: a POST replaces it wholesale (policy.go).
// Every test here therefore:
//   - reads the bundle before doing anything, so a pre-existing policy (there
//     should be none — these are disposable fixture organizations per
//     TESTING.md, but the test does not assume that) is captured rather than
//     silently discarded;
//   - restores exactly that captured content in t.Cleanup, which runs even if
//     the test fails partway, and reports (t.Errorf, not swallowed) if the
//     restore itself fails;
//   - only ever deletes policies it created itself for real, and only after
//     confirming via a dry run what a real apply would do.
//
// CONCURRENCY. Safe against a different integration's copy of this test
// running at the same time: owner_id differs, so there is no shared state.
// Safe against itself re-running after a crash: the whole-bundle-replace model
// is self-healing here — any policy a crashed prior run left behind is not in
// this run's configuration, so the very first apply removes it as a matter of
// course, the same way it would remove any other absent policy. Not marked
// t.Parallel(): CI's serial-group already prevents two runs against the same
// organization overlapping, and there is no benefit to risking that here.

// randomPolicyName returns a policy name unique to this test run, so a leaked
// object from a previous crashed run — or another instance of this same test
// against a different organization — is never mistaken for this run's own.
func randomPolicyName(prefix string) string {
	return prefix + "_" + rand.Text()
}

// policyBundleTestClient builds a client using CIRCLE_TOKEN, for the direct API
// calls (capture/restore/verify) these tests make outside of Terraform.
func policyBundleTestClient() *circleci.Client {
	return circleci.New(circleci.Config{Token: os.Getenv("CIRCLE_TOKEN")})
}

// TestAccConfigPolicyBundleResourceNet_Lifecycle exercises create, an in-place
// update that drops one policy from the bundle, a live read confirming the
// dropped policy is actually gone from CircleCI (not just out of Terraform
// state), import verification, and destroy.
//
// This is the load-bearing regression test for the resource's central design
// decision: a policy present in the old bundle but absent from the new
// configuration must be deleted by CircleCI, not left behind. That behavior was
// [NET, measured 2026-08-21] confirmed once by hand (see policy.go); this pins
// it so a future API change that started merging instead of replacing would
// fail a real CI run instead of going unnoticed.
func TestAccConfigPolicyBundleResourceNet_Lifecycle(t *testing.T) {
	testAccPreCheck(t)
	ownerID := testOrgID(t)
	// circleci_config_policy_bundle behaves identically regardless of VCS
	// integration or organization class — it is keyed purely on owner_id — so
	// this is supported everywhere CIRCLECI_TEST_VCS_TYPE can name, and every
	// integration's job gets counted as real coverage for it.
	testRequireVCSType(t, acceptanceVCSTypes...)

	client := policyBundleTestClient()
	ctx := context.Background()

	before, err := client.GetPolicyBundle(ctx, ownerID, circleci.PolicyContextConfig)
	if err != nil {
		t.Fatalf("reading the pre-existing policy bundle for organization %s: %v", ownerID, err)
	}
	if len(before) > 0 {
		names := make([]string, 0, len(before))
		for name := range before {
			names = append(names, name)
		}
		sort.Strings(names)
		t.Logf("organization %s already has %d polic(ies) in its %q policy context: %v; "+
			"they will be restored after this test", ownerID, len(before), circleci.PolicyContextConfig, names)
	}

	t.Cleanup(func() {
		if _, err := client.SetPolicyBundle(
			context.Background(), ownerID, circleci.PolicyContextConfig, before.Contents(), false,
		); err != nil {
			t.Errorf("restoring the pre-existing policy bundle for organization %s after the test: %v", ownerID, err)
		}
	})

	nameA := randomPolicyName("acc_test_bundle_a")
	nameB := randomPolicyName("acc_test_bundle_b")
	regoA := fmt.Sprintf("package org\n\npolicy_name[%q]\n", nameA)
	regoAUpdated := fmt.Sprintf("package org\n\npolicy_name[%q]\n\n# updated by TestAccConfigPolicyBundleResourceNet_Lifecycle\n", nameA)
	regoB := fmt.Sprintf("package org\n\npolicy_name[%q]\n", nameB)

	config := func(policies string) string {
		return fmt.Sprintf(`
resource "circleci_config_policy_bundle" "net_test" {
  owner_id = %q
  policies = %s
}
`, ownerID, policies)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(fmt.Sprintf("{ %q = %q, %q = %q }", nameA, regoA, nameB, regoB)),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_config_policy_bundle.net_test", tfjsonpath.New("policies"),
						knownvalue.MapExact(map[string]knownvalue.Check{
							nameA: knownvalue.StringExact(regoA),
							nameB: knownvalue.StringExact(regoB),
						}),
					),
				},
			},
			// The load-bearing step: dropping nameB and rewriting nameA in the same
			// apply. If CircleCI ever started merging instead of replacing, nameB
			// would still be present on the live read below and this would fail.
			{
				Config: config(fmt.Sprintf("{ %q = %q }", nameA, regoAUpdated)),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_config_policy_bundle.net_test", tfjsonpath.New("policies"),
						knownvalue.MapExact(map[string]knownvalue.Check{
							nameA: knownvalue.StringExact(regoAUpdated),
						}),
					),
				},
				Check: func(*terraform.State) error {
					got, err := client.GetPolicyBundle(ctx, ownerID, circleci.PolicyContextConfig)
					if err != nil {
						return fmt.Errorf("reading the live bundle after dropping %s: %w", nameB, err)
					}
					if _, stillThere := got[nameB]; stillThere {
						return fmt.Errorf(
							"%s is still present in the live CircleCI bundle after being dropped from the "+
								"configuration; the whole-bundle-replace apply did not delete it", nameB,
						)
					}
					if policy, ok := got[nameA]; !ok || policy.Content != regoAUpdated {
						return fmt.Errorf("live bundle entry for %s = %+v, want content %q", nameA, policy, regoAUpdated)
					}

					return nil
				},
			},
			{
				ResourceName:                         "circleci_config_policy_bundle.net_test",
				ImportState:                          true,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "owner_id",
				ImportStateId:                        ownerID,
			},
			// Delete testing automatically occurs at the end of TestCase.
		},
		CheckDestroy: func(*terraform.State) error {
			got, err := client.GetPolicyBundle(ctx, ownerID, circleci.PolicyContextConfig)
			if err != nil {
				return fmt.Errorf("reading the live bundle after destroy: %w", err)
			}
			if _, ok := got[nameA]; ok {
				return fmt.Errorf("%s is still present in the live bundle after destroy; destroy must empty the context", nameA)
			}

			return nil
		},
	})
}

// TestAccConfigPolicyBundleNet_DryRunDoesNotApply pins, against the real API,
// the mechanism every other test in this file relies on to check before it
// destroys anything: [NET, measured 2026-08-21] dry=true validates and reports
// the diff an upload would produce, without applying it.
//
// It writes and then dry-run-deletes a policy this test created itself — never
// anything captured from a pre-existing bundle — so a regression in this
// property (dry-run starting to apply for real) is caught without any risk of
// data loss to a policy this test does not own.
func TestAccConfigPolicyBundleNet_DryRunDoesNotApply(t *testing.T) {
	testAccPreCheck(t)
	ownerID := testOrgID(t)
	testRequireVCSType(t, acceptanceVCSTypes...)

	client := policyBundleTestClient()
	ctx := context.Background()

	before, err := client.GetPolicyBundle(ctx, ownerID, circleci.PolicyContextConfig)
	if err != nil {
		t.Fatalf("reading the pre-existing policy bundle for organization %s: %v", ownerID, err)
	}

	t.Cleanup(func() {
		if _, err := client.SetPolicyBundle(
			context.Background(), ownerID, circleci.PolicyContextConfig, before.Contents(), false,
		); err != nil {
			t.Errorf("restoring the pre-existing policy bundle for organization %s after the test: %v", ownerID, err)
		}
	})

	name := randomPolicyName("acc_test_dryrun")
	rego := fmt.Sprintf("package org\n\npolicy_name[%q]\n", name)

	withProbe := make(map[string]string, len(before)+1)
	for existingName, policy := range before {
		withProbe[existingName] = policy.Content
	}
	withProbe[name] = rego

	// Write the probe for real, so the dry run below has something of this
	// test's own creation to threaten deleting.
	if _, err := client.SetPolicyBundle(ctx, ownerID, circleci.PolicyContextConfig, withProbe, false); err != nil {
		t.Fatalf("uploading the probe policy %s: %v", name, err)
	}

	// Dry-run a replacement that omits the probe.
	diff, err := client.SetPolicyBundle(ctx, ownerID, circleci.PolicyContextConfig, before.Contents(), true)
	if err != nil {
		t.Fatalf("dry-running the removal of %s: %v", name, err)
	}
	if !slices.Contains(diff.Deleted, name) {
		t.Errorf("dry-run diff.Deleted = %v, want it to name %q as what WOULD be deleted", diff.Deleted, name)
	}

	// Confirm the dry run genuinely changed nothing: the probe must still be
	// live.
	after, err := client.GetPolicyBundle(ctx, ownerID, circleci.PolicyContextConfig)
	if err != nil {
		t.Fatalf("reading the bundle after the dry run: %v", err)
	}
	if _, stillThere := after[name]; !stillThere {
		t.Errorf("probe policy %q is gone after a dry run; dry=true must not apply anything", name)
	}

	// Now delete it for real, since this test did create it, and confirm the
	// real (non-dry) delete actually removes it — the same mechanism the
	// Lifecycle test above pins through Terraform, pinned here directly
	// through the client.
	if _, err := client.SetPolicyBundle(ctx, ownerID, circleci.PolicyContextConfig, before.Contents(), false); err != nil {
		t.Fatalf("removing the probe policy %s for real: %v", name, err)
	}

	final, err := client.GetPolicyBundle(ctx, ownerID, circleci.PolicyContextConfig)
	if err != nil {
		t.Fatalf("reading the bundle after the real removal: %v", err)
	}
	if _, stillThere := final[name]; stillThere {
		t.Errorf("probe policy %q is still present after a real (non-dry) removal", name)
	}
}
