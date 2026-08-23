// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"terraform-provider-circleci/internal/circleci"
)

// A small, general-purpose harness for acceptance tests that create real
// objects, so that any number of jobs can run this package's suite AT THE
// SAME TIME — against the same organization or different ones — without one
// run's fixtures colliding with, or being torn down by, another's.
//
// Three problems, three helpers:
//
//   - NAMING. testUniqueName gives every created object a name that cannot
//     collide with another concurrent run's, another job's, or a run that
//     died halfway through last night, while still telling a human debugging
//     a leftover which test made it, on which integration, and roughly when.
//   - CLEANUP. testRegisterCleanup wraps t.Cleanup so that a delete which
//     itself fails is a loud test failure, never a silently leaked object —
//     see the two paragraphs below testRegisterCleanup for why that is worth
//     a helper at all.
//   - VERIFICATION. testAssertAbsent and its typed wrappers (for context,
//     group, project and runner resource class — the object types this
//     harness creates directly) confirm an object is actually gone by
//     re-listing its collection, because (measured over the network, see
//     TESTING.md) a single status code lies about absence differently per
//     service: a deleted context answers 403, a deleted group answers 403, a
//     missing budget's own delete answers 500. None of those is IsNotFound,
//     and none is safe to trust alone — a budget has no name to give it a
//     unique per-test identity in the first place (see budget.go), so it has
//     no wrapper here, but the same lesson is why every wrapper that does
//     exist re-lists rather than re-GETs.
//
// None of this replaces terraform-plugin-testing's own destroy step for a
// resource under test — resource.Test already tears down what it applied,
// even after a failed assertion earlier in the same TestCase. It exists for
// everything a test creates *around* the resource under test: a project to
// attach a context restriction to, a second organization to move a project
// into, a runner resource class to grant a token against — the private
// fixtures that testProjectID and friends used to be, one shared mutable
// object per integration, before this file existed.

// testRunStamp is computed once per process and shared by every object this
// run creates. It deliberately combines two sources rather than leaning on
// either alone:
//
//   - a coarse (minute-resolution) UTC timestamp, so a name states roughly
//     when it was created without a human having to decode anything — this
//     is the part a maintainer reads first when triaging a leftover;
//   - the CI identifier when one is available (CIRCLE_WORKFLOW_ID, falling
//     back to CIRCLE_BUILD_NUM), so a leftover can be traced to the exact
//     job that made it, not just the minute.
//
// It is NOT the sole source of uniqueness — two runs started in the same
// minute (routine on a developer machine with no CI id at all, where both
// fall back to "local") must not collide, which is what the random suffix in
// testUniqueName is for. And it is not random-only either: a bare random
// string tells a human nothing about when or where an object came from, which
// is the failure mode the task description calls out explicitly (a wall
// clock alone is fine to read but must not be the only source of
// uniqueness; randomness alone is unique but tells nobody anything).
var testRunStampOnce = sync.OnceValue(func() string {
	when := time.Now().UTC().Format("0102-1504") // MMDD-HHMM, UTC, minute resolution.

	ci := "local"
	switch {
	case os.Getenv("CIRCLE_WORKFLOW_ID") != "":
		id := os.Getenv("CIRCLE_WORKFLOW_ID")
		if len(id) > 8 {
			id = id[:8]
		}
		ci = id
	case os.Getenv("CIRCLE_BUILD_NUM") != "":
		ci = "b" + os.Getenv("CIRCLE_BUILD_NUM")
	}

	return when + "-" + ci
})

// testRunStamp returns the per-process run stamp described above.
func testRunStamp() string { return testRunStampOnce() }

// testNameSanitizer collapses everything outside [a-z0-9] to a single hyphen,
// so the slug it produces is safe on every resource type this harness names:
// contexts, groups, projects and organizations all accept lower-case
// alphanumerics and hyphens, and this is deliberately the intersection of
// their charsets rather than the most permissive one, so one function works
// for all of them.
var testNameSanitizer = regexp.MustCompile(`[^a-z0-9]+`)

