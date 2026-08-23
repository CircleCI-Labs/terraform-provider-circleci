// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-circleci/internal/circleci"
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
// It records into vcsCoverage (vcs_gating_test.go) through the same
// recordVCSCoverageRan/Skip helpers testRequireVCSType uses, so
// printVCSCoverageSummary still states what a green run proved — and, like
// testRequireVCSType, only for a caller whose root test name is TestAcc*
// (see isRealAcceptanceTestName): this gate's own mutation test,
// TestRequireStandaloneOrgGatesOnClass below, drives it directly and must
// never be counted as coverage. What testRequireStandaloneOrg deliberately
// does not do is key off CIRCLECI_TEST_VCS_TYPE: every integration can be
// either class — a GitHub organization can be classic or standalone — so a
// VCS-type list would be wrong for whichever half of an integration's
// organizations it did not name.
func testRequireStandaloneOrg(t *testing.T, orgSlug string) {
	t.Helper()

	class := projectOrgClass(orgSlug)
	name := t.Name()

	if class != orgClassStandalone {
		recordVCSCoverageSkip(name,
			fmt.Sprintf("%s (needs a standalone organization, got the classic org %s)", name, orgSlug))

		t.Skipf("%s creates a project, which only a standalone (\"circleci/…\") organization "+
			"supports; the configured organization %s is classic and VCS-backed, where this route "+
			"can only adopt a repository that already exists (it answers "+
			"404 \"GitHub response: Not Found\" otherwise). See TestAccGithubProjectResource for "+
			"the classic-organization equivalent.", name, orgSlug)

		return
	}

	recordVCSCoverageRan(name, fmt.Sprintf("%s (standalone org %s, %s)", name, orgSlug, testVCSType(t)))
}

// testRequireGitHubCLI skips the calling test unless a `gh` binary is on
// PATH and authenticated. Every test in this file that adopts a repository on
// a classic organization needs one, via testAdoptableGithubRepo below.
//
// Skip, not Fatal: exactly the same convention every other unmet fixture in
// this suite follows (testAccEnv), because a missing `gh` binary or an
// unauthenticated one says nothing about a regression in this provider — it
// says this environment cannot run this class of test. Measured while
// building this file: none of the four jobs in .circleci/config.yml exports a
// GitHub credential today (only CIRCLE_TOKEN, via the
// terraform-provider-acc-token context), so this skips there and runs
// wherever `gh auth login` (or a GH_TOKEN/GITHUB_TOKEN in the environment,
// which `gh` reads on its own) has already happened — a developer's machine,
// or an agent session with `gh` configured, such as the one that wrote this.
func testRequireGitHubCLI(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("gh"); err != nil {
		t.Skip("gh CLI not found on PATH; this test provisions its own private GitHub repository to " +
			"exercise circleci_project's classic-organization adopt path, and needs `gh` authenticated " +
			"with repository create/delete access to the GitHub OAuth test organization")
	}

	if out, err := exec.Command("gh", "auth", "status").CombinedOutput(); err != nil {
		t.Skipf("gh CLI is present but not authenticated (%v): %s", err, strings.TrimSpace(string(out)))
	}
}

