// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// Organization-class gating for circleci_project's acceptance tests.
//
// WHY THIS IS NOT testRequireVCSType. `POST /api/v2/organization/{id}/project`
// is two different operations, and which one runs is decided by the
// organization's CLASS, not by which VCS integration it is connected to.
// Measured over the network:
//
//   - On a STANDALONE (CircleCI-native) organization it genuinely creates a new,
//     repository-less project: 200, with vcs_info.provider "CircleCI" and a
//     vcs_url of "//circleci.com/<orgUUID>/<projectUUID>". This was confirmed on
//     an organization created seconds earlier with no VCS connection of any kind,
//     and separately on a GitHub App organization and a GitLab one — so nothing
//     about it depends on a VCS integration being present.
//   - On a CLASSIC, VCS-backed organization it can only adopt a repository that
//     already exists. A name with no matching repository answers
//     404 {"message":"GitHub response: Not Found"} — confirmed against two
//     separate GitHub-backed organizations.
//
// Four acceptance tests used to be gated `testRequireVCSType(t, "github_oauth",
// "bitbucket")`, or hard-wired to the static GitHub OAuth fixtures, which is
// gating TO the classic integrations where a create of a fresh, randomly named
// project cannot work and AWAY from every standalone one where it can. Run
// against the GitHub OAuth fixtures they failed at step 1 with
// `POST /api/v2/organization/<uuid>/project: 404 Not Found`; run against a
// standalone organization they skipped. That is BUG P4.
//
// The gate reads the organization SLUG, because the slug is what states the class
// and it is already a fixture every one of these tests loads. A standalone
// organization's slug is `circleci/<fragment>`; a classic one's is `gh/<org>` or
// `bb/<org>`.
const (
	orgClassStandalone = "standalone"
	orgClassClassic    = "classic"
)

// standaloneOrgSlugPrefix is the slug prefix CircleCI gives a standalone
// organization. The remaining segment is an opaque base62 identifier of 21 or 22
// characters — measured across fifteen standalone organizations, both lengths
// occur — and it is neither the organization's name nor its UUID. Only the
// prefix is load-bearing here, which is why this matches on that alone.
const standaloneOrgSlugPrefix = "circleci/"

// projectOrgClass reports which class an organization slug names.
//
// Pure, so it can be unit-tested without a live account or any environment
// variable — see TestProjectOrgClass.
func projectOrgClass(orgSlug string) string {
	if strings.HasPrefix(orgSlug, standaloneOrgSlugPrefix) {
		return orgClassStandalone
	}

	return orgClassClassic
}

// testRequireStandaloneOrg skips the calling test unless orgSlug names a
// standalone organization, which is the only class where circleci_project can
// create a project rather than adopt an existing repository.
//
// It records into vcsCoverage (vcs_gating_test.go) exactly as testRequireVCSType
// does, so printVCSCoverageSummary still states what a green run proved. What it
// deliberately does not do is key off CIRCLECI_TEST_VCS_TYPE: every integration
// can be either class — a GitHub organization can be classic or standalone — so a
// VCS-type list would be wrong for whichever half of an integration's
// organizations it did not name.
func testRequireStandaloneOrg(t *testing.T, orgSlug string) {
	t.Helper()

	class := projectOrgClass(orgSlug)
	name := t.Name()

	if class != orgClassStandalone {
		vcsCoverage.mu.Lock()
		vcsCoverage.skipped = append(vcsCoverage.skipped,
			fmt.Sprintf("%s (needs a standalone organization, got the classic org %s)", name, orgSlug))
		vcsCoverage.mu.Unlock()

		t.Skipf("%s creates a project, which only a standalone (\"circleci/…\") organization "+
			"supports; the configured organization %s is classic and VCS-backed, where this route "+
			"can only adopt a repository that already exists (it answers "+
			"404 \"GitHub response: Not Found\" otherwise). See TestAccGithubProjectResource for "+
			"the classic-organization equivalent.", name, orgSlug)

		return
	}

	vcsCoverage.mu.Lock()
	vcsCoverage.ran = append(vcsCoverage.ran,
		fmt.Sprintf("%s (standalone org %s, %s)", name, orgSlug, testVCSType(t)))
	vcsCoverage.mu.Unlock()
}