// testNameSlug reduces a *testing.T.Name() to a short, safe fragment: the
// root test name (before any "/subtest" a t.Run added), lower-cased, with the
// "TestAcc" prefix dropped (every real acceptance test carries it, and
// repeating it in every object name would waste the length budget on
// something that is not information), non-alphanumerics collapsed to
// hyphens, and truncated to maxLen.
//
// Pure and independent of any environment variable or live account, so it is
// unit-tested directly — see TestTestNameSlug.
func testNameSlug(name string, maxLen int) string {
	root := name
	if i := strings.IndexByte(root, '/'); i >= 0 {
		root = root[:i]
	}

	root = strings.TrimPrefix(root, "TestAcc")

	slug := testNameSanitizer.ReplaceAllString(strings.ToLower(root), "-")
	slug = strings.Trim(slug, "-")

	if len(slug) > maxLen {
		slug = strings.Trim(slug[:maxLen], "-")
	}

	return slug
}

// testUniqueNameSlugLen and testUniqueNameRandLen bound testUniqueName's
// output so it fits the tightest name-length constraint among the resource
// types this harness names today: a context name is documented (see
// FindContextByName in internal/circleci/context.go) as accepted up to 50
// characters server-side. testMaxUniqueNameLen below is asserted against by
// TestUniqueNameFitsTightestConstraint using the longest real TestAcc name in
// this package, so a future long test name fails a fast unit test rather than
// a live create call.
const (
	testUniqueNameKindLen = 6
	testUniqueNameSlugLen = 10
	testUniqueNameRandLen = 6
	testMaxUniqueNameLen  = 50
)

// testUniqueName returns a name of the form
//
//	tf-acc-<kind>-<test-slug>-<MMDD-HHMM-ci>-<random>
//
// unique to this call, this test, this process and this run — safe for any
// number of jobs, on any number of organizations, to create objects with at
// the same instant. kind identifies the resource ("ctx", "grp", "proj", ...)
// so that two objects of different types created by the same test never
// collide either, even though nothing here requires that.
//
// This is the naming half of the harness described at the top of this file.
// Every call is independent — call it once per object, not once per test —
// so a test that creates three contexts gets three distinct names, each
// still traceable back to the same test and run.
func testUniqueName(t *testing.T, kind string) string {
	t.Helper()

	// kind is clamped too, defensively: every call site in this package uses
	// a short one ("ctx", "grp", "proj", "rrc"), but the length guarantee in
	// TestUniqueNameFitsTightestConstraint should hold regardless of what a
	// future caller passes, not merely for today's call sites.
	if len(kind) > testUniqueNameKindLen {
		kind = kind[:testUniqueNameKindLen]
	}

	random := strings.ToLower(rand.Text())
	if len(random) > testUniqueNameRandLen {
		random = random[:testUniqueNameRandLen]
	}

	return fmt.Sprintf("tf-acc-%s-%s-%s-%s", kind, testNameSlug(t.Name(), testUniqueNameSlugLen), testRunStamp(), random)
}

// testRegisterCleanup registers del to run via t.Cleanup, and turns a failed
// delete into a visible test failure rather than a leaked object.
//
// WHY THIS IS A HELPER AND NOT JUST t.Cleanup(func() { _ = del() }). A
// delete call whose error is silently discarded is exactly how a leaked
// object goes unreported — this project has already had that happen twice.
// t.Errorf (not
// t.Fatalf) is deliberate: Cleanup functions already run after the test body
// has finished, most-recently-registered first, and calling Fatal from
// inside one of them only unwinds that one function, not the others still
// queued — Errorf reports the failure without cutting the remaining cleanups
// short.
//
// LIMITS, stated rather than assumed away. t.Cleanup funcs run after
// t.Fatal/t.FailNow (both unwind via runtime.Goexit, which still runs
// deferred and Cleanup-registered work) and after a recovered panic within a
// subtest. They do NOT run if the process itself dies: an unrecovered panic
// that reaches the top of the test goroutine, the binary being SIGKILLed by
// CircleCI's no-output timeout, or an interrupted `go test`. Nothing running
// inside that same process can fix that — it is exactly the gap
// .circleci/scripts/find-leaked-fixtures.sh exists to close after the fact,
// by finding the object from the outside instead of relying on the test
// that made it to say so.
func testRegisterCleanup(t *testing.T, description string, del func() error) {
	t.Helper()

	t.Cleanup(func() {
		if err := del(); err != nil {
			t.Error(testCleanupFailureMessage(description, err))
		}
	})
}

