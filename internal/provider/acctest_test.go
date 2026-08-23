// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

// Acceptance tests in this package talk to a real CircleCI installation, so
// every organization, project, pipeline, trigger, context and webhook they
// reference has to exist there. Those identifiers used to be hardcoded, which
// tied the suite to a single (now deleted) account. They are now read from
// environment variables instead: each helper below skips the calling test when
// its variable is unset (or a placeholder — see testAccEnv), so a developer
// without a provisioned test account gets skips rather than failures.
// README.md documents the full list.
//
// Every per-integration variable is named CIRCLECI_TEST_<KEY>_<SUFFIX>, where
// KEY comes from integrationKeys below and SUFFIX is identical for every key
// (ORG_ID, PROJECT_ID, and so on — see README.md for the full suffix list).
// There are two ways a helper can turn that template into an actual name:
//
//   - DYNAMICALLY, from whichever integration CIRCLECI_TEST_VCS_TYPE names as
//     active: testOrgID, testProjectID and the rest of the "test the
//     organization under test" helpers do this via testActiveEnv, which is why
//     they carry no VCS branching of their own — the same call reads a GitHub
//     App fixture in one run and a Bitbucket fixture in another.
//   - STATICALLY, from one hardcoded key: testGithubOrgID always reads
//     CIRCLECI_TEST_GH_OAUTH_ORG_ID no matter what is active, because it is a
//     test that specifically needs a GitHub OAuth org regardless of which
//     integration the rest of the run is exercising.

// placeholderValues are sentinel strings a maintainer can seed into a shared
// CI context so that every CIRCLECI_TEST_* variable this suite reads is
// visible and fillable in one place, without making an unfilled one look like
// a real fixture. testAccEnv treats any of these — compared case-insensitively
// after trimming surrounding whitespace — exactly like an unset variable.
// Without this, a seeded placeholder makes the variable *present*, so the
// test runs and fails against a nonsense identifier in a way that is
// indistinguishable from a real regression; with it, the test skips exactly
// as if the variable had never been set at all.
var placeholderValues = map[string]bool{
	"REPLACE_ME": true,
	"TODO":       true,
	"CHANGEME":   true,
}

// isPlaceholder reports whether value is one of placeholderValues, comparing
// case-insensitively after trimming whitespace.
func isPlaceholder(value string) bool {
	return placeholderValues[strings.ToUpper(strings.TrimSpace(value))]
}

// testAccEnv returns the value of the named acceptance-test environment
// variable, skipping the calling test when it is empty or holds one of
// placeholderValues. The skip message says which of the two it was, because
// "I set that variable and it still skipped" is otherwise a genuinely
// confusing five minutes for whoever seeded a placeholder and expected the
// test to run.
func testAccEnv(t *testing.T, name, description string) string {
	t.Helper()

	value := os.Getenv(name)

	switch {
	case value == "":
		t.Skipf("%s must be set for this acceptance test (%s); see the Development section of README.md.", name, description)
	case isPlaceholder(value):
		t.Skipf("%s is set to a placeholder value (%q), which counts as unset; it must be set to a real value for this acceptance test (%s); see the Development section of README.md.", name, value, description)
	}

	return value
}

// integrationKeys maps each value CIRCLECI_TEST_VCS_TYPE may take to the key
// used in that integration's environment variable names
// (CIRCLECI_TEST_<KEY>_<SUFFIX>). The suffix set is identical for every key —
// that uniformity is the point: giving a future integration a row here is
// the only code change needed for every dynamic helper below to resolve it.
//
// github_hybrid is not a duplicate of github_oauth. An organization connected
// by GitHub OAuth may *also* carry a GitHub App installation, and the two are
// distinguishable from outside: GET
// /api/v2/github-app/organization/{id}/installation answers 200 for such an
// organization and 404 for an OAuth-only one. Every project in it is still an
// OAuth project — the slug is `gh/<org>`, and project-level behaviour matches
// github_oauth — so what the extra key buys is a fixture for the github-app
// routes on an organization whose projects are not GitHub App projects, a
// combination neither GH_OAUTH nor GH_APP can express.
var integrationKeys = map[string]string{
	"github_app":         "GH_APP",
	"github_oauth":       "GH_OAUTH",
	"github_hybrid":      "GH_HYBRID",
	"github_server":      "GH_SERVER",
	"gitlab":             "GL_CLOUD",
	"gitlab_selfmanaged": "GL_SM",
	"bitbucket":          "BB_CLOUD",
}

