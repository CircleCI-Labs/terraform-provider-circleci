// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Per-VCS gating and coverage reporting for the acceptance suite.
//
// A green run of this package against one GitHub App organization used to be
// indistinguishable from full coverage across all eight integration types,
// because no test keyed its skips off which one it was actually pointed at.
// Running the same suite against GitLab or Bitbucket would fail on features
// those integrations genuinely do not have (see README.md's compatibility
// matrix) — a real-API failure that reads exactly like a regression.
//
// testRequireVCSType closes that gap for the handful of tests it actually
// affects: it skips, naming both the requirement and what was configured,
// when CIRCLECI_TEST_VCS_TYPE names an integration the test does not support.
// TestMain then reports, once the suite finishes, which integration this run
// exercised and which VCS-gated tests it could not — so a green run states
// what it proved instead of only that it passed.

// vcsCoverage records, for the lifetime of one `go test` invocation, every
// call to testRequireVCSType: whether the configured integration was
// supported, and if not, what was needed instead. printVCSCoverageSummary
// reports it once, after every test has run.
//
// This is deliberately not a general pass/fail reporter — `go test`'s own
// output already is one. It exists only to answer the one question the rest
// of the suite cannot: of the eight integration types, which one did *this*
// run actually touch.
var vcsCoverage = struct {
	mu      sync.Mutex
	ran     []string
	skipped []string
}{}

// testRequireVCSType skips the calling test, naming both the requirement and
// the actual value, unless CIRCLECI_TEST_VCS_TYPE is one of the given types.
//
// Call it once, before building any Terraform config, from every acceptance
// test whose resource is not available — or not available with the same
// contract — on every VCS integration. Tests that only reach fixtures already
// scoped to one integration (for example testGithubAppRepoExternalID) do not
// need this: CIRCLECI_TEST_GH_APP_REPO_EXTERNAL_ID is documented as set only
// for the GitHub App integration, so those already skip cleanly elsewhere.
func testRequireVCSType(t *testing.T, supported ...string) {
	t.Helper()

	actual := testVCSType(t)
	name := t.Name()

	if !slices.Contains(supported, actual) {
		recordVCSCoverageSkip(name, fmt.Sprintf("%s (needs %s, got %s)", name, strings.Join(supported, "/"), actual))

		t.Skipf("%s only runs against %s; CIRCLECI_TEST_VCS_TYPE=%s does not support this feature "+
			"(see the compatibility matrix in README.md)", name, strings.Join(supported, "/"), actual)

		return
	}

	recordVCSCoverageRan(name, fmt.Sprintf("%s (%s)", name, actual))
}

// isRealAcceptanceTestName reports whether name — a *testing.T.Name(), which
// for a subtest is "Parent/Child/…" — belongs to a genuine acceptance test
// rather than to one of this package's own tests of the gating helpers.
//
// Every real acceptance test in this package is named TestAcc* (see
// TESTING.md and the "=== Acceptance tests (TestAcc*)" section of the CI
// summary); every test that drives testRequireVCSType or
// testRequireStandaloneOrg directly — the mutation tests below and in
// project_resource_test.go — is not. Only the root test name decides this: a
// subtest's own name (after t.Run's space-to-underscore mangling) could
// coincidentally start with "TestAcc" and must not count.
//
// recordVCSCoverageRan/Skip call this so that recording into vcsCoverage is
// opt-IN by construction: a new gating helper's self-test is excluded the
// moment it exists, with nothing to call and nothing to remember. That
// replaces an earlier opt-OUT design (a helper named testIsolateVCSCoverage,
// which every self-test had to remember to call) that one self-test —
// TestRequireStandaloneOrgGatesOnClass, in project_resource_test.go — forgot,
// and which then reported its own synthetic run as VCS coverage a real
// acceptance test never provided. See TestGatingSelfTestsNeverRecordCoverage
// below for the regression this replaces.
func isRealAcceptanceTestName(name string) bool {
	root := name
	if i := strings.IndexByte(root, '/'); i >= 0 {
		root = root[:i]
	}

	return strings.HasPrefix(root, "TestAcc")
}

// recordVCSCoverageRan and recordVCSCoverageSkip are the only writers of
// vcsCoverage. Both gate on isRealAcceptanceTestName so that a gating
// helper's own self-test — called from a test not named TestAcc* — can never
// land in either slice.
func recordVCSCoverageRan(testName, entry string) {
	if !isRealAcceptanceTestName(testName) {
		return
	}

	vcsCoverage.mu.Lock()
	defer vcsCoverage.mu.Unlock()

	vcsCoverage.ran = append(vcsCoverage.ran, entry)
}

func recordVCSCoverageSkip(testName, entry string) {
	if !isRealAcceptanceTestName(testName) {
		return
	}

	vcsCoverage.mu.Lock()
	defer vcsCoverage.mu.Unlock()

	vcsCoverage.skipped = append(vcsCoverage.skipped, entry)
}