// testAdoptableGithubRepo creates a private GitHub repository with a name
// unique to this test run (testUniqueName) under owner (a GitHub login, e.g.
// "gh-oauth-cci-1"), registers its deletion via testRegisterCleanup, and
// returns its bare name — which is also what circleci_project's "name"
// attribute must be set to on a classic organization, since the adopt route
// resolves purely against a GitHub repository of that name.
//
// It seeds the repository with a README (`--add-readme`) rather than leaving
// it truly empty. That is not optional: a v2 create against a repository with
// no commits at all still answers 200, but CreateProject's v1.1 follow call
// right after it — the step that makes the project actually runnable, and
// with no way to opt out of on a classic organization — answers
// 400 {"message":"Branch not found"}, because there is no default branch for
// it to follow. This is a correction of an earlier version of this comment
// (and of this function), which claimed adopting a repository needs no commit
// at all: that was measured only against the bare v2 create call in
// isolation, never against CreateProject's real, complete path including
// follow — and it was wrong. Measured now with a README present: the same
// v1.1 follow call answers 200.
//
// No .circleci/config.yml is added, and that part of the original measurement
// holds: adopting and following both succeed without one. A project with no
// config simply cannot run a pipeline, which none of these tests need it to.
//
// WHY THIS EXISTS RATHER THAN A HAND-MAINTAINED FIXTURE. Every classic-org
// test in this file used to read a single repository name from
// CIRCLECI_TEST_GH_OAUTH_ADOPTABLE_REPO_NAME (gh-oauth-cci-1/tf-acc-adoptable),
// reused across every run, on the understanding that each run adopts it,
// exercises it, and unadopts it again before the next run needs it unadopted.
//
// That invariant does not hold, and this is not theoretical: it broke live
// while this file was being written. The fixture repository was found already
// ADOPTED (project id 045eccd7-…, with real build history) with no test of
// this suite having run to do it — almost certainly a previous run that died
// between adopting it and deleting it again, the exact gap
// testRegisterCleanup's own doc comment (acctest_harness_test.go) names as
// unrecoverable from inside the process that died. It was unadopted by hand
// (`DELETE /api/v2/project/gh/gh-oauth-cci-1/tf-acc-adoptable` → 200, then a
// GET → 404) to get a clean baseline — and within about a minute of that,
// while this file's own tests were being run against it, a GET on the exact
// same slug answered 200 again with a DIFFERENT project id (6f928ba4-…).
// Something else running concurrently against the same organization had
// adopted it again, and `go test -run TestAccGithubProjectResource` failed
// immediately with precisely the 409 TestAccGithubProjectResourceAlreadyAdopted
// below now exists to trigger on purpose.
//
// A single, fixed-name repository cannot be made safe against that: there is
// exactly one of it, so any two processes that both need it adopted, or one
// that needs it adopted while another needs it not, collide — the same
// "shared mutable fixture" problem acctest_harness_test.go's testUniqueName
// exists to close for every other object type this suite creates directly.
// A repository name is no different, and provisioning one turned out cheap
// enough that the collision risk is not worth keeping: also measured live
// while building this file, `gh repo create OWNER/NAME --private --add-readme`
// followed immediately by adopting and following it answered 200 on both
// calls on the very next try, in well under the time a `go test` timeout
// would notice.
//
// This does mean these tests need `gh` rather than a pre-made repository —
// see testRequireGitHubCLI — which is a net loosening, not a tightening: no
// job in .circleci/config.yml sets CIRCLECI_TEST_GH_OAUTH_ADOPTABLE_REPO_NAME
// today, so removing it costs that CI config nothing, while a developer or
// agent with `gh` already authenticated no longer has to hand-create and
// babysit a fixture repository at all.
func testAdoptableGithubRepo(t *testing.T, owner string) string {
	t.Helper()

	testRequireGitHubCLI(t)

	name := testUniqueName(t, "repo")
	full := owner + "/" + name

	out, err := exec.Command("gh", "repo", "create", full, "--private", "--add-readme",
		"-d", "Ephemeral fixture for terraform-provider-circleci's circleci_project acceptance "+
			"tests (see testAdoptableGithubRepo). Safe to delete.",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("creating GitHub repository %s via gh CLI: %v: %s", full, err, out)
	}

	testRegisterCleanup(t, "GitHub repository "+full, func() error {
		out, err := exec.Command("gh", "repo", "delete", full, "--yes").CombinedOutput()
		if err != nil {
			return fmt.Errorf("gh repo delete %s: %w: %s", full, err, out)
		}

		return nil
	})

	return name
}