// activeIntegrationKey resolves a CIRCLECI_TEST_VCS_TYPE value to its
// environment-variable key ("github_oauth" -> "GH_OAUTH"). It takes no
// *testing.T and touches no environment variable, so the mapping itself can
// be unit-tested directly — see TestActiveIntegrationKey and
// TestIntegrationKeysCoversEveryVCSType below — independent of testActiveEnv,
// which is what actually skips tests.
func activeIntegrationKey(vcsType string) (key string, ok bool) {
	key, ok = integrationKeys[vcsType]
	return key, ok
}

// testActiveEnv resolves a per-integration acceptance-test environment
// variable DYNAMICALLY: it reads CIRCLECI_TEST_VCS_TYPE, maps it through
// activeIntegrationKey to find which integration is active, and reads
// CIRCLECI_TEST_<key>_<suffix> — skipping, via testAccEnv, exactly as if that
// variable were unset, when it is unset or a placeholder. This is what lets
// testOrgID and the other "organization under test" helpers below read a
// GitHub App fixture in one run and a Bitbucket fixture in another with no
// VCS branching of their own.
func testActiveEnv(t *testing.T, suffix, description string) string {
	t.Helper()

	vcsType := testVCSType(t)

	key, ok := activeIntegrationKey(vcsType)
	if !ok {
		t.Skipf("CIRCLECI_TEST_VCS_TYPE=%s is not a recognized integration key (see acceptanceVCSTypes); "+
			"cannot resolve CIRCLECI_TEST_<key>_%s from it", vcsType, suffix)
	}

	return testAccEnv(t, "CIRCLECI_TEST_"+key+"_"+suffix, description)
}

// testOrgID returns the UUID of the primary test organization for the active
// integration.
func testOrgID(t *testing.T) string {
	t.Helper()

	return testActiveEnv(t, "ORG_ID", "UUID of the primary test organization for the active integration")
}

// testOrgSlug returns the slug of the primary test organization for the
// active integration, for example "circleci/<org-identifier>" or "gh/<org>".
func testOrgSlug(t *testing.T) string {
	t.Helper()

	return testActiveEnv(t, "ORG_SLUG", "slug of the primary test organization for the active integration")
}

// testOrgName returns the display name of the primary test organization for
// the active integration.
func testOrgName(t *testing.T) string {
	t.Helper()

	return testActiveEnv(t, "ORG_NAME", "name of the primary test organization for the active integration")
}

// testAltOrgID returns the UUID of a second test organization on the active
// integration, used by the tests that move a project between organizations.
func testAltOrgID(t *testing.T) string {
	t.Helper()

	return testActiveEnv(t, "ALT_ORG_ID", "UUID of a second test organization for the active integration")
}

// testAltOrgSlug returns the slug of the second test organization on the
// active integration.
func testAltOrgSlug(t *testing.T) string {
	t.Helper()

	return testActiveEnv(t, "ALT_ORG_SLUG", "slug of a second test organization for the active integration")
}

// testGithubOrgID returns the UUID of a GitHub OAuth-backed test
// organization. This is deliberately STATIC — a test that needs a GitHub
// OAuth org needs one regardless of which integration is otherwise active —
// so it always reads CIRCLECI_TEST_GH_OAUTH_ORG_ID rather than going through
// testActiveEnv.
func testGithubOrgID(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_GH_OAUTH_ORG_ID", "UUID of a GitHub OAuth-backed test organization")
}

// testGithubOrgSlug returns the slug of a GitHub OAuth-backed test
// organization, for example "gh/myorg". Static for the same reason as
// testGithubOrgID.
func testGithubOrgSlug(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_GH_OAUTH_ORG_SLUG", "slug of a GitHub OAuth-backed test organization")
}

// testProjectID returns the UUID of the project that pipelines, triggers,
// webhooks and context restrictions are created against, for the active
// integration.
func testProjectID(t *testing.T) string {
	t.Helper()

	return testActiveEnv(t, "PROJECT_ID", "UUID of the writable test project for the active integration")
}

// testProjectSlug returns the slug of the project that environment variables
// are created on, for the active integration, for example
// "circleci/<org>/<project>".
func testProjectSlug(t *testing.T) string {
	t.Helper()

	return testActiveEnv(t, "PROJECT_SLUG", "slug of the writable test project for the active integration")
}