// testCleanupFailureMessage builds the message testRegisterCleanup reports
// when del fails, as a pure function so its wording is unit-testable
// (TestCleanupFailureMessageNamesTheLeakDetector) without a live *testing.T
// whose own failure would otherwise propagate to whatever test exercised it
// — every ancestor of a failed subtest is reported failed in `go test`, with
// no way for a parent to observe and then clear that, so "prove this reports
// a failure" and "prove this test still passes" cannot both be asserted
// through one real t.Run. Separating the message from the t.Error call sidesteps
// that entirely: this function is checked directly, and only the
// non-failing path (TestRegisterCleanupIsSilentOnSuccessfulDelete) is ever
// driven through a real subtest.
func testCleanupFailureMessage(description string, err error) string {
	return fmt.Sprintf("cleanup failed for %s: %v -- this object was very likely NOT deleted; "+
		"run .circleci/scripts/find-leaked-fixtures.sh to confirm and remove it by hand",
		description, err)
}

// testAssertAbsent fails the test (via t.Errorf, so any other checks in the
// same test still run) unless id is missing from a fresh call to list.
//
// This is the verification half of the harness. It exists because this
// package's own measurements show a single status code is not a trustworthy
// "it's gone" signal, and the failure mode is different for every service: a
// context that no longer exists answers 403 (indistinguishable, by status
// alone, from "you lack access to a context that still exists"); a group
// answers 403 the same way; a budget's own DELETE answers 500 regardless of
// whether it worked. Re-listing the collection and checking for absence is
// the one signal every one of these services answers unambiguously — an id
// either comes back in the list or it does not — so that is what every typed
// wrapper below does, rather than trusting whatever DELETE or a single GET
// reported.
func testAssertAbsent(t *testing.T, description, id string, list func() ([]string, error)) {
	t.Helper()

	ids, err := list()

	if ok, message := testAbsentCheckResult(description, id, ids, err); !ok {
		t.Error(message)
	}
}

// testAbsentCheckResult is testAssertAbsent's decision, pulled out as a pure
// function for the same reason testCleanupFailureMessage is: a live subtest
// that deliberately fails still marks every ancestor test failed, so the
// "present" and "listing failed" branches are unit-tested by calling this
// directly (TestAbsentCheckResult) rather than through t.Error/t.Run.
func testAbsentCheckResult(description, id string, ids []string, err error) (ok bool, message string) {
	if err != nil {
		return false, fmt.Sprintf("could not confirm %s (id %s) is gone: listing its collection failed: %v",
			description, id, err)
	}

	if slices.Contains(ids, id) {
		return false, fmt.Sprintf("%s (id %s) is still present in its collection after delete", description, id)
	}

	return true, ""
}

// testAPIClient returns a *circleci.Client authenticated the same way this
// test's Terraform provider instance is: activeIntegrationToken/testAccPreCheck
// resolve CIRCLECI_TEST_<key>_TOKEN or CIRCLE_TOKEN and export it as
// CIRCLE_TOKEN via t.Setenv before any test body runs, so reading CIRCLE_TOKEN
// here after PreCheck has already run picks up the same credential — the
// same pattern TestAccOrganizationCircleCiResource's CheckDestroy already
// uses.
//
// This is for the objects a test manages directly, alongside (not instead
// of) the resource under test that terraform-plugin-testing already drives
// through the provider.
func testAPIClient(t *testing.T) *circleci.Client {
	t.Helper()

	token := os.Getenv("CIRCLE_TOKEN")
	if token == "" {
		t.Skip("CIRCLE_TOKEN is not set; testAccPreCheck should have skipped before this was reached")
	}

	return circleci.New(circleci.Config{Token: token})
}

