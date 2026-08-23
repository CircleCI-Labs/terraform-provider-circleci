// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"crypto/rand"
	"fmt"
	"os"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

// Shared fixtures for the groups-and-access family's live ([NET]) acceptance
// tests: circleci_group, circleci_groups, circleci_group_membership,
// circleci_project_group and circleci_project_groups. Every *_net_test.go file
// in this family uses these rather than each growing its own copy.

// testUniqueGroupName returns a "<prefix>-<random>" name unique to this test
// run, valid against groupNameAndDescriptionPattern (group_resource.go): the
// random part is rand.Text(), which is [A-Z2-7] and so contains none of the
// characters that pattern rejects.
//
// Unique, not fixed, for the same reason testUniqueRunnerResourceClass in
// acctest_test.go is: a name collision would make a group left behind by a
// killed run block every later run, and there is no update endpoint to fall
// back on -- a duplicate name 409s outright the same way the runner resource
// class route does (this is *not* independently [NET]-confirmed for groups the
// way it is for runner resource classes, but the create route's own generated
// id makes a same-name create idempotent-looking only by accident; relying on
// that would be worse than simply not colliding).
func testUniqueGroupName(t *testing.T, prefix string) string {
	t.Helper()

	return fmt.Sprintf("%s-%s", prefix, rand.Text())
}

// testRequireClassicOrg skips the calling test unless the active integration's
// primary organization is a classic (VCS-backed) one -- the class the groups
// family (circleci_group, circleci_group_membership, circleci_project_group)
// is documented, and [NET] confirmed, to reject.
//
// It is the mirror image of testRequireStandaloneOrg (project_resource_test.go):
// that helper gates TO the class where circleci_project creates a fresh
// project; this one gates TO the class where the groups family's rejection can
// actually be measured, rather than merely assumed. Recording follows the same
// convention (recordVCSCoverageRan/Skip) so printVCSCoverageSummary still
// states what a green run proved.
func testRequireClassicOrg(t *testing.T) {
	t.Helper()

	orgSlug := testOrgSlug(t)
	name := t.Name()

	if projectOrgClass(orgSlug) != orgClassClassic {
		recordVCSCoverageSkip(name,
			fmt.Sprintf("%s (needs a classic organization, got the standalone org %s)", name, orgSlug))

		t.Skipf("%s needs a classic (VCS-backed) organization to exercise the groups family's "+
			"documented rejection there (creating a group 403s on a classic organization); the "+
			"configured organization %s is standalone.", name, orgSlug)

		return
	}

	recordVCSCoverageRan(name, fmt.Sprintf("%s (classic org %s, %s)", name, orgSlug, testVCSType(t)))

}

// testCurrentUserID returns the CircleCI user id the active integration's
// token authenticates as, for use as the one user id every one of these
// acceptance tests can safely add to and remove from a group: TESTING.md
// requires the configured token to belong to an organization admin, so this
// user is guaranteed to be a member of the organization under test, without
// depending on any other account existing.
//
// It must be called after testAccPreCheck (directly, the same way
// TestAccGitHubAppRepositoryDataSourceNet_Found calls it before building its
// own fixture) so CIRCLE_TOKEN is resolved for the active integration before
// this makes its own request.
func testCurrentUserID(t *testing.T) string {
	t.Helper()

	token := activeIntegrationToken(t)
	if token == "" {
		token = os.Getenv("CIRCLE_TOKEN")
	}

	client := circleci.New(circleci.Config{Token: token})

	user, err := client.Users().Current(t.Context())
	if err != nil {
		t.Fatalf("fetching the current user (needed as a group-membership fixture): %v", err)
	}

	return user.ID
}
