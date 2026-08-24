// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

// The tests in this file are the harness's own end-to-end proof, live
// against whatever organization CIRCLECI_TEST_VCS_TYPE currently points at.
// Each one:
//
//   - names its object with testUniqueName, so it cannot collide with
//     another concurrent run of the SAME test, a DIFFERENT acceptance job
//     running against the same or a different organization at the same
//     instant, or a run that died before cleaning up after itself;
//   - registers its own delete with testRegisterCleanup immediately after
//     create succeeds, rather than relying on a shared fixture someone else
//     also writes to;
//   - proves the object is really gone with the list-based check
//     (testAssertContextGone / testAssertGroupGone / testAssertProjectGone),
//     not a single status code.
//
// They create their own project, context or group in the organization under
// test rather than reading testProjectID/testContextID/a shared group,
// which is the "no shared mutable fixture" property in practice: nothing
// here can be broken by, or break, another test that also uses this
// organization's shared fixtures.
//
// None of the three is named TestAcc*, even though every one of them is
// live against a real organization: that prefix is reserved for a test
// driven through the plugin-testing framework's own runner (resource.Test,
// resource.UnitTest or resource.ParallelTest — see
// TestEveryTestAccFunctionUsesAnAcceptanceRunner in vcs_gating_test.go), and
// none of these three can be. That is the whole point of this file: it is
// proving the harness helpers themselves (testUniqueName,
// testRegisterCleanup, testAssertContextGone and friends), not a Terraform
// resource -- there is deliberately no circleci_context, circleci_group or
// circleci_project configuration anywhere below for a plan/apply to drive.
// A real acceptance test for each of those resources already exists
// elsewhere and does go through the runner; this file is what that acceptance
// test's own fixtures and cleanup logic get proven against, one level down.

// TestHarnessContextLifecycle creates a context with a unique name in the
// active organization's primary org, using nothing but the harness defined
// alongside this file (testUniqueName, testRegisterCleanup and friends) and
// the raw API client -- no Terraform resource is involved, because a
// context created directly is exactly the kind of
// private, disposable fixture object other acceptance tests need (a context
// to restrict, an environment variable to set on) without reaching for the
// shared CIRCLECI_TEST_<key>_CONTEXT_ID fixture every other test in the
// suite also reads.
//
// This is also the harness's proof for the "single status code lies about
// absence" measurement: DeleteContext's own doc comment (internal/circleci/
// context.go) records that a context which no longer exists answers 403, not
// 404, so a GET-and-check-for-404 verification would either wrongly treat a
// still-live-but-unauthorized context as deleted, or (as here, this token
// being the org admin that created it) never reliably distinguish the two at
// all. Listing the organization's contexts and checking for absence, which
// testAssertContextGone does, is what actually settles it.
func TestHarnessContextLifecycle(t *testing.T) {
	testAccPreCheck(t)

	client := testAPIClient(t)
	orgID := testOrgID(t)

	name := testUniqueName(t, "ctx")

	created, err := client.CreateContext(t.Context(), orgID, name)
	if err != nil {
		t.Fatalf("creating context %q in org %s: %v", name, orgID, err)
	}

	t.Logf("created context %s (%s) in org %s", created.ID, name, orgID)

	// Registered immediately after create succeeds, before any assertion
	// below that could fail the test: exactly the ordering that makes
	// testRegisterCleanup survive a failed assertion later in this
	// function, not just a clean run.
	testRegisterCleanup(t, "context "+created.ID+" ("+name+")", func() error {
		err := client.DeleteContext(context.Background(), created.ID)
		// DeleteContext's own doc comment records that a context which no
		// longer exists answers 403, not 404 -- this test's own explicit
		// delete a few lines below normally beats this Cleanup to it, so
		// treating only IsNotFound as "already gone" made this Cleanup
		// report a false failure on every successful run: caught live,
		// against a real organization, the first time this test ran (see
		// TESTING.md's "Parallel-safe acceptance fixtures" section). This is
		// the same 403-is-fine convention context_resource.go's own Delete
		// already applies.
		if err != nil && !circleci.IsNotFound(err) && !circleci.IsUnauthorized(err) {
			return err
		}

		return nil
	})

	contexts, err := client.ListContexts(t.Context(), orgID)
	if err != nil {
		t.Fatalf("listing contexts in org %s: %v", orgID, err)
	}

	found := false
	for _, c := range contexts {
		if c.ID == created.ID {
			found = true

			if c.Name != name {
				t.Errorf("context %s listed with name %q, want %q", created.ID, c.Name, name)
			}
		}
	}

	if !found {
		t.Errorf("context %s (%s) was created but does not appear in ListContexts for org %s", created.ID, name, orgID)
	}

	if err := client.DeleteContext(t.Context(), created.ID); err != nil {
		t.Fatalf("deleting context %s: %v", created.ID, err)
	}

	// The measurement this whole test exists to act on: confirm deletion via
	// a fresh list, not via GetContext, which answers 403 for a context that
	// is merely inaccessible just as readily as for one that is gone.
	testAssertContextGone(t, client, orgID, created.ID)
}