// ---- typed "really gone" checks ----
//
// One per resource type this harness creates directly, each built on
// testAssertAbsent and the client's own List method, so no call site has to
// re-derive which status code to (not) trust.
//
// A DIFFERENT GOTCHA LIVES IN THE DELETE SIDE, not here: a testRegisterCleanup
// callback that calls DeleteContext or the group service's Delete directly
// must treat a 403 (circleci.IsUnauthorized), not only a 404
// (circleci.IsNotFound), as "already gone" -- both routes answer 403 for a
// context or group that no longer exists (see DeleteContext's doc comment in
// internal/circleci/context.go, and context_resource.go's own Delete, which
// already applies this). Getting this wrong does not fail loudly with a
// wrong verdict; it fails loudly with the RIGHT verdict for the wrong
// reason: a cleanup that ran a second time (after the test's own explicit
// delete already succeeded) reports the object as un-deletable when it was
// deleted the first time. Caught exactly this way, live, the first time
// TestAccHarnessContextLifecycle ran against a real organization -- see its
// own comment for what that looked like.

// testAssertContextGone confirms a context no longer appears in its
// organization's context list.
func testAssertContextGone(t *testing.T, client *circleci.Client, orgID, contextID string) {
	t.Helper()

	testAssertAbsent(t, "context", contextID, func() ([]string, error) {
		contexts, err := client.ListContexts(t.Context(), orgID)
		if err != nil {
			return nil, err
		}

		ids := make([]string, len(contexts))
		for i, c := range contexts {
			ids[i] = c.ID
		}

		return ids, nil
	})
}

// testAssertGroupGone confirms a group no longer appears in its
// organization's group list.
func testAssertGroupGone(t *testing.T, client *circleci.Client, orgID, groupID string) {
	t.Helper()

	testAssertAbsent(t, "group", groupID, func() ([]string, error) {
		groups, err := client.Groups().List(t.Context(), orgID)
		if err != nil {
			return nil, err
		}

		ids := make([]string, len(groups))
		for i, g := range groups {
			ids[i] = g.ID
		}

		return ids, nil
	})
}

// testAssertResourceClassGone confirms a runner resource class no longer
// appears in its organization's resource-class list.
func testAssertResourceClassGone(t *testing.T, client *circleci.Client, orgID, resourceClassID string) {
	t.Helper()

	testAssertAbsent(t, "runner resource class", resourceClassID, func() ([]string, error) {
		classes, err := client.ListResourceClasses(t.Context(), "", orgID)
		if err != nil {
			return nil, err
		}

		ids := make([]string, len(classes))
		for i, c := range classes {
			ids[i] = c.ID
		}

		return ids, nil
	})
}

// testAssertProjectGone confirms a project answers IsNotFound on a fresh
// read.
//
// This is the one exception to "never trust a single status": a deleted
// project's slug is documented (internal/circleci/project.go, DeleteProject)
// as answering a real 404, measured over the network against a freshly
// deleted standalone project, unlike context/group/budget above. There is
// also no list-projects route to re-check against instead (measured while
// building this harness: neither GET /organization/{id}/projects nor the v2
// insights org summary exists; the only working listing is the account-wide,
// v1.1 GET /projects used by find-leaked-fixtures.sh, which is too broad and
// too slow to call from every test). So GetProject is the correct check here,
// not a shortcut.
func testAssertProjectGone(t *testing.T, client *circleci.Client, slug string) {
	t.Helper()

	_, err := client.GetProject(t.Context(), slug)
	if err == nil {
		t.Errorf("project %s is still readable after delete", slug)
		return
	}

	if !circleci.IsNotFound(err) {
		t.Errorf("could not confirm project %s is gone: GetProject failed with a non-404 error: %v", slug, err)
	}
}

