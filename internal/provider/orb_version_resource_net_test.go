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

// This file is circleci_orb_version's real-API counterpart to
// orb_version_resource_test.go, which — like every other "TestAcc*" in this
// family — is entirely [FAKE]; see orb_fake_test.go's package comment. Before
// this file, circleci_orb_version had zero live coverage.
//
// WHY THIS FILE DOES NOT PUBLISH A VERSION. Publishing is permanent: there is
// no delete route, a published version's source cannot be edited, and it
// cannot be republished with different source (see PublishOrbVersion's doc
// comment in internal/circleci/orb.go, and this resource's own schema
// description). A live publish test would add one version, forever, to
// whatever orb it targeted, every single time it ran — a cost this gap does
// not need to pay, because the resource's Create path is fully exercisable
// from the API's *rejection* of a republish instead, at zero ongoing cost:
// attempting to publish a version number that already exists on a real orb
// can never succeed, so it can never leave anything behind.
//
// [NET, confirmed 2026-08-27, against gh-oauth-cci-1's own "demo-orb", which
// already had a published "1.0.0"]:
//
//	POST /orb/versions, orb_id=<demo-orb's id>, version="1.0.0" (already published):
//	  400 {"error":{"title":"orb revision already exists"}}
//
// TestAccOrbVersionResourceNet_RejectsRepublishOfAnExistingVersion below
// drives circleci_orb_version itself against exactly that case: it resolves a
// pre-existing orb that already has a published stable version (never one it
// creates), and applies a config that asks the resource to publish that same
// version number again. The API's immutability guarantee is what this test
// leans on for safety — the request is not merely expected to fail, it is
// structurally unable to succeed against a version that already exists — so
// this is safe to run every time, on every CI run, indefinitely, without
// accumulating anything.
//
// This does not prove that publishing a genuinely new version ever succeeds
// end to end; nothing here observes a 201. Unlike circleci_orb's own
// bare-vs-qualified-name bug, circleci_orb_version's Create path has no
// analogous "wrong string sent to the API" failure mode to guard against —
// PublishOrbVersionRequest addresses the orb purely by its UUID (OrbID), never
// by name — so there is no equivalent regression for a rejection-only test
// like this one to catch by design. What it does cheaply confirm, against the
// real API rather than only the fake's model of it, is that Create reaches
// the API at all with the fields this resource says it sends, and that a
// same-version republish is rejected rather than silently accepted or
// corrupting the existing version.
func TestAccOrbVersionResourceNet_RejectsRepublishOfAnExistingVersion(t *testing.T) {
	testAccPreCheck(t)

	orgID := testOrgID(t)
	client := testAccClient(t)
	ctx := context.Background()

	nsName := testRunnerNamespace(t)
	ns, err := client.GetNamespace(ctx, nsName)
	if err != nil {
		t.Fatalf("resolving namespace %q for organization %s: %v", nsName, orgID, err)
	}

	orbs, err := client.ListOrbPackages(ctx, circleci.ListOrbPackagesOptions{NamespaceID: ns.ID})
	if err != nil {
		t.Fatalf("listing orbs in namespace %q: %v", nsName, err)
	}

	// Find any orb in the namespace that already has at least one published
	// stable version — not necessarily the first orb listed, and not one this
	// test creates. See the package comment above for why a new orb or
	// version is never an option here.
	var (
		orbID, orbName string
		version        string
	)
	for i := range orbs {
		versions, vErr := client.ListOrbVersions(ctx, circleci.ListOrbVersionsOptions{
			OrbID:   orbs[i].ID,
			Channel: circleci.OrbChannelStable,
		})
		if vErr != nil {
			t.Fatalf("listing versions of orb %q: %v", orbs[i].Name, vErr)
		}
		if len(versions) > 0 {
			orbID, orbName, version = orbs[i].ID, orbs[i].Name, versions[0].Version

			break
		}
	}
	if orbID == "" {
		t.Skipf(
			"no orb in namespace %q (organization %s) has a published stable version yet for this "+
				"test to collide with, and publishing one here is not an option: an orb version can "+
				"never be deleted or edited, so this test must reuse a pre-existing one rather than "+
				"making a new permanent fixture just for itself",
			nsName, orgID,
		)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "circleci_orb_version" "net_test" {
  orb_id  = %q
  version = %q
  yaml    = "version: 2.1\ndescription: tf-acc-orb-version-net-republish-check\n"
}
`, orbID, version),
			// Measured: `400 {"error":{"title":"orb revision already exists"}}`.
			ExpectError: regexp.MustCompile(`(?s)revision\s+already\s+exists`),
		}},
	})

	// The step above must never reach 201: reconfirm the orb's stable-channel
	// version count is unchanged, so a change to the API's behavior that makes
	// this "republish" request succeed is caught here as a test failure
	// rather than silently adding a duplicate version record next to the
	// original.
	after, err := client.ListOrbVersions(ctx, circleci.ListOrbVersionsOptions{OrbID: orbID, Channel: circleci.OrbChannelStable})
	if err != nil {
		t.Fatalf("re-listing versions of orb %q after the test: %v", orbName, err)
	}

	var stillPresent int
	for _, v := range after {
		if v.Version == version {
			stillPresent++
		}
	}
	if stillPresent != 1 {
		t.Errorf(
			"orb %q now has %d version(s) numbered %q, want exactly 1: the republish request above "+
				"must have been rejected, not accepted", orbName, stillPresent, version,
		)
	}
}
