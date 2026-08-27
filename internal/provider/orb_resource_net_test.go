// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"terraform-provider-circleci/internal/circleci"
)

// This file is circleci_orb's real-API counterpart to orb_resource_test.go,
// which — like every other "TestAcc*" in this family — is entirely [FAKE]; see
// orb_fake_test.go's package comment. Before this file, circleci_orb had zero
// live coverage, which is exactly how its core bug shipped: an earlier version
// of orb_resource.go sent the fully qualified "<namespace>/<orb>" name to
// POST /orb/packages, and every account this provider ever ran against
// answered a generic 400 "this name is invalid" — indistinguishable, from the
// fake, from a real name problem. See CreateOrbPackage's doc comment in
// internal/circleci/orb.go for the fix and the [NET] investigation that found
// it.
//
// WHY THIS FILE DOES NOT CREATE AN ORB. `DELETE /orb/packages/{id}` is a
// router-level 404 (see orb_resource.go's own schema description): there is no
// way to undo a create, ever, against any organization. A live create test
// would therefore need to run exactly once, against exactly one throwaway
// name, in exactly one namespace, forever — which is a cost this gap does not
// need to pay, because the same regression is fully observable from the
// API's *rejection* path instead, at zero cost:
//
//   - Sending the qualified form ("<namespace>/<orb>") answers 400 "this name
//     is invalid...".
//   - Sending a bare name that already belongs to an existing orb answers a
//     DIFFERENT 400: "an Orb with that name already exists."
//
// [NET, confirmed 2026-08-27, against gh-oauth-cci-1's own namespace/orb]:
//
//	POST /orb/packages, name="gh-oauth-cci-1/tf-acc-orb-regression-check" (new, qualified):
//	  400 {"error":{"title":"Cannot create an Orb named
//	  'gh-oauth-cci-1/tf-acc-orb-regression-check': this name is invalid. See
//	  the documentation for more information about the restrictions on Orb
//	  names."}}
//	POST /orb/packages, name="demo-orb" (bare, already exists in that namespace):
//	  400 {"error":{"title":"Cannot create an Orb named 'demo-orb': an Orb
//	  with that name already exists."}}
//
// TestAccOrbResourceNet_DuplicateNameNamesTheExistingOrb below drives
// circleci_orb itself (not the bare circleci client) against the second case.
// If the qualified-name bug were ever reintroduced, orb_resource.go would once
// again send "<namespace>/demo-orb" instead of "demo-orb", the API would
// answer the FIRST message above instead of the second, and this test's
// ExpectError — which requires "already exists" — would fail to match, for
// exactly the reason that matters: the two failure messages are the tell, not
// an incidental detail. What this test does NOT prove is that create ever
// succeeds end to end; nothing here observes a 201.
//
// WHY THIS RUNS ONLY AGAINST TWO OF THE FOUR FIXTURE ORGANIZATIONS. Creating
// ANY uncertified public orb — regardless of name — 400s with a different,
// entitlement error ("To create uncertified public orbs, your organization
// must enable the 'Allow uncertified public orbs' feature in Org Settings >
// Security") when circleci_organization_settings.enable_uncertified_public_orbs
// is false, and that check runs BEFORE the name check this test needs to
// observe. [NET, confirmed 2026-08-27] measured against this suite's four
// fixture organizations: true today only on the two classic GitHub OAuth
// orgs (gh-oauth-cci-1, gh-oauth-cci-2 — github_oauth and github_hybrid);
// false on the two standalone orgs (gh-app-cci-1, gitlab-test). The test below
// reads the live setting itself and skips with that finding named, rather than
// gating on VCS type — VCS integration is not actually what this setting
// depends on, it just happens to line up that way across this suite's four
// fixtures today. See organization_settings_net_test.go's own header comment
// for why that test deliberately never writes this toggle: this suite already
// has orb-family live coverage (this file) that depends on it not moving
// mid-run, and flipping it via the capture-restore pattern shown there was
// considered and rejected here — the two organizations that already have it
// enabled are sufficient, so there was nothing to buy by mutating a
// third/fourth organization's org-wide setting just to reach the same
// assertion.
//
// WHY THIS DOES NOT HARDCODE THE EXISTING ORB'S NAME. "demo-orb" is what both
// qualifying organizations happen to already own (found by listing, not
// assumed), left over from the investigation that diagnosed the original bug.
// Encoding that literal name here would make the test pass or fail on an
// accident of history; instead it lists the namespace's orbs at run time and
// uses whichever one it finds, so it keeps working if that orb is ever
// (somehow) renamed-by-recreation or a different one is used instead — and
// skips, naming why, if the namespace one day owns none at all.
func TestAccOrbResourceNet_DuplicateNameNamesTheExistingOrb(t *testing.T) {
	testAccPreCheck(t)

	orgID := testOrgID(t)
	client := testAccClient(t)
	ctx := context.Background()

	settings, err := client.GetOrganizationSettings(ctx, orgID)
	if err != nil {
		t.Fatalf("reading organization settings for %s: %v", orgID, err)
	}
	if !boolValue(settings.EnableUncertifiedPublicOrbs) {
		t.Skipf(
			"organization %s has enable_uncertified_public_orbs=false: creating any public orb "+
				"there 400s on that entitlement check before the API ever reaches the name-validity "+
				"check this test needs to observe. Of this suite's four fixture organizations, only "+
				"the two classic GitHub OAuth ones (github_oauth, github_hybrid) have it enabled today.",
			orgID,
		)
	}

	nsName := testRunnerNamespace(t)
	ns, err := client.GetNamespace(ctx, nsName)
	if err != nil {
		t.Fatalf("resolving namespace %q for organization %s: %v", nsName, orgID, err)
	}

	existing, err := client.ListOrbPackages(ctx, circleci.ListOrbPackagesOptions{NamespaceID: ns.ID})
	if err != nil {
		t.Fatalf("listing orbs in namespace %q: %v", nsName, err)
	}
	if len(existing) == 0 {
		t.Skipf(
			"namespace %q (organization %s) owns no orb yet for this test to collide with, and "+
				"creating one here is not an option: an orb can never be deleted, so this test must "+
				"reuse a pre-existing one rather than making a new permanent fixture just for itself",
			nsName, orgID,
		)
	}

	bareName := orbBareName(existing[0].Name, nsName)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "circleci_orb" "net_test" {
  namespace_id = %q
  name         = %q
}
`, ns.ID, bareName),
			// The measured message names the rejected name before saying why:
			// `Cannot create an Orb named 'X': an Orb with that name already
			// exists.` Terraform's own error rendering can wrap the message
			// onto a new line, hence \s+ rather than a literal space, and
			// (?s) so "." also matches a wrapped newline between the name and
			// "already exists".
			ExpectError: regexp.MustCompile(
				`(?s)` + regexp.QuoteMeta(bareName) + `.*already\s+exists`,
			),
		}},
	})

	// The step above must never reach 201: reconfirm the namespace still owns
	// exactly the orbs it started with, so a change to the API's behavior that
	// makes this "duplicate" request succeed is caught here as a test failure
	// rather than silently adding a second permanent orb next to the first.
	after, err := client.ListOrbPackages(ctx, circleci.ListOrbPackagesOptions{NamespaceID: ns.ID})
	if err != nil {
		t.Fatalf("re-listing orbs in namespace %q after the test: %v", nsName, err)
	}
	if len(after) != len(existing) {
		t.Errorf(
			"namespace %q now owns %d orbs, want %d (unchanged): the duplicate-name request above "+
				"must have been rejected, not accepted", nsName, len(after), len(existing),
		)
	}
}