// testCreateStandaloneProject creates a project with a unique name in a
// standalone organization and registers its cleanup, so a test can hold a
// project of its own rather than reading and writing the shared testProjectID
// fixture every other test in the suite also depends on.
//
// Only works on a standalone organization: a classic, VCS-backed organization
// has no route that creates a project, only one that adopts a pre-existing
// repository. Callers on a classic organization should use
// testAdoptableGithubRepo (project_resource_test.go) instead, not this — it
// provisions the repository to adopt via the `gh` CLI rather than depending
// on one already existing.
func testCreateStandaloneProject(t *testing.T, client *circleci.Client, orgID string) *circleci.Project {
	t.Helper()

	name := testUniqueName(t, "proj")

	project, err := client.CreateProject(t.Context(), orgID, name)
	if err != nil {
		t.Fatalf("creating a per-test project %q in organization %s: %v", name, orgID, err)
	}

	testRegisterCleanup(t, fmt.Sprintf("project %s (%s)", project.Slug, name), func() error {
		// A background context, not t.Context(): this runs from t.Cleanup,
		// after the test body has returned, and t.Context() is documented to
		// be canceled once the test has finished -- exactly the window this
		// call runs in. Using it here would make the delete racy against the
		// same teardown that is supposed to run it.
		if err := client.DeleteProject(context.Background(), project.Slug); err != nil && !circleci.IsNotFound(err) {
			return fmt.Errorf("deleting project %s: %w", project.Slug, err)
		}

		return nil
	})

	return project
}

// TestTestNameSlug is a permanent unit test of testNameSlug, independent of
// any environment variable or live account.
func TestTestNameSlug(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		maxLen int
		want   string
	}{
		{"TestAccFoo", 10, "foo"},
		{"TestAccFoo", 2, "fo"},
		// Only the root test name before the first "/" counts: a subtest's
		// own name never feeds the slug (matching isRealAcceptanceTestName's
		// root-only convention in vcs_gating_test.go).
		{"TestAccFoo/subtest/deeper", 10, "foo"},
		{"TestAccUPPERCase123", 20, "uppercase123"},
		// Runs of non-alphanumerics (underscore, dot, ...) collapse to one
		// hyphen each, and a leading/trailing hyphen left by truncation or by
		// stripping "TestAcc" is trimmed.
		{"TestAccHas_Under_and.Dots", 30, "has-under-and-dots"},
		// Not TestAcc*-prefixed: nothing is stripped, only lower-cased and
		// truncated.
		{"NotTestAccPrefixed", 10, "nottestacc"},
		{"", 10, ""},
	}

	for _, c := range cases {
		if got := testNameSlug(c.name, c.maxLen); got != c.want {
			t.Errorf("testNameSlug(%q, %d) = %q, want %q", c.name, c.maxLen, got, c.want)
		}
	}
}

// TestUniqueNameFitsTightestConstraint proves the length bound testUniqueName
// promises holds even in the worst case this package's own call sites could
// hit: the longest CI-id fallback (an 8-character CIRCLE_WORKFLOW_ID) and a
// kind longer than any real call site passes. If this ever failed, the
// failure a maintainer would otherwise see is a live create call rejected
// for a name that is too long -- a real-API error that reads like a
// regression in the resource, not in this helper.
func TestUniqueNameFitsTightestConstraint(t *testing.T) {
	t.Setenv("CIRCLE_WORKFLOW_ID", "12345678-abcd-ef01-2345-6789abcdef01")

	got := testUniqueName(t, "a-kind-name-far-longer-than-any-real-caller-uses")

	if len(got) > testMaxUniqueNameLen {
		t.Errorf("testUniqueName() = %q (%d chars), want at most %d -- a context name is documented as "+
			"accepted only up to 50 characters server-side", got, len(got), testMaxUniqueNameLen)
	}
}