// testStaticProjectID returns the UUID of a pre-existing project that is only
// ever read, never modified, on the active integration.
func testStaticProjectID(t *testing.T) string {
	t.Helper()

	return testActiveEnv(t, "STATIC_PROJECT_ID", "UUID of a pre-existing read-only project for the active integration")
}

// testStaticProjectSlug returns the slug of the pre-existing read-only
// project on the active integration.
func testStaticProjectSlug(t *testing.T) string {
	t.Helper()

	return testActiveEnv(t, "STATIC_PROJECT_SLUG", "slug of a pre-existing read-only project for the active integration")
}

// testStaticProjectName returns the name of the pre-existing read-only
// project on the active integration.
func testStaticProjectName(t *testing.T) string {
	t.Helper()

	return testActiveEnv(t, "STATIC_PROJECT_NAME", "name of a pre-existing read-only project for the active integration")
}

// testPipelineID returns the UUID of a pre-existing pipeline belonging to the
// project identified by testProjectID, on the active integration.
func testPipelineID(t *testing.T) string {
	t.Helper()

	return testActiveEnv(t, "PIPELINE_ID", "UUID of a pre-existing pipeline in the writable test project for the active integration")
}

// testGithubAppRepoExternalID returns the external (GitHub) ID of the
// repository reachable through the GitHub App integration. Static: this test
// specifically needs a GitHub App repository.
func testGithubAppRepoExternalID(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_GH_APP_REPO_EXTERNAL_ID", "GitHub App repository external ID")
}

// testGithubAppRepoName returns the full name ("owner/repo") of the
// repository reachable through the GitHub App integration. Static, for the
// same reason as testGithubAppRepoExternalID.
func testGithubAppRepoName(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_GH_APP_REPO_NAME", "GitHub App repository full name")
}

// testGithubServerProjectID returns the UUID of a project backed by a GitHub
// Server (self-hosted) integration. Static: this test specifically needs a
// GitHub Server project.
func testGithubServerProjectID(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_GH_SERVER_PROJECT_ID", "UUID of a GitHub Server backed project")
}

// testGithubServerPipelineID returns the UUID of a pipeline in the GitHub
// Server backed project. Static, for the same reason as
// testGithubServerProjectID.
func testGithubServerPipelineID(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_GH_SERVER_PIPELINE_ID", "UUID of a pipeline in the GitHub Server backed project")
}

// testGithubServerRepoExternalID returns the external ID of the repository
// reachable through the GitHub Server integration. Static, for the same
// reason as testGithubServerProjectID.
func testGithubServerRepoExternalID(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_GH_SERVER_REPO_EXTERNAL_ID", "GitHub Server repository external ID")
}

// testTriggerID returns the UUID of a pre-existing trigger for the active
// integration.
func testTriggerID(t *testing.T) string {
	t.Helper()

	return testActiveEnv(t, "TRIGGER_ID", "UUID of a pre-existing trigger for the active integration")
}

// testTriggerProjectID returns the UUID of the project owning the
// pre-existing trigger for the active integration.
func testTriggerProjectID(t *testing.T) string {
	t.Helper()

	return testActiveEnv(t, "TRIGGER_PROJECT_ID", "UUID of the project owning the pre-existing trigger for the active integration")
}

// testScheduledTriggerID returns the UUID of a pre-existing scheduled trigger
// belonging to the project identified by testStaticProjectID, for the active
// integration.
func testScheduledTriggerID(t *testing.T) string {
	t.Helper()

	return testActiveEnv(t, "SCHEDULED_TRIGGER_ID", "UUID of a pre-existing scheduled trigger for the active integration")
}

// testRunnerNamespace returns the runner namespace of the primary test
// organization for the active integration, used to build
// "<namespace>/<resource-class>" names.
func testRunnerNamespace(t *testing.T) string {
	t.Helper()

	return testActiveEnv(t, "RUNNER_NAMESPACE", "runner namespace of the primary test organization for the active integration")
}