// TestHarnessProjectLifecycle proves testCreateStandaloneProject end to
// end: create a private, per-test project, confirm it is readable, delete
// it explicitly (rather than relying only on the registered cleanup, so this
// test also exercises what testAssertProjectGone checks), and confirm a
// fresh read answers IsNotFound. testRequireStandaloneOrg gates it exactly
// like TestAccCircleCiProjectResource does, for the same reason: only a
// standalone organization's create route makes a genuine project rather than
// adopting a pre-existing repository.
func TestHarnessProjectLifecycle(t *testing.T) {
	testAccPreCheck(t)

	orgID := testOrgID(t)
	orgSlug := testOrgSlug(t)

	testRequireStandaloneOrg(t, orgSlug)

	client := testAPIClient(t)

	project := testCreateStandaloneProject(t, client, orgID)

	t.Logf("created project %s (slug %s)", project.Name, project.Slug)

	read, err := client.GetProject(t.Context(), project.Slug)
	if err != nil {
		t.Fatalf("reading project %s right after creating it: %v", project.Slug, err)
	}

	if read.ID != project.ID {
		t.Errorf("GetProject(%s).ID = %s, want %s", project.Slug, read.ID, project.ID)
	}

	if err := client.DeleteProject(t.Context(), project.Slug); err != nil {
		t.Fatalf("deleting project %s: %v", project.Slug, err)
	}

	testAssertProjectGone(t, client, project.Slug)
}

// TestHarnessGroupLifecycle is TestHarnessContextLifecycle's
// counterpart for groups, and the harness's live confirmation of the other
// half of the measurement: a deleted group answers 403, not 404, on both a
// repeat DELETE and a GET -- measured directly against this organization
// while building this test ("Permission denied." on both, after a
// genuinely successful first DELETE). That is also why
// internal/circleci/group.go's own Get doc comment ("a missing group is
// reported as an error satisfying IsNotFound") does not describe what this
// route actually does: IsNotFound only recognizes 404 and the ErrNotFound
// sentinel, neither of which this measurement produced. This test does not
// change that file; it exists so testAssertGroupGone's list-based check —
// the thing this harness actually relies on — has real coverage rather
// than only the fake-backed one in group_resource_test.go.
//
// testRequireStandaloneOrg gates it because groups require a standalone
// organization (see the "Accounts required" table in TESTING.md).
func TestHarnessGroupLifecycle(t *testing.T) {
	testAccPreCheck(t)

	orgID := testOrgID(t)
	orgSlug := testOrgSlug(t)

	testRequireStandaloneOrg(t, orgSlug)

	client := testAPIClient(t)
	name := testUniqueName(t, "grp")

	created, err := client.Groups().Create(t.Context(), orgID, circleci.CreateGroupRequest{Name: name})
	if err != nil {
		t.Fatalf("creating group %q in org %s: %v", name, orgID, err)
	}

	t.Logf("created group %s (%s) in org %s", created.ID, name, orgID)

	testRegisterCleanup(t, "group "+created.ID+" ("+name+")", func() error {
		err := client.Groups().Delete(context.Background(), orgID, created.ID)
		// Same convention as the context cleanup above, for the same
		// measured reason: a group that no longer exists answers 403, not
		// 404, on a repeat delete.
		if err != nil && !circleci.IsNotFound(err) && !circleci.IsUnauthorized(err) {
			return err
		}

		return nil
	})

	groups, err := client.Groups().List(t.Context(), orgID)
	if err != nil {
		t.Fatalf("listing groups in org %s: %v", orgID, err)
	}

	found := false
	for _, g := range groups {
		if g.ID == created.ID {
			found = true
		}
	}

	if !found {
		t.Errorf("group %s (%s) was created but does not appear in Groups().List for org %s", created.ID, name, orgID)
	}

	if err := client.Groups().Delete(t.Context(), orgID, created.ID); err != nil {
		t.Fatalf("deleting group %s: %v", created.ID, err)
	}

	testAssertGroupGone(t, client, orgID, created.ID)
}