// TestUniqueNameIsUniquePerCallAndTraceable pins both halves of
// testUniqueName: two calls from the same test never collide, and the
// result carries this test's own name and the run stamp, so a human looking
// at a leftover object can tell which test made it without reading any code.
func TestUniqueNameIsUniquePerCallAndTraceable(t *testing.T) {
	t.Setenv("CIRCLE_WORKFLOW_ID", "") // exercise the "local" fallback deterministically
	t.Setenv("CIRCLE_BUILD_NUM", "")

	first := testUniqueName(t, "ctx")
	second := testUniqueName(t, "ctx")

	if first == second {
		t.Fatalf("testUniqueName returned %q twice; two objects created by the same test would collide", first)
	}

	// This test's own name is not TestAcc*-prefixed, so testNameSlug lower-cases
	// and truncates it without stripping anything: "TestUniqueNameIsUnique..."
	// becomes "testunique" at the 10-character slug budget.
	wantPrefix := "tf-acc-ctx-" + testNameSlug(t.Name(), testUniqueNameSlugLen) + "-"

	for _, name := range []string{first, second} {
		if !strings.HasPrefix(name, wantPrefix) {
			t.Errorf("testUniqueName() = %q, want it to start with %q (the kind and this test's slug), "+
				"so a leftover names which test made it", name, wantPrefix)
		}

		if !strings.Contains(name, testRunStamp()) {
			t.Errorf("testUniqueName() = %q, want it to contain the run stamp %q, so a leftover names "+
				"roughly when and (in CI) which job made it", name, testRunStamp())
		}
	}
}

// TestRunStampIsStableWithinAProcess pins that every object created in one
// `go test` invocation shares the same stamp: testRunStamp is computed once
// (sync.OnceValue), not per call, which is what makes every object from one
// run recognizable as a set.
func TestRunStampIsStableWithinAProcess(t *testing.T) {
	if a, b := testRunStamp(), testRunStamp(); a != b {
		t.Errorf("testRunStamp() returned %q then %q; it must be stable within one process", a, b)
	}
}

// TestCleanupFailureMessageNamesTheLeakDetector pins the one property that
// matters about testRegisterCleanup's failure message: it must name both the
// object (via description) and the leak detector, so a maintainer reading a
// failed run knows there is a next step and what it is. It is checked as a
// pure function — see testCleanupFailureMessage's doc comment for why: a
// live subtest that deliberately triggers t.Error also marks this test
// failed in `go test`'s own accounting, with no way for the parent to then
// report itself as passing, so the failing path cannot be driven through a
// real *testing.T at all and still leave this test green.
func TestCleanupFailureMessageNamesTheLeakDetector(t *testing.T) {
	t.Parallel()

	got := testCleanupFailureMessage("project tf-acc-proj-x", errors.New("HTTP 500"))

	for _, want := range []string{"project tf-acc-proj-x", "HTTP 500", "find-leaked-fixtures.sh"} {
		if !strings.Contains(got, want) {
			t.Errorf("testCleanupFailureMessage() = %q, want it to contain %q", got, want)
		}
	}
}

// TestRegisterCleanupIsSilentOnSuccessfulDelete drives testRegisterCleanup
// through a real subtest for the one outcome that is safe to: a successful
// delete must not fail the test. The failing-delete outcome is pinned above
// through testCleanupFailureMessage instead, for the reason given there.
func TestRegisterCleanupIsSilentOnSuccessfulDelete(t *testing.T) {
	ok := t.Run("subtest", func(t *testing.T) {
		testRegisterCleanup(t, "probe object", func() error { return nil })
	})

	if !ok {
		t.Error("testRegisterCleanup failed the test even though the delete function succeeded")
	}
}

