// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"crypto/rand"
	"errors"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// build_fork_prs is not uniform across VCS integrations (README.md's
// compatibility matrix): GitHub App, GitHub Enterprise Server and GitLab
// self-managed all report "no", and GitLab is an open contradiction in
// CircleCI's own documentation ([^forkprs]). This test asserts
// build_fork_prs = true on create, which only GitHub OAuth and Bitbucket Cloud
// are confirmed to honor, and organizationID/organizationSlug below are the
// fixtures set in every context — so without this gate the assertion would
// fail against a real GitHub App, GHES or GitLab self-managed organization
// rather than skip.
func TestAccCircleCiProjectResource(t *testing.T) {
	testRequireVCSType(t, "github_oauth", "bitbucket")

	organizationID := testOrgID(t)
	organizationSlug := testOrgSlug(t)
	projectName := rand.Text()
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccProjectResourceConfig(
					projectName,
					organizationID,
					true,
					true,
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("name"),
						knownvalue.StringExact(projectName),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("organization_slug"),
						knownvalue.StringExact(organizationSlug),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("organization_id"),
						knownvalue.StringExact(organizationID),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("auto_cancel_builds"),
						knownvalue.Bool(true),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("build_fork_prs"),
						knownvalue.Bool(true),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("pr_only_branch_overrides"),
						knownvalue.ListSizeExact(1),
					),
				},
			},
			// ImportState testing
			{
				ResourceName:      "circleci_project.test_project",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					slug, found := s.RootModule().Resources["circleci_project.test_project"].Primary.Attributes["slug"]
					if !found {
						return "", errors.New("attribute circleci_project.test_project.slug not found")
					}
					return slug, nil
				},
			},
			// Delete testing automatically occurs in TestCase
		},
	})
}

// CIRCLECI_TEST_GH_OAUTH_ORG_ID/_SLUG are documented (README.md, TESTING.md)
// as static GitHub OAuth fixtures, so testGithubOrgID/testGithubOrgSlug
// already gate this test to that one integration; it needs no separate
// testRequireVCSType call, and build_fork_prs = true is confirmed there (see
// TestAccCircleCiProjectResource above).
func TestAccGithubProjectResource(t *testing.T) {
	projectName := rand.Text()
	orgId := testGithubOrgID(t)
	orgSlug := testGithubOrgSlug(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccProjectResourceConfig(
					projectName,
					orgId,
					false,
					true,
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("name"),
						knownvalue.StringExact(projectName),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("organization_slug"),
						knownvalue.StringExact(orgSlug),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("organization_id"),
						knownvalue.StringExact(orgId),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("build_fork_prs"),
						knownvalue.Bool(true),
					),
				},
			},
			// ImportState testing
			{
				ResourceName:      "circleci_project.test_project",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					slug, found := s.RootModule().Resources["circleci_project.test_project"].Primary.Attributes["slug"]
					if !found {
						return "", errors.New("attribute circleci_project.test_project.slug not found")
					}
					return slug, nil
				},
			},
			// Delete testing automatically occurs in TestCase
		},
	})
}

