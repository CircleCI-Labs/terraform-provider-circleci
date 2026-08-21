// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// slugVCSTypePrefixes maps an organization slug's leading segment to the
// value the API's own `vcs_type` field reports for it.
//
// [NET] "circleci" and "gh" are confirmed against real fixture organizations:
// a standalone org's slug is "circleci/<opaque id>" and its vcs_type is
// "circleci"; a classic GitHub-backed org's slug is "gh/<name>" and its
// vcs_type is "github". "bb" and "gitlab" mirror the documented VCS
// abbreviation table (see organization.go's OrganizationVCSTypes and
// TESTING.md's slug-shape table) but are not independently network-confirmed
// here, since no classic Bitbucket or GitLab fixture was available to probe.
var slugVCSTypePrefixes = map[string]string{
	"circleci": "circleci",
	"gh":       "github",
	"bb":       "bitbucket",
	"gitlab":   "gitlab",
}

// expectedVCSTypeForSlug derives the vcs_type this test should see, from the
// slug of the organization actually under test.
//
// This test used to hardcode `vcs_type = "circleci"` unconditionally — true
// only by accident, because every run before this one happened to point at a
// standalone fixture. Running it with CIRCLECI_TEST_VCS_TYPE=github_oauth
// (a classic, GitHub-backed organization) failed with
// "expected value circleci ... got: github" — the test encoded a false
// belief ("the organization under test is always standalone"), not a
// provider bug. See testOrgSlug/testOrgName/testOrgID's own documentation:
// they are meant to resolve to whichever integration is active, dynamically.
func expectedVCSTypeForSlug(t *testing.T, slug string) string {
	t.Helper()

	prefix, _, ok := strings.Cut(slug, "/")
	if !ok {
		t.Fatalf("organization slug %q has no \"/\" separator to read a VCS prefix from", slug)
	}

	vcsType, ok := slugVCSTypePrefixes[prefix]
	if !ok {
		t.Fatalf("organization slug %q has prefix %q, which is not in slugVCSTypePrefixes; "+
			"add it there rather than guessing in the test body", slug, prefix)
	}

	return vcsType
}

func TestAccOrganizationDataSource(t *testing.T) {
	organizationID := testOrgID(t)
	organizationName := testOrgName(t)
	organizationSlug := testOrgSlug(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Read testing
			{
				Config: testOrganizationDataSourceConfig(organizationID),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_organization.test_organization",
						tfjsonpath.New("id"),
						knownvalue.StringExact(organizationID),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_organization.test_organization",
						tfjsonpath.New("name"),
						knownvalue.StringExact(organizationName),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_organization.test_organization",
						tfjsonpath.New("slug"),
						knownvalue.StringExact(organizationSlug),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_organization.test_organization",
						tfjsonpath.New("vcs_type"),
						knownvalue.StringExact(expectedVCSTypeForSlug(t, organizationSlug)),
					),
				},
			},
		},
	})
}

func testOrganizationDataSourceConfig(organizationID string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host = "https://circleci.com/api/v2"
}

data "circleci_organization" "test_organization" {
  id = %[1]q
}
`, organizationID)
}

// TestAccOrganizationDataSourceBySlugAgainstRealAPI is the real-network
// sibling of the fake-backed TestAccOrganizationDataSourceBySlug in
// lookup_by_name_test.go.
//
// It matters specifically for a standalone organization: its slug is
// "circleci/<21-or-22-char base62 identifier>", a fragment with no
// relationship at all to the organization's `name`. A provider that quietly
// assumed the slug's second segment was derived from the name — or that only
// ever exercised the classic "gh/<name>" shape, where the two happen to
// match — would not be caught by a fake using that shape. Running this
// against every configured integration proves the real API's
// "org-slug-or-id" segment round-trips both shapes: `circleci/<opaque id>`
// for a standalone organization and `gh/<name>` for a classic one.
func TestAccOrganizationDataSourceBySlugAgainstRealAPI(t *testing.T) {
	organizationID := testOrgID(t)
	organizationName := testOrgName(t)
	organizationSlug := testOrgSlug(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testOrganizationDataSourceSlugConfig(organizationSlug),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.circleci_organization.test_organization",
						tfjsonpath.New("id"),
						knownvalue.StringExact(organizationID),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_organization.test_organization",
						tfjsonpath.New("name"),
						knownvalue.StringExact(organizationName),
					),
					statecheck.ExpectKnownValue(
						"data.circleci_organization.test_organization",
						tfjsonpath.New("slug"),
						knownvalue.StringExact(organizationSlug),
					),
				},
			},
		},
	})
}

func testOrganizationDataSourceSlugConfig(slug string) string {
	return fmt.Sprintf(`
data "circleci_organization" "test_organization" {
  slug = %[1]q
}
`, slug)
}