// TestAbsentCheckResult pins all three outcomes testAssertAbsent has to
// distinguish, as a pure function for the same reason
// TestCleanupFailureMessageNamesTheLeakDetector is: the "present" and
// "listing failed" branches both want to observe a failure, and a live
// subtest that fails would mark this test failed too, with no way back.
// TestAssertAbsentPassesWhenMissing below still drives the real function
// through a real subtest for the one outcome — success — where that is safe.
func TestAbsentCheckResult(t *testing.T) {
	t.Parallel()

	t.Run("absent passes", func(t *testing.T) {
		ok, message := testAbsentCheckResult("probe", "id-1", []string{"id-2", "id-3"}, nil)
		if !ok || message != "" {
			t.Errorf("testAbsentCheckResult() = (%v, %q), want (true, \"\")", ok, message)
		}
	})

	t.Run("present fails, naming the id", func(t *testing.T) {
		ok, message := testAbsentCheckResult("probe", "id-1", []string{"id-1", "id-2"}, nil)
		if ok {
			t.Error("testAbsentCheckResult() reported ok even though the id was still present in the list; " +
				"a single status code lying about absence is exactly what this helper must not trust")
		}
		if !strings.Contains(message, "id-1") || !strings.Contains(message, "still present") {
			t.Errorf("testAbsentCheckResult() message = %q, want it to name the id and say it is still present", message)
		}
	})

	t.Run("a listing failure fails, distinctly worded", func(t *testing.T) {
		ok, message := testAbsentCheckResult("probe", "id-1", nil, errors.New("listing failed"))
		if ok {
			t.Error("testAbsentCheckResult() reported ok even though it could not list the collection at all")
		}
		if !strings.Contains(message, "listing failed") || !strings.Contains(message, "could not confirm") {
			t.Errorf("testAbsentCheckResult() message = %q, want it to say the listing itself failed, "+
				"distinctly from \"still present\"", message)
		}
	})
}

// TestAssertAbsentPassesWhenMissing drives the real testAssertAbsent through
// a real subtest for the one outcome that is safe to: nothing found, so
// nothing fails.
func TestAssertAbsentPassesWhenMissing(t *testing.T) {
	ok := t.Run("subtest", func(t *testing.T) {
		testAssertAbsent(t, "probe", "id-1", func() ([]string, error) {
			return []string{"id-2", "id-3"}, nil
		})
	})

	if !ok {
		t.Error("testAssertAbsent failed even though the id was missing from the list")
	}
}

// TestAssertResourceClassGone is the one typed "really gone" wrapper with no
// live demonstration alongside TestAccHarnessContextLifecycle and its
// siblings: creating a runner resource class needs a namespace already
// claimed in the organization (see
// CreateResourceClass's doc comment in internal/circleci/runner.go), and
// none of the four acceptance organizations has one configured today
// (CIRCLECI_TEST_<KEY>_RUNNER_NAMESPACE is unset in every job in
// .circleci/config.yml). So this exercises it [FAKE] instead, against a
// minimal httptest stand-in for the legacy runner API's one list route
// (GET /api/v3/runner/resource) -- enough to prove the id-in-list logic this
// wrapper adds on top of the already-live-proven testAssertAbsent, without
// standing up the full runner fake runner_fake_test.go maintains for the
// provider-level resource tests.
func TestAssertResourceClassGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []circleci.ResourceClass{
				{ID: "still-here", ResourceClass: "acme/still-here"},
			},
		})
	}))
	defer server.Close()

	client := circleci.New(circleci.Config{Token: "fake-token", RunnerHost: server.URL})

	t.Run("absent passes", func(t *testing.T) {
		ok := t.Run("subtest", func(t *testing.T) {
			testAssertResourceClassGone(t, client, "org-1", "long-gone")
		})

		if !ok {
			t.Error("testAssertResourceClassGone failed for an id absent from the fake list")
		}
	})

	// The "still present" branch is exercised directly through
	// testAbsentCheckResult already (TestAbsentCheckResult); repeating it
	// here as an intentionally-failing subtest would only mark this test
	// failed too, for the reason documented on testCleanupFailureMessage.
}

// TestAPIClientSkipsWithoutToken guards testAPIClient's fallback, using the
// same t.Run-and-check-Skipped idiom the other gating self-tests in this
// package use, so its own correctness is observable without a live account.
func TestAPIClientSkipsWithoutToken(t *testing.T) {
	t.Setenv("CIRCLE_TOKEN", "")

	skipped := false

	t.Run("subtest", func(t *testing.T) {
		t.Cleanup(func() {
			skipped = t.Skipped()
		})

		testAPIClient(t)
	})

	if !skipped {
		t.Error("testAPIClient did not skip with CIRCLE_TOKEN unset")
	}
}