// testAdoptableRepoName returns the name of a repository that already exists in
// the classic GitHub OAuth test organization and that the suite is allowed to
// create and delete a CircleCI project for.
//
// This fixture is what makes a create test possible at all on a classic
// organization, and it cannot be synthesised: a random name — which is what
// TestAccGithubProjectResource used to pass — has no matching repository, so the
// create is answered 404 and the test fails every time. It is deliberately a
// SEPARATE variable from the org and project fixtures, and deliberately
// documented as "the suite may delete this project", because the test's teardown
// destroys what it created: pointing it at a repository whose CircleCI project
// someone cares about would delete that project and its build history.
//
// Unset means skip, the same as every other fixture in acctest_test.go. A run
// without it proves nothing about the classic path and says so rather than
// passing quietly.
func testAdoptableRepoName(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_GH_OAUTH_ADOPTABLE_REPO_NAME",
		"name of a repository that already exists in the GitHub OAuth test organization and "+
			"whose CircleCI project this suite may create and delete")
}

// TestProjectOrgClass is a permanent unit test of the slug -> class resolution,
// independent of environment variables or skipping. Every slug below is one the
// API actually reported.
func TestProjectOrgClass(t *testing.T) {
	t.Parallel()

	cases := []struct {
		slug string
		want string
	}{
		// A 22-character and a 21-character organization fragment: both lengths
		// occur, so neither may be treated as the shape.
		{"circleci/TFtestOrgFragment01234", orgClassStandalone},
		{"circleci/TFtestOrgFragment0123", orgClassStandalone},
		{"gh/example-org", orgClassClassic},
		{"bb/acme", orgClassClassic},
		{"", orgClassClassic},
	}

	for _, c := range cases {
		if got := projectOrgClass(c.slug); got != c.want {
			t.Errorf("projectOrgClass(%q) = %q, want %q", c.slug, got, c.want)
		}
	}
}

// TestRequireStandaloneOrgGatesOnClass mutation-tests the gate itself, the same
// way TestRequireVCSTypeSkipsWhenUnsupported does for testRequireVCSType: the
// tests it guards all need a live CircleCI account, so its own correctness would
// otherwise be unobservable in this repository.
func TestRequireStandaloneOrgGatesOnClass(t *testing.T) {
	t.Setenv("CIRCLECI_TEST_VCS_TYPE", "github_app")

	t.Run("a classic organization skips", func(t *testing.T) {
		ranPastTheGate := false

		t.Run("subtest", func(t *testing.T) {
			testRequireStandaloneOrg(t, "gh/example-org")
			ranPastTheGate = true
		})

		if ranPastTheGate {
			t.Error("testRequireStandaloneOrg let a test run against the classic organization " +
				"gh/example-org, where creating a project answers 404 unless a repository of " +
				"that name already exists")
		}
	})

	t.Run("a standalone organization runs", func(t *testing.T) {
		ranPastTheGate := false

		t.Run("subtest", func(t *testing.T) {
			testRequireStandaloneOrg(t, "circleci/TFtestOrgFragment01234")
			ranPastTheGate = true
		})

		if !ranPastTheGate {
			t.Error("testRequireStandaloneOrg skipped a test against a standalone organization, " +
				"which is the one class where creating a project works")
		}
	})
}