// githubOwnerFromSlug extracts the GitHub login from a classic organization
// slug ("gh/gh-oauth-cci-1" -> "gh-oauth-cci-1"), which is what `gh repo
// create`/`gh repo delete` need as the OWNER half of OWNER/NAME.
func githubOwnerFromSlug(orgSlug string) string {
	return strings.TrimPrefix(orgSlug, "gh/")
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
//
// This test's own name is not TestAcc*, so its two direct calls to
// testRequireStandaloneOrg below can never be recorded as VCS coverage — see
// isRealAcceptanceTestName in vcs_gating_test.go. That is what makes it safe
// for this test not to isolate itself: an earlier version of this test did
// pollute vcsCoverage this way, under the opt-out design that mechanism
// replaced (see TestGatingSelfTestsNeverRecordCoverage in vcs_gating_test.go
// for the regression test).
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
			// ImportStateVerify (above) only compares the imported state's
			// attributes against the state Create produced -- it never runs a
			// plan, so it cannot by itself catch an import that leaves the
			// configuration wanting a change. This step does: same Config as
			// step 1, applied against the just-imported state, with PlanOnly so
			// the framework's own "no changes" check (every non-import,
			// non-ExpectNonEmptyPlan step gets one) is what proves the second
			// plan is empty.
			{
				Config: testAccProjectResourceConfig(
					projectName,
					organizationID,
					true,
					true,
				),
				PlanOnly: true,
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
// It now provisions its own repository to adopt, testAdoptableGithubRepo,
// and skips when `gh` is unavailable or unauthenticated. Skipping is the
// honest outcome there: without a real repository to adopt there is nothing
// this test could prove, and inventing a name would only reproduce the 404 —
// which is exactly what TestAccGithubProjectResourceRepoNotFound below tests
// on purpose, instead.
func TestAccGithubProjectResource(t *testing.T) {
	orgId := testGithubOrgID(t)
	orgSlug := testGithubOrgSlug(t)
	// A repository this test creates and adopts itself. Not a fixture, and
	// not random: see the comment above and testAdoptableGithubRepo.
	projectName := testAdoptableGithubRepo(t, githubOwnerFromSlug(orgSlug))
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
			// Same reasoning as TestAccCircleCiProjectResource's own PlanOnly
			// step: ImportStateVerify never runs a plan, so this is what proves
			// import leaves an empty plan on a CLASSIC organization too, not
			// only a standalone one.
			{
				Config: testAccProjectResourceConfig(
					projectName,
					orgId,
					false,
					true,
				),
				PlanOnly: true,
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
// It provisions its own repository via testAdoptableGithubRepo for the same
// reason TestAccGithubProjectResource above does — a random name cannot be
// adopted, and adoption is the only thing the create route does on a classic
// organization.
func TestAccGithubProjectOrgUpdateResource(t *testing.T) {
	orgId := testGithubOrgID(t)
	orgSlug := testGithubOrgSlug(t)
	projectName := testAdoptableGithubRepo(t, githubOwnerFromSlug(orgSlug))
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

// TestAccGithubProjectResourceRepoNotFound is the classic-organization
// failure path a practitioner actually hits when the repository they named
// does not exist: adopting it answers
// 404 {"message":"GitHub response: Not Found"} (see
// projectCreateFailureDetail's own doc comment for the network measurement),
// and the provider is expected to turn that into a diagnostic that names the
// precondition rather than relaying the API's message alone.
//
// No fixture and no self-provisioned repository: testUniqueName's random
// suffix makes a collision with a real repository in the organization
// practically impossible, and creating one here would defeat the point —
// this test exists to prove what happens when the repository genuinely is
// not there.
func TestAccGithubProjectResourceRepoNotFound(t *testing.T) {
	orgId := testGithubOrgID(t)

	projectName := testUniqueName(t, "gone")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectResourceConfig(projectName, orgId, false, true),
				// (?s) so the message's own newlines cannot break the match, and
				// bare single tokens on both sides of the .* rather than
				// multi-word (or even multi-character-with-an-internal-space)
				// phrases: terraform's own CLI renderer wraps long diagnostic
				// text at a column width it chooses, which can and did insert a
				// newline in place of an ordinary space mid-phrase. Caught live
				// twice while writing this file: first as "GitHub response:
				// Not\nFound" (splitting a two-word phrase), and again, after
				// switching to what looked like a single wrap-proof token, as
				// "(HTTP\n409)" in TestAccGithubProjectResourceAlreadyAdopted
				// below -- "(HTTP 409)" is not actually one token, since the
				// space between "HTTP" and "409" is exactly as wrappable as any
				// other. A regex space does not match a literal newline, so
				// both matches failed even though the diagnostic was exactly
				// right — a false negative in the test, not a bug in
				// projectCreateFailureDetail (which TestProjectCreateFailureDetail
				// asserts in full, away from any wrapping, as a pure function).
				// "404" and "ADOPT" are each a single run of characters with no
				// space for wrapping to land on.
				ExpectError: regexp.MustCompile(`(?s)\b404\b.*\bADOPT\b`),
			},
		},
	})
}

// TestAccGithubProjectResourceAlreadyAdopted is the classic-organization
// failure path for the OTHER thing a practitioner hits: naming a repository
// that is already a CircleCI project. Measured over the network (see
// projectCreateFailureDetail): 409, with a message that says a project
// already exists but nothing about what to do next. This test proves the
// provider adds the "what to do next" — point at `terraform import` rather
// than leaving the practitioner to guess or, worse, delete the existing
// project (and its build history) to make room for a new one.
//
// It provisions its own repository (testAdoptableGithubRepo) rather than
// reusing the one TestAccGithubProjectResource adopts: reusing a shared,
// already-adopted repository for this would race against every other test in
// this file that needs its own repository unadopted, which is exactly the
// hazard that made this suite move away from a single shared fixture in the
// first place (see testAdoptableGithubRepo's own doc comment). The repository
// is adopted once here with the raw API client — deliberately not through
// Terraform, so the conflict this test is about happens on the FIRST
// Terraform apply, not on a second one racing the first's own state.
func TestAccGithubProjectResourceAlreadyAdopted(t *testing.T) {
	testAccPreCheck(t)

	orgId := testGithubOrgID(t)
	orgSlug := testGithubOrgSlug(t)

	projectName := testAdoptableGithubRepo(t, githubOwnerFromSlug(orgSlug))

	client := testAPIClient(t)

	adopted, err := client.CreateProject(t.Context(), orgId, projectName)
	if err != nil {
		t.Fatalf("adopting %s with the raw API client, to set up the conflict this test is about: %v",
			projectName, err)
	}

	testRegisterCleanup(t, "project "+adopted.Slug, func() error {
		if err := client.DeleteProject(context.Background(), adopted.Slug); err != nil && !circleci.IsNotFound(err) {
			return fmt.Errorf("deleting project %s: %w", adopted.Slug, err)
		}

		return nil
	})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectResourceConfig(projectName, orgId, false, true),
				// Bare "409" and "import", not "(HTTP 409)" or "terraform
				// import" — see TestAccGithubProjectResourceRepoNotFound's own
				// comment on this regex for why: this exact test is where
				// "(HTTP 409)" was caught live coming back as "(HTTP\n409)",
				// wrapped mid-phrase, on the first run with this fix in place.
				ExpectError: regexp.MustCompile(`(?s)\b409\b.*\bimport\b`),
			},
		},
	})
}