// TestIsRealAcceptanceTestName is a permanent unit test of the naming rule
// recordVCSCoverageRan/Skip gate on, independent of the testing package's own
// skip/parallel machinery. The two mutation-test names are the actual names
// this guarded against polluting the summary.
func TestIsRealAcceptanceTestName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		want bool
	}{
		{"TestAccGithubProjectResource", true},
		{"TestAccGithubProjectResource/subtest", true},
		{"TestRequireVCSTypeRunsWhenSupported/subtest", false},
		{"TestRequireStandaloneOrgGatesOnClass/a_standalone_organization_runs/subtest", false},
		{"TestGatingSelfTestsNeverRecordCoverage/subtest", false},
	}

	for _, c := range cases {
		if got := isRealAcceptanceTestName(c.name); got != c.want {
			t.Errorf("isRealAcceptanceTestName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestGatingSelfTestsNeverRecordCoverage pins the opt-in design that replaced
// the old opt-out (a testIsolateVCSCoverage helper self-tests had to
// remember to call): driving testRequireVCSType or testRequireStandaloneOrg
// directly, the way their own mutation tests do, must leave vcsCoverage
// exactly as it found it, with nothing extra to call.
//
// This is the test that would have caught the actual incident: it drives
// testRequireStandaloneOrg exactly the way TestRequireStandaloneOrgGatesOnClass
// (project_resource_test.go) does — directly, with a synthetic standalone-org
// slug, from a subtest of a test not named TestAcc* — and checks nothing
// lands in vcsCoverage. Before this fix, that exact call was the one entry a
// green acceptance-gh-hybrid job reported as its only "exercised" real-API
// test.
func TestGatingSelfTestsNeverRecordCoverage(t *testing.T) {
	recorded := func() int {
		vcsCoverage.mu.Lock()
		defer vcsCoverage.mu.Unlock()

		return len(vcsCoverage.ran) + len(vcsCoverage.skipped)
	}

	before := recorded()

	t.Setenv("CIRCLECI_TEST_VCS_TYPE", "github_app")

	t.Run("testRequireVCSType, supported", func(t *testing.T) {
		testRequireVCSType(t, "github_app")
	})

	t.Run("testRequireVCSType, unsupported", func(t *testing.T) {
		testRequireVCSType(t, "gitlab")
	})

	t.Run("testRequireStandaloneOrg, standalone", func(t *testing.T) {
		testRequireStandaloneOrg(t, "circleci/TFtestOrgFragment01234")
	})

	t.Run("testRequireStandaloneOrg, classic", func(t *testing.T) {
		testRequireStandaloneOrg(t, "gh/example-org")
	})

	if after := recorded(); after != before {
		t.Errorf("driving the gating helpers directly, as a self-test does, left %d entries in "+
			"vcsCoverage, want %d; they would be reported as VCS integration coverage that no "+
			"acceptance test provided", after, before)
	}
}

// TestRequireVCSTypeSkipsWhenUnsupported and TestRequireVCSTypeRunsWhenSupported
// mutation-test the guard itself. Every test that calls testRequireVCSType needs
// a live CircleCI account to run at all, which makes the guard's own correctness
// otherwise unobservable in this repository — these two exercise it directly, with
// no API involved, by checking whether code placed after the call in a subtest
// ever runs.
//
// Neither needs to isolate itself from the coverage record: neither is named
// TestAcc*, so recordVCSCoverageRan/Skip already exclude them — see
// isRealAcceptanceTestName.
func TestRequireVCSTypeSkipsWhenUnsupported(t *testing.T) {
	t.Setenv("CIRCLECI_TEST_VCS_TYPE", "gitlab")

	ranPastTheGate := false

	t.Run("subtest", func(t *testing.T) {
		testRequireVCSType(t, "github_app", "github_oauth")
		ranPastTheGate = true
	})

	if ranPastTheGate {
		t.Error("testRequireVCSType let a test whose CIRCLECI_TEST_VCS_TYPE (gitlab) is not in its " +
			"supported list run past the gate")
	}
}

func TestRequireVCSTypeRunsWhenSupported(t *testing.T) {
	t.Setenv("CIRCLECI_TEST_VCS_TYPE", "github_app")

	ranPastTheGate := false

	t.Run("subtest", func(t *testing.T) {
		testRequireVCSType(t, "github_app", "github_oauth")
		ranPastTheGate = true
	})

	if !ranPastTheGate {
		t.Error("testRequireVCSType skipped a test whose CIRCLECI_TEST_VCS_TYPE (github_app) is in its " +
			"supported list")
	}
}

// TestMain lets the suite report what it covered after every test has run.
// It changes nothing about which tests execute or how they are gated — that
// is testRequireVCSType's job — it only makes a green run's result legible.
func TestMain(m *testing.M) {
	code := m.Run()

	printVCSCoverageSummary()

	os.Exit(code)
}

// printVCSCoverageSummary is the whole of "reporting" here: one block of
// stdout, no separate command, no framework. TESTING.md's "What a run covers"
// section is the human-readable statement of the same claim.
func printVCSCoverageSummary() {
	vcsCoverage.mu.Lock()
	ran := append([]string(nil), vcsCoverage.ran...)
	skipped := append([]string(nil), vcsCoverage.skipped...)
	vcsCoverage.mu.Unlock()

	if len(ran) == 0 && len(skipped) == 0 {
		// No VCS-gated test even asked: either this was a credential-less
		// checkout, or CIRCLECI_TEST_VCS_TYPE was never read this invocation.
		// Nothing to report either way, and staying silent here matters —
		// this is what keeps `task test:fast` and `task test` quiet on a
		// fresh checkout.
		//
		// The acceptance jobs in .circleci/config.yml rely on that silence
		// having exactly one meaning: they fail when the block is absent,
		// because for a job that configures an integration its absence means
		// the gate was never reached. That only holds because the guard's own
		// mutation tests can never land in the record — see
		// isRealAcceptanceTestName.
		return
	}

	sort.Strings(ran)
	sort.Strings(skipped)

	fmt.Println()
	fmt.Println("=== VCS integration coverage (CIRCLECI_TEST_VCS_TYPE=" + os.Getenv("CIRCLECI_TEST_VCS_TYPE") + ") ===")
	fmt.Printf("Exercised by this run (%d):\n", len(ran))
	for _, name := range ran {
		fmt.Println("  " + name)
	}
	fmt.Printf("Skipped, configured fixture is a different integration (%d):\n", len(skipped))
	for _, name := range skipped {
		fmt.Println("  " + name)
	}
}