// This test creates a project under a randomly generated name, so it needs an
// organization where creating a project is possible at all: a STANDALONE one.
// See the org-class gating helpers below for the measurements, and BUG P4 — this test
// used to be gated `testRequireVCSType(t, "github_oauth", "bitbucket")`, which
// pointed it at the two classic integrations where the create is an adoption of
// an existing repository and a random name is therefore answered
// `404 "GitHub response: Not Found"`, and away from every standalone
// organization where it works. Run against the GitHub OAuth fixtures it failed
// at step 1; run against a GitHub App organization it skipped.
//
// The old gate's stated reason was build_fork_prs, on the grounds that only
// GitHub OAuth and Bitbucket Cloud are confirmed to honor `build_fork_prs =
// true`. That is not what the settings API does. Measured over the network on a
// project in a standalone GitHub App organization:
//
//	PATCH /api/v2/project/<slug>/settings
//	  {"advanced":{"build_fork_prs":true,"forks_receive_secret_env_vars":false,"autocancel_builds":true}}
//	→ 200, "build_fork_prs":true, and a fresh GET reports true as well.
//
// So the setting round-trips here, which is all this test asserts. Whether
// CircleCI then actually builds a fork pull request on a given integration is a
// separate question — that is what README.md's compatibility matrix is about —
// and not something a Terraform state check can observe.
func TestAccCircleCiProjectResource(t *testing.T) {
	organizationID := testOrgID(t)
	organizationSlug := testOrgSlug(t)

	testRequireStandaloneOrg(t, organizationSlug)

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

// TestAccGithubProjectResource is the CLASSIC-organization counterpart to
// TestAccCircleCiProjectResource: it exercises the same resource where the create
// route ADOPTS a repository rather than creating a project.
//
// CIRCLECI_TEST_GH_OAUTH_ORG_ID/_SLUG already pin it to a GitHub OAuth
// organization, which is classic, so no VCS gate is needed. What it did need and
// did not have is a repository to adopt. It passed rand.Text() as the project
// name, and on a classic organization a name with no matching repository is
// answered `404 "GitHub response: Not Found"` — measured against two separate
// GitHub-backed organizations — so this test could not pass, ever. That is the
// other half of BUG P4.
//
// It now takes the repository name from a fixture, testAdoptableRepoName, and
// skips when that fixture is unset. Skipping is the honest outcome there: without
// a real repository to adopt there is nothing this test could prove, and inventing
// a name would only reproduce the 404.
func TestAccGithubProjectResource(t *testing.T) {
	orgId := testGithubOrgID(t)
	orgSlug := testGithubOrgSlug(t)
	// A repository that already exists in the classic organization. Not random:
	// see the comment above and testAdoptableRepoName.
	projectName := testAdoptableRepoName(t)
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

// This test creates a randomly named project in the primary organization and
// then again in the alternate one (changing the organization forces
// replacement), so BOTH have to be standalone for the same reason as
// TestAccCircleCiProjectResource above. It carried the same wrong gate and is
// the same half of BUG P4.
func TestAccCircleCiProjectOrgUpdateResource(t *testing.T) {
	organizationID := testOrgID(t)
	organizationSlug := testOrgSlug(t)
	altOrganizationID := testAltOrgID(t)
	altOrganizationSlug := testAltOrgSlug(t)

	testRequireStandaloneOrg(t, organizationSlug)
	testRequireStandaloneOrg(t, altOrganizationSlug)

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

// TestAccGithubProjectOrgUpdateResource exercises Update on a CLASSIC
// organization. It keeps the same organization across both steps and only flips
// build_fork_prs from true to false: the suite has no second GitHub OAuth
// organization fixture to move between, so an org move is not what this test
// exercises.
//
// It takes the repository name from testAdoptableRepoName for the same reason
// TestAccGithubProjectResource above does — a random name cannot be adopted, and
// adoption is the only thing the create route does on a classic organization.
func TestAccGithubProjectOrgUpdateResource(t *testing.T) {
	orgId := testGithubOrgID(t)
	orgSlug := testGithubOrgSlug(t)
	projectName := testAdoptableRepoName(t)
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