// This test's Create step asserts build_fork_prs = true against the primary
// test organization, the same fixture and the same assertion as
// TestAccCircleCiProjectResource above, so it needs the same gate for the
// same reason: that value is only confirmed on GitHub OAuth and Bitbucket
// Cloud, and organizationID is set in every context regardless of which VCS
// integration it actually is.
func TestAccCircleCiProjectOrgUpdateResource(t *testing.T) {
	testRequireVCSType(t, "github_oauth", "bitbucket")

	organizationID := testOrgID(t)
	organizationSlug := testOrgSlug(t)
	altOrganizationID := testAltOrgID(t)
	altOrganizationSlug := testAltOrgSlug(t)
	projectName := rand.Text()
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccProjectResourceConfig(
					projectName,
					organizationID,
					true,
					true,
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("name"),
						knownvalue.StringExact(projectName),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("organization_slug"),
						knownvalue.StringExact(organizationSlug),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("organization_id"),
						knownvalue.StringExact(organizationID),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("auto_cancel_builds"),
						knownvalue.Bool(true),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("build_fork_prs"),
						knownvalue.Bool(true),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("pr_only_branch_overrides"),
						knownvalue.ListSizeExact(1),
					),
				},
			},
			{
				Config: testAccProjectResourceConfig(
					projectName,
					altOrganizationID,
					true,
					false,
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("name"),
						knownvalue.StringExact(projectName),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("organization_slug"),
						knownvalue.StringExact(altOrganizationSlug),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("organization_id"),
						knownvalue.StringExact(altOrganizationID),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("auto_cancel_builds"),
						knownvalue.Bool(true),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("build_fork_prs"),
						knownvalue.Bool(false),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("pr_only_branch_overrides"),
						knownvalue.ListSizeExact(1),
					),
				},
			},
			// ImportState testing
			{
				ResourceName:      "circleci_project.test_project",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					slug, found := s.RootModule().Resources["circleci_project.test_project"].Primary.Attributes["slug"]
					if !found {
						return "", errors.New("attribute circleci_project.test_project.slug not found")
					}
					return slug, nil
				},
			},
			// Delete testing automatically occurs in TestCase
		},
	})
}

// CIRCLECI_TEST_GH_OAUTH_ORG_ID/_SLUG are documented as static GitHub OAuth
// fixtures (see TestAccGithubProjectResource above), so this test needs
// no separate testRequireVCSType call either. Unlike the CircleCI-VCS org
// update test above, this one keeps the same organization across both steps
// and only flips build_fork_prs from true to false: the suite has no second
// GitHub OAuth organization fixture to move between, so an org move is not
// what this test exercises.
func TestAccGithubProjectOrgUpdateResource(t *testing.T) {
	projectName := rand.Text()
	orgId := testGithubOrgID(t)
	orgSlug := testGithubOrgSlug(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccProjectResourceConfig(
					projectName,
					orgId,
					false,
					true,
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("name"),
						knownvalue.StringExact(projectName),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("organization_slug"),
						knownvalue.StringExact(orgSlug),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("organization_id"),
						knownvalue.StringExact(orgId),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("build_fork_prs"),
						knownvalue.Bool(true),
					),
				},
			},
			{
				Config: testAccProjectResourceConfig(
					projectName,
					orgId,
					true,
					false,
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("name"),
						knownvalue.StringExact(projectName),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("organization_slug"),
						knownvalue.StringExact(orgSlug),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("organization_id"),
						knownvalue.StringExact(orgId),
					),
					statecheck.ExpectKnownValue(
						"circleci_project.test_project",
						tfjsonpath.New("build_fork_prs"),
						knownvalue.Bool(false),
					),
				},
			},
			// ImportState testing
			{
				ResourceName:      "circleci_project.test_project",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					slug, found := s.RootModule().Resources["circleci_project.test_project"].Primary.Attributes["slug"]
					if !found {
						return "", errors.New("attribute circleci_project.test_project.slug not found")
					}
					return slug, nil
				},
			},
			// Delete testing automatically occurs in TestCase
		},
	})
}

func testAccProjectResourceConfig(name, organization_id string, auto_cancel_builds bool, build_forked_prs bool) string {
	// forks_receive_secret_env_vars is named explicitly because the provider now
	// requires it whenever build_fork_prs is true: an unset value defaults to true
	// on a private project, which would give fork pull requests this project's
	// secrets. false is the choice a test fixture wants.
	return fmt.Sprintf(`
resource "circleci_project" "test_project" {
  name 				 = %[1]q
  organization_id 	 = %[2]q
  auto_cancel_builds = %[3]t
  build_fork_prs     = %[4]t

  forks_receive_secret_env_vars = false
}
`, name, organization_id, auto_cancel_builds, build_forked_prs)
}