// TestAccGithubProjectResourceDestroyUnfollows proves that destroying a
// circleci_project on a CLASSIC organization really unfollows the
// repository, by a read that is independent of the route Delete itself
// calls: re-adopting the SAME repository name with the raw API client after
// Terraform's own destroy has run. A repository that is still followed
// answers exactly the 409 TestAccGithubProjectResourceAlreadyAdopted above
// exists to test; only a genuinely unfollowed one can be adopted again. A
// second GET on the project's old slug would only show that DeleteProject's
// own belief about itself is consistent, not that CircleCI agrees — this
// asks CircleCI a question whose answer depends on the real state, through a
// second, independent call to the very route this whole family of tests is
// about.
func TestAccGithubProjectResourceDestroyUnfollows(t *testing.T) {
	testAccPreCheck(t)

	orgId := testGithubOrgID(t)
	orgSlug := testGithubOrgSlug(t)

	projectName := testAdoptableGithubRepo(t, githubOwnerFromSlug(orgSlug))

	client := testAPIClient(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProjectResourceConfig(projectName, orgId, false, true),
				Check:  resource.TestCheckResourceAttr("circleci_project.test_project", "name", projectName),
			},
			// Delete testing automatically occurs when resource.Test's own
			// TestCase teardown runs, after this function returns from
			// resource.Test below. The independent proof happens after that.
		},
	})

	// INDEPENDENT PROOF: adopt the same repository again with the raw API
	// client. A project that destroy left genuinely unfollowed can be
	// adopted again (200); one that is still followed answers 409. This is
	// the measurement projectCreateFailureDetail's own doc comment records,
	// used here as an assertion rather than a diagnostic to explain.
	reAdopted, err := client.CreateProject(t.Context(), orgId, projectName)
	if err != nil {
		t.Fatalf("destroy did not really unfollow %s: adopting it again failed: %v -- a still-followed "+
			"project answers 409 (\"already exists\") here, which is exactly what this test exists to "+
			"rule out", projectName, err)
	}

	// Clean up this assertion's OWN re-adopt -- registered only now, because
	// only now does it exist. testRegisterCleanup's t.Cleanup runs LIFO, so
	// this runs before testAdoptableGithubRepo's GitHub-repository deletion
	// above: the CircleCI project is unfollowed first, then the repository
	// itself is deleted.
	testRegisterCleanup(t, "project "+reAdopted.Slug+" (this test's own re-adopt check)", func() error {
		if err := client.DeleteProject(context.Background(), reAdopted.Slug); err != nil && !circleci.IsNotFound(err) {
			return fmt.Errorf("deleting project %s: %w", reAdopted.Slug, err)
		}

		return nil
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
