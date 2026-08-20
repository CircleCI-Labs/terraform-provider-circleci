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
// indistinguishable from full coverage across all seven integration types,
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
// of the suite cannot: of the seven integration types, which one did *this*
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

	if !slices.Contains(supported, actual) {
		vcsCoverage.mu.Lock()
		vcsCoverage.skipped = append(vcsCoverage.skipped,
			fmt.Sprintf("%s (needs %s, got %s)", t.Name(), strings.Join(supported, "/"), actual))
		vcsCoverage.mu.Unlock()

		t.Skipf("%s only runs against %s; CIRCLECI_TEST_VCS_TYPE=%s does not support this feature "+
			"(see the compatibility matrix in README.md)", t.Name(), strings.Join(supported, "/"), actual)

		return
	}

	vcsCoverage.mu.Lock()
	vcsCoverage.ran = append(vcsCoverage.ran, fmt.Sprintf("%s (%s)", t.Name(), actual))
	vcsCoverage.mu.Unlock()
}

// TestRequireVCSTypeSkipsWhenUnsupported and TestRequireVCSTypeRunsWhenSupported
// mutation-test the guard itself. Every test that calls testRequireVCSType needs
// a live CircleCI account to run at all, which makes the guard's own correctness
// otherwise unobservable in this repository — these two exercise it directly, with
// no API involved, by checking whether code placed after the call in a subtest
// ever runs.
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