// testAccClient builds a circleci.Client against the same installation the
// provider under test talks to, resolved from the same two environment
// variables the provider itself falls back to (CIRCLE_HOST, CIRCLE_DEPLOYMENT)
// so that a run against CircleCI Server reaches the same installation.
//
// Acceptance tests normally reach the API only through Terraform. A handful
// need a side channel instead — to establish or restore state Terraform has no
// operation for (testAccProjectSettingsClient), or to observe a value
// Terraform's own state never carries because the API refuses to disclose it,
// such as a webhook's signing secret (see Webhook.SigningSecret's doc
// comment). Call testAccPreCheck first: it is what resolves the active
// integration's token into CIRCLE_TOKEN.
func testAccClient(t *testing.T) *circleci.Client {
	t.Helper()

	return circleci.New(circleci.Config{
		Host:       os.Getenv("CIRCLE_HOST"),
		Token:      os.Getenv("CIRCLE_TOKEN"),
		Deployment: circleci.Deployment(os.Getenv("CIRCLE_DEPLOYMENT")),
	})
}

// testUniqueRunnerResourceClass returns a "<namespace>/<prefix>-<random>"
// resource class name for the active integration's runner namespace, unique to
// this run.
//
// Unique per run, not fixed, because CreateResourceClass answers HTTP 409 for a
// duplicate (see internal/circleci/runner.go). A resource class left behind by
// a run that died between create and destroy therefore makes *every* later run
// of the same test fail on the conflict, and the failure names the API's
// complaint rather than the leftover object — indistinguishable, from the log,
// from the provider having broken. The prefix keeps the leftovers identifiable
// as this suite's; the random tail is what stops them blocking the next run.
//
// The random part is rand.Text(), which is [A-Z2-7] and so is accepted by the
// class half of the resource-class grammar (see runnerResourceClassPattern:
// mixed case is fine after the "/", and there is no "." in it to trip the
// no-dots rule).
func testUniqueRunnerResourceClass(t *testing.T, prefix string) string {
	t.Helper()

	return fmt.Sprintf("%s/%s-%s", testRunnerNamespace(t), prefix, rand.Text())
}

// acceptanceVCSTypes lists every value CIRCLECI_TEST_VCS_TYPE may take. It
// mirrors the "CircleCI org slug shape" table in TESTING.md, and every entry
// here must have a matching row in integrationKeys above — see
// TestIntegrationKeysCoversEveryVCSType. CircleCI Server is deliberately not
// one of these seven: it is a separate axis (deployment, selected by
// CIRCLE_DEPLOYMENT), orthogonal to which VCS an organization is connected
// to, and every one of these seven can in principle run on it.
var acceptanceVCSTypes = []string{
	"github_app", "github_oauth", "github_hybrid", "gitlab", "gitlab_selfmanaged", "bitbucket", "github_server",
}

// testVCSType returns which VCS integration the configured fixtures belong
// to, skipping the calling test — the same convention as every other helper
// above — when CIRCLECI_TEST_VCS_TYPE is unset.
//
// Most resources behave identically on every integration, which is why every
// helper above it takes no VCS branching at all: the same variable names
// resolve to a GitHub App project in one CI run and a Bitbucket project in
// another, and most tests never need to know which. A test whose resource is
// *not* uniform across integrations (see README.md's compatibility matrix)
// should call testRequireVCSType instead of this directly, so that running it
// against the wrong fixture produces a named skip rather than a live-API
// failure indistinguishable from a regression.
func testVCSType(t *testing.T) string {
	t.Helper()

	return testAccEnv(t, "CIRCLECI_TEST_VCS_TYPE",
		"the VCS integration of the configured fixtures, one of "+strings.Join(acceptanceVCSTypes, ", "))
}

// TestIntegrationKeysCoversEveryVCSType keeps integrationKeys and
// acceptanceVCSTypes in sync. If a value is ever added to acceptanceVCSTypes
// without a matching row in integrationKeys, testActiveEnv would skip every
// dynamic helper for it with a "not a recognized integration key" message
// instead of resolving one — this catches that mismatch at `go test` time
// instead of at whatever acceptance run first exercises the new value.
func TestIntegrationKeysCoversEveryVCSType(t *testing.T) {
	for _, vcsType := range acceptanceVCSTypes {
		if _, ok := activeIntegrationKey(vcsType); !ok {
			t.Errorf("acceptanceVCSTypes contains %q, but integrationKeys has no matching entry for it", vcsType)
		}
	}
}

// TestActiveIntegrationKey is a permanent unit test of the pure
// vcsType -> key resolution, independent of environment variables or
// skipping.
func TestActiveIntegrationKey(t *testing.T) {
	cases := []struct {
		vcsType string
		wantKey string
		wantOK  bool
	}{
		{"github_app", "GH_APP", true},
		{"github_oauth", "GH_OAUTH", true},
		{"github_hybrid", "GH_HYBRID", true},
		{"github_server", "GH_SERVER", true},
		{"gitlab", "GL_CLOUD", true},
		{"gitlab_selfmanaged", "GL_SM", true},
		{"bitbucket", "BB_CLOUD", true},
		{"not_a_real_vcs_type", "", false},
		{"", "", false},
	}

	for _, c := range cases {
		key, ok := activeIntegrationKey(c.vcsType)
		if key != c.wantKey || ok != c.wantOK {
			t.Errorf("activeIntegrationKey(%q) = (%q, %v), want (%q, %v)", c.vcsType, key, ok, c.wantKey, c.wantOK)
		}
	}
}

// TestActiveEnvResolvesFromActiveIntegration proves the dynamic-resolution
// half of the naming scheme end to end: with CIRCLECI_TEST_VCS_TYPE set to
// github_oauth, a helper for "the organization under test" (testOrgID) must
// read CIRCLECI_TEST_GH_OAUTH_ORG_ID, not CIRCLECI_TEST_GH_APP_ORG_ID, even
// though both are set. Every dynamic helper above (testOrgID, testProjectID,
// testWebhookID, ...) is a thin wrapper around testActiveEnv, so this one
// test guards all of them against the "reads the wrong integration's
// variable" failure mode.
func TestActiveEnvResolvesFromActiveIntegration(t *testing.T) {
	t.Setenv("CIRCLECI_TEST_VCS_TYPE", "github_oauth")
	t.Setenv("CIRCLECI_TEST_GH_OAUTH_ORG_ID", "oauth-org-id")
	t.Setenv("CIRCLECI_TEST_GH_APP_ORG_ID", "app-org-id")

	got := testOrgID(t)

	if got != "oauth-org-id" {
		t.Errorf("testOrgID() = %q with CIRCLECI_TEST_VCS_TYPE=github_oauth, want %q (CIRCLECI_TEST_GH_OAUTH_ORG_ID); "+
			"resolving to the GH_APP value instead would mean dynamic resolution is reading the wrong integration",
			got, "oauth-org-id")
	}

	t.Setenv("CIRCLECI_TEST_VCS_TYPE", "github_app")

	got = testOrgID(t)

	if got != "app-org-id" {
		t.Errorf("testOrgID() = %q with CIRCLECI_TEST_VCS_TYPE=github_app, want %q (CIRCLECI_TEST_GH_APP_ORG_ID)",
			got, "app-org-id")
	}

	// github_hybrid is the arm most at risk of being "simplified" into an alias
	// for GH_OAUTH, because a hybrid organization *is* OAuth-connected. It must
	// resolve to its own fixtures: the whole reason the key exists is that the
	// two organizations differ (one carries a GitHub App installation, the other
	// does not), so reading the OAuth org's identifiers under github_hybrid would
	// silently test the wrong organization.
	//
	// The subtest is not decoration. testActiveEnv reports an unrecognised
	// CIRCLECI_TEST_VCS_TYPE by *skipping*, so calling testOrgID directly here
	// would make a missing integrationKeys row skip this test rather than fail
	// it — the assertion below would never run and the run would still be green.
	// Asserting that code after the call was reached is what turns that skip
	// into a failure.
	t.Setenv("CIRCLECI_TEST_VCS_TYPE", "github_hybrid")
	t.Setenv("CIRCLECI_TEST_GH_HYBRID_ORG_ID", "hybrid-org-id")

	resolved := ""
	ranPastTheSkip := false

	t.Run("github_hybrid", func(t *testing.T) {
		resolved = testOrgID(t)
		ranPastTheSkip = true
	})

	if !ranPastTheSkip {
		t.Error("testOrgID() skipped with CIRCLECI_TEST_VCS_TYPE=github_hybrid, which means " +
			"integrationKeys has no github_hybrid row and every dynamic helper is unresolvable there")
	}

	if ranPastTheSkip && resolved != "hybrid-org-id" {
		t.Errorf("testOrgID() = %q with CIRCLECI_TEST_VCS_TYPE=github_hybrid, want %q "+
			"(CIRCLECI_TEST_GH_HYBRID_ORG_ID); resolving to the GH_OAUTH value instead would mean "+
			"github_hybrid is aliased to the OAuth-only organization", resolved, "hybrid-org-id")
	}
}

// TestPlaceholderValueSkipsLikeUnset proves the placeholder sentinel: a
// variable set to any of placeholderValues must skip the same way an unset
// variable does, not be treated as a real (if nonsensical) fixture value.
//
// Deliberately not named with a TestAcc prefix: that prefix is reserved in
// this package for tests exercising a real acceptance-test resource (see
// "go test ./internal/provider/ -run TestAcc" in TESTING.md), and this is a
// unit test of testAccEnv itself.
func TestPlaceholderValueSkipsLikeUnset(t *testing.T) {
	for _, placeholder := range []string{"REPLACE_ME", "replace_me", "  TODO  ", "ChangeMe"} {
		t.Run(placeholder, func(t *testing.T) {
			t.Setenv("CIRCLECI_TEST_PLACEHOLDER_PROBE", placeholder)

			ranPastTheSkip := false

			t.Run("subtest", func(t *testing.T) {
				testAccEnv(t, "CIRCLECI_TEST_PLACEHOLDER_PROBE", "probe")
				ranPastTheSkip = true
			})

			if ranPastTheSkip {
				t.Errorf("testAccEnv did not skip for placeholder value %q", placeholder)
			}
		})
	}
}

// TestRealValuePassesThroughUnchanged proves the flip side: a real,
// non-placeholder value must not be treated as a placeholder. Not named with
// a TestAcc prefix for the same reason as TestPlaceholderValueSkipsLikeUnset
// above.
func TestRealValuePassesThroughUnchanged(t *testing.T) {
	t.Setenv("CIRCLECI_TEST_PLACEHOLDER_PROBE", "a4f1c2e0-real-uuid")

	got := testAccEnv(t, "CIRCLECI_TEST_PLACEHOLDER_PROBE", "probe")

	if got != "a4f1c2e0-real-uuid" {
		t.Errorf("testAccEnv() = %q, want the real value unchanged", got)
	}
}

// TestUniqueRunnerResourceClassIsUniquePerCallAndValid pins both halves of
// testUniqueRunnerResourceClass, because both are load-bearing and neither is
// observable from this repository any other way — every test that uses the
// helper needs a live CircleCI account with a runner namespace to run at all.
//
// Unique: a runner resource class is the one acceptance fixture in this package
// whose create genuinely conflicts. CreateResourceClass answers HTTP 409 for a
// duplicate (internal/circleci/runner.go), so the fixed names these tests used
// to build ("<namespace>/acc-test-runner" and friends) meant that a single run
// interrupted between create and destroy left every later run failing on a
// conflict with its own leftover — reported as an API error naming neither the
// leftover nor the test that leaked it.
//
// Valid: the randomised part has to survive the provider's own plan-time check.
// rand.Text() is [A-Z2-7], which the class half of runnerResourceClassPattern
// accepts (mixed case is fine after the "/", and there is no "." in it) — but
// that is a property of an implementation detail of the standard library, so it
// is asserted rather than assumed. If it ever stopped holding, every runner
// acceptance test would fail in the plan with "invalid resource_class format"
// and nothing would say why.
func TestUniqueRunnerResourceClassIsUniquePerCallAndValid(t *testing.T) {
	t.Setenv("CIRCLECI_TEST_VCS_TYPE", "github_app")
	t.Setenv("CIRCLECI_TEST_GH_APP_RUNNER_NAMESPACE", "acc-ns")

	first := testUniqueRunnerResourceClass(t, "acc-test-runner")
	second := testUniqueRunnerResourceClass(t, "acc-test-runner")

	if first == second {
		t.Errorf("testUniqueRunnerResourceClass returned %q twice; a fixed resource class name makes "+
			"every run after an interrupted one fail with HTTP 409 against its own leftover", first)
	}

	for _, name := range []string{first, second} {
		if !strings.HasPrefix(name, "acc-ns/acc-test-runner-") {
			t.Errorf("testUniqueRunnerResourceClass() = %q, want it in the active integration's "+
				"namespace with the given prefix, so leftovers are identifiable as this suite's", name)
		}

		if !runnerResourceClassPattern.MatchString(name) {
			t.Errorf("testUniqueRunnerResourceClass() = %q, which the provider's own plan-time "+
				"resource_class check rejects (%s)", name, runnerResourceClassFormatMessage)
		}
	}
}
