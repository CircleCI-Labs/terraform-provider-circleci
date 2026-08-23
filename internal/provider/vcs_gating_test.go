// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"terraform-provider-circleci/internal/circleci"
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

// --- Network-vs-fake coverage: a mechanism that cannot be forgotten ---
//
// vcsCoverage above is opt-in: it only grows when a test calls
// testRequireVCSType or testRequireStandaloneOrg. That missed a whole
// category of real-API tests that never needed either gate -- the iOS
// signing, notification, organization-contacts and URL-orb-allow-list tests
// (ios_signing_realapi_test.go, notification_channel_config_resource_realapi_test.go,
// notification_data_sources_realapi_test.go, organization_contacts_resource_test.go,
// url_orb_allow_list_entry_net_test.go, organization_data_source_test.go). They
// call testAccPreCheck and testOrgID like everything else, but their resource
// behaves identically on every VCS integration, so there is nothing for them to
// gate on. A real run against github_app counted 24 of these as zero; they had
// run, against the real API, and vcsCoverage never heard about it.
//
// A second opt-in call (say, "every RealAPI/Net test must also call
// testRecordRealAPICoverage") would only relocate the same failure mode: a new
// test that forgets the call is undercounted exactly as before, silently. So
// this does not add a call for authors to remember. It instruments the one
// thing every acceptance test in this package already does regardless of which
// gates it uses or forgets: it configures the provider, through
// testAccProtoV6ProviderFactories (acctest_test.go's "used to instantiate a
// provider during acceptance testing" factory), before Terraform can run a
// single plan or apply.
//
// The instrument lives entirely in this file:
//
//  1. init() below wraps testAccProtoV6ProviderFactories["circleci"] once, for
//     the whole test binary. Every test that reaches resource.Test/UnitTest/
//     ParallelTest -- which is every acceptance test in this package, see
//     TestEveryTestAccFunctionUsesAnInstrumentedRunner -- gets the wrapped
//     server with no second call required.
//  2. The wrapper's ConfigureProvider decodes the *effective* host Terraform
//     configured the provider with (the config's own "host" attribute if set,
//     else CIRCLE_HOST, else circleci.DefaultHost -- the same three-step
//     fallback Configure itself uses in provider.go) and classifies it: a
//     loopback address is one of this package's own httptest fakes, an RFC
//     2606/6761 reserved domain (this package's plan-time-rejection tests use
//     "circleci.example.com" on purpose) is a fake by construction too, and
//     anything else is the real API -- see isKnownFakeHost.
//  3. Attribution to a specific test survives ConfigureProvider running on the
//     go-plugin server's own goroutine, not the test's: terraform-plugin-testing
//     calls the factory synchronously, on the calling test's own goroutine, once
//     per Terraform CLI invocation (its internal runProviderCommand helper).
//     A runtime.Stack capture taken *inside the factory* therefore always shows
//     a frame naming the running test, fully qualified by this module's own
//     import path -- "terraform-provider-circleci/internal/provider.TestXxx",
//     which cannot collide with some unrelated package also named "provider" --
//     and that name is closed over by the wrapped server instance, so
//     ConfigureProvider reads a plain field rather than re-deriving identity on
//     whatever goroutine actually calls it. Verified for both a direct call and
//     a t.Run subtest closure; see TestCurrentAcceptanceTestNameFromStack and
//     TestCurrentAcceptanceTestNameFromStackInSubtest.
//
// This cannot silently miss a test the way the opt-in gates could: there is no
// second call to skip, and a test that reaches resource.Test/UnitTest at all
// is, by construction, a test whose provider gets configured, which is the one
// event this instrument is watching for. The only way to defeat it is to not
// call resource.Test/UnitTest/ParallelTest at all -- which means the test never
// touched the provider, real or fake, and correctly owes this report nothing.
//
// What it cannot do is name a test when the stack search itself fails (a future
// call shape this package does not use today). That failure mode is not
// silent either: recordNetworkCoverage still counts it, in
// networkCoverageState.unattributedReal/unattributedFake, and
// printVCSCoverageSummary surfaces that count rather than dropping it.
var acceptanceTestFrame = regexp.MustCompile(
	`(?m)^terraform-provider-circleci/internal/provider\.(Test[A-Za-z0-9_]+)[.(]`)

// currentAcceptanceTestName reports the name of the top-level Test function
// whose goroutine is currently executing, by reading it back out of a stack
// trace of the calling goroutine. It works from inside a t.Run subtest too:
// Go names a closure "EnclosingFunc.funcN", so the regex above matches the
// leading "EnclosingFunc" and stops before the ".funcN" suffix regardless of
// nesting depth.
//
// It returns ok=false only if no frame in this package's import path matches
// "Test*" at all, which would mean this function was called from somewhere
// that is not, even transitively, a test goroutine running a Test* function --
// not something this file's own call site (the wrapped factory) can produce
// today, but callers must not assume a name is always available.
func currentAcceptanceTestName() (string, bool) {
	buf := make([]byte, 4096)
	for {
		n := runtime.Stack(buf, false)
		if n < len(buf) || len(buf) >= 1<<20 {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}

	matches := acceptanceTestFrame.FindAllStringSubmatch(string(buf), -1)
	if len(matches) == 0 {
		return "", false
	}

	// The last match is the outermost (closest to testing.tRunner) frame in
	// this package, i.e. the actual top-level Test function -- as opposed to
	// some inner helper that happened to be named Test*, which nothing in this
	// package is (see TestIsRealAcceptanceTestName's naming rule, which every
	// real acceptance test already follows).
	return matches[len(matches)-1][1], true
}

// providerConfigObjectType derives the tftypes.Object type of the provider's
// own configuration block directly from CircleCiProvider.Schema, rather than
// hardcoding "host, key, runner_host, deployment" a second time here. If a
// future attribute is added to the schema, this stays correct automatically;
// a hardcoded copy would instead start failing Unmarshal below with no
// obvious connection to what changed.
func providerConfigObjectType(ctx context.Context) tftypes.Type {
	var resp provider.SchemaResponse
	(&CircleCiProvider{}).Schema(ctx, provider.SchemaRequest{}, &resp)

	return resp.Schema.Type().TerraformType(ctx)
}

// reservedFakeDomains are second-level domains RFC 2606 reserves for
// documentation; they are guaranteed never to resolve to a real host.
// reservedFakeTLDs are RFC 6761 special-use top-level domains with the same
// guarantee. This package's own tests rely on that guarantee: several
// (cloud_only_test.go, orb_fake_test.go, configure_test.go and others) point
// the provider at "https://circleci.example.com" specifically for a case that
// must fail before any network attempt is made -- a validation or
// deployment-mismatch rejection at plan time, never Apply. Measured directly:
// the first version of this instrument, which only checked for a loopback
// address, misclassified all seven of those tests as "real API" (they
// configure a non-loopback, non-empty host and nothing else distinguishes
// them) -- see the git history of this file for the run that caught it. A
// reserved domain is not loopback, but it is exactly as certainly not the real
// API.
var (
	reservedFakeDomains = []string{"example.com", "example.net", "example.org"}
	reservedFakeTLDs    = []string{"test", "example", "invalid", "localhost"}
)

// isKnownFakeHost reports whether rawHost -- a bare origin such as
// "https://circleci.com", an httptest fake's "http://127.0.0.1:54321", or a
// reserved placeholder like "https://circleci.example.com" -- is definitely
// not the real CircleCI API: a loopback address (this package's own httptest
// fakes always bind there) or an RFC 2606/6761 reserved domain (this
// package's plan-time-rejection tests use these, on purpose, so that a bug
// that let one reach the network would fail loudly on DNS resolution rather
// than quietly reaching circleci.com).
func isKnownFakeHost(rawHost string) bool {
	host := rawHost
	if u, err := url.Parse(rawHost); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}

	if host == "" {
		return true
	}

	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}

	lower := strings.ToLower(host)

	for _, domain := range reservedFakeDomains {
		if lower == domain || strings.HasSuffix(lower, "."+domain) {
			return true
		}
	}

	tld := lower
	if i := strings.LastIndexByte(lower, '.'); i >= 0 {
		tld = lower[i+1:]
	}

	return slices.Contains(reservedFakeTLDs, tld)
}

// fallbackHost reproduces the second and third steps of provider.go's own
// Configure fallback (CIRCLE_HOST, then circleci.DefaultHost), for exactly
// the case Configure itself falls back for: no "host" attribute set in the
// test's own Terraform config.
func fallbackHost() string {
	if envHost := os.Getenv("CIRCLE_HOST"); envHost != "" {
		if normalized, _ := circleci.NormalizeHost(envHost); normalized != "" {
			return normalized
		}
	}

	return circleci.DefaultHost
}

// effectiveConfiguredHost decodes the host Terraform actually configured the
// provider with from the raw ConfigureProvider request, following the same
// precedence provider.go's Configure uses: the config's own "host" attribute,
// else CIRCLE_HOST, else circleci.DefaultHost.
func effectiveConfiguredHost(ctx context.Context, req *tfprotov6.ConfigureProviderRequest) (string, error) {
	if req == nil || req.Config == nil {
		return "", fmt.Errorf("effectiveConfiguredHost: ConfigureProviderRequest has no Config")
	}

	val, err := req.Config.Unmarshal(providerConfigObjectType(ctx))
	if err != nil {
		return "", fmt.Errorf("effectiveConfiguredHost: unmarshal provider config: %w", err)
	}

	if !val.IsKnown() || val.IsNull() {
		return fallbackHost(), nil
	}

	attrs := map[string]tftypes.Value{}
	if err := val.As(&attrs); err != nil {
		return "", fmt.Errorf("effectiveConfiguredHost: provider config is not an object: %w", err)
	}

	if hostVal, ok := attrs["host"]; ok && hostVal.IsKnown() && !hostVal.IsNull() {
		var host string
		if err := hostVal.As(&host); err == nil && host != "" {
			if normalized, _ := circleci.NormalizeHost(host); normalized != "" {
				return normalized, nil
			}
		}
	}

	return fallbackHost(), nil
}

// networkCoverageState is the live record of, this run, which acceptance
// tests configured the provider against the real API and which against an
// in-process fake. Kept as its own type (rather than a bare package-level
// struct literal, the way vcsCoverage above is) so tests can exercise
// recordNetworkCoverage against a private instance instead of the shared
// global -- see TestNetworkCoverageProviderServerRecordsAndDelegates.
type networkCoverageState struct {
	mu   sync.Mutex
	real map[string]bool // TestAcc* names whose provider configured against a real host (see isKnownFakeHost)
	fake map[string]bool // TestAcc* names whose provider configured against a known fake (loopback or reserved domain)

	// unattributedReal/unattributedFake count a ConfigureProvider call this
	// instrument could classify real-vs-fake but could not name a test for
	// (currentAcceptanceTestName returned ok=false, or the name it found is
	// not a real acceptance test's -- see isRealAcceptanceTestName). Counted,
	// never dropped: see the package comment above on why this cannot
	// silently miss a test.
	unattributedReal int
	unattributedFake int

	// decodeErrors counts a ConfigureProviderRequest this instrument could not
	// decode at all (a nil Config, or one that fails to unmarshal against the
	// live schema). Surfaced rather than silently ignored, for the same reason.
	decodeErrors int
}

func newNetworkCoverageState() *networkCoverageState {
	return &networkCoverageState{real: map[string]bool{}, fake: map[string]bool{}}
}

// networkCoverage is the process-wide record for this `go test` invocation.
var networkCoverage = newNetworkCoverageState()

// recordNetworkCoverage classifies one ConfigureProvider call and records it
// into state. testName/hasName come from currentAcceptanceTestName, captured
// at factory time -- see networkCoverageProviderServer below.
func recordNetworkCoverage(
	state *networkCoverageState,
	ctx context.Context,
	req *tfprotov6.ConfigureProviderRequest,
	testName string,
	hasName bool,
) {
	host, err := effectiveConfiguredHost(ctx, req)
	if err != nil {
		state.mu.Lock()
		state.decodeErrors++
		state.mu.Unlock()

		return
	}

	isReal := !isKnownFakeHost(host)

	if !hasName || !isRealAcceptanceTestName(testName) {
		state.mu.Lock()
		defer state.mu.Unlock()

		if isReal {
			state.unattributedReal++
		} else {
			state.unattributedFake++
		}

		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	if isReal {
		state.real[testName] = true
	} else {
		state.fake[testName] = true
	}
}

// networkCoverageProviderServer wraps a real tfprotov6.ProviderServer and
// records, on ConfigureProvider, whether Terraform configured it against the
// real API or an in-process fake. Every other method is the embedded
// server's own, promoted unchanged -- this changes nothing about how any
// acceptance test actually runs.
type networkCoverageProviderServer struct {
	tfprotov6.ProviderServer

	state    *networkCoverageState
	testName string
	hasName  bool
}

func (s *networkCoverageProviderServer) ConfigureProvider(
	ctx context.Context, req *tfprotov6.ConfigureProviderRequest,
) (*tfprotov6.ConfigureProviderResponse, error) {
	recordNetworkCoverage(s.state, ctx, req, s.testName, s.hasName)

	return s.ProviderServer.ConfigureProvider(ctx, req)
}

// init wraps testAccProtoV6ProviderFactories["circleci"] (declared in
// acctest_test.go) exactly once, for the whole test binary. This is the only
// place this instrument attaches itself -- no test file, including the six
// RealAPI/Net-suffixed ones named in the package comment above, needs any
// change for this to see it.
func init() {
	original, ok := testAccProtoV6ProviderFactories["circleci"]
	if !ok {
		panic("vcs_gating_test.go: testAccProtoV6ProviderFactories has no \"circleci\" factory to " +
			"instrument for network coverage; see acctest_test.go")
	}

	testAccProtoV6ProviderFactories["circleci"] = func() (tfprotov6.ProviderServer, error) {
		server, err := original()
		if err != nil {
			return server, err
		}

		name, hasName := currentAcceptanceTestName()

		return &networkCoverageProviderServer{
			ProviderServer: server,
			state:          networkCoverage,
			testName:       name,
			hasName:        hasName,
		}, nil
	}
}

// TestCurrentAcceptanceTestNameFromStack pins the load-bearing claim behind
// networkCoverage's attribution: a runtime.Stack capture taken from a
// directly-called helper still names the top-level Test function that called
// it, fully qualified by this module's own import path.
func TestCurrentAcceptanceTestNameFromStack(t *testing.T) {
	name, ok := currentAcceptanceTestName()
	if !ok {
		t.Fatal("currentAcceptanceTestName() found no frame; want TestCurrentAcceptanceTestNameFromStack")
	}

	if name != "TestCurrentAcceptanceTestNameFromStack" {
		t.Errorf("currentAcceptanceTestName() = %q, want %q", name, "TestCurrentAcceptanceTestNameFromStack")
	}
}

// TestCurrentAcceptanceTestNameFromStackInSubtest pins the other half: the
// same lookup, called from inside a t.Run subtest closure -- the shape
// TestAccIOSSigningCertificatesDataSource and others actually use -- still
// recovers the outer test's name and not "TestXxx.func1", because Go names
// the closure "EnclosingFunc.funcN" and the regex stops at the "." separating
// them.
func TestCurrentAcceptanceTestNameFromStackInSubtest(t *testing.T) {
	t.Run("nested", func(t *testing.T) {
		name, ok := currentAcceptanceTestName()
		if !ok {
			t.Fatal("currentAcceptanceTestName() found no frame from within a t.Run subtest")
		}

		if name != "TestCurrentAcceptanceTestNameFromStackInSubtest" {
			t.Errorf("currentAcceptanceTestName() = %q, want the outer test's name %q, not the closure's own",
				name, "TestCurrentAcceptanceTestNameFromStackInSubtest")
		}
	})
}

// TestIsKnownFakeHost pins the real-vs-fake classification networkCoverage's
// whole distinction rests on -- including the reserved-domain cases
// (cloud_only_test.go's "https://circleci.example.com" and its siblings) a
// loopback-only check missed. That gap was not hypothetical: it is exactly
// what let TestAccCloudOnlyResourcesFailAtPlanNotApply and six similar tests
// be recorded as "real API" on the first version of this instrument, which
// only asked "is this loopback?".
func TestIsKnownFakeHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"https://circleci.com", false},
		{"circleci.com", false},
		{"https://example.circleci.com", false},
		{"https://127.0.0.1:54321", true},
		{"http://localhost:8080", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"", true},
		{"https://circleci.example.com", true},
		{"https://circleci.example.com:443", true},
		{"example.com", true},
		{"https://example.org", true},
		{"https://something.example.net", true},
		{"https://foo.test", true},
		{"https://circleci.example.com.evil.com", false}, // not a suffix match on the reserved domain
	}

	for _, c := range cases {
		if got := isKnownFakeHost(c.host); got != c.want {
			t.Errorf("isKnownFakeHost(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

// newProviderConfigDynamicValue builds a *tfprotov6.DynamicValue for the
// live provider schema, for effectiveConfiguredHost/recordNetworkCoverage
// tests below. host == "" encodes a null "host" attribute, the shape every
// real-API test in this package's config actually sends (no provider block
// at all).
func newProviderConfigDynamicValue(t *testing.T, host string) *tfprotov6.DynamicValue {
	t.Helper()

	ctx := context.Background()
	objType := providerConfigObjectType(ctx)

	hostVal := tftypes.NewValue(tftypes.String, nil)
	if host != "" {
		hostVal = tftypes.NewValue(tftypes.String, host)
	}

	val := tftypes.NewValue(objType, map[string]tftypes.Value{
		"host":        hostVal,
		"key":         tftypes.NewValue(tftypes.String, "fake-token"),
		"runner_host": tftypes.NewValue(tftypes.String, nil),
		"deployment":  tftypes.NewValue(tftypes.String, nil),
	})

	dv, err := tfprotov6.NewDynamicValue(objType, val)
	if err != nil {
		t.Fatalf("tfprotov6.NewDynamicValue: %v", err)
	}

	return &dv
}

// TestEffectiveConfiguredHostFallbackOrder pins the three-step precedence
// effectiveConfiguredHost mirrors from provider.go's own Configure: the
// config's own "host" attribute, then CIRCLE_HOST, then circleci.DefaultHost.
func TestEffectiveConfiguredHostFallbackOrder(t *testing.T) {
	ctx := context.Background()

	t.Run("an explicit host attribute wins", func(t *testing.T) {
		req := &tfprotov6.ConfigureProviderRequest{Config: newProviderConfigDynamicValue(t, "http://127.0.0.1:9999")}

		got, err := effectiveConfiguredHost(ctx, req)
		if err != nil {
			t.Fatalf("effectiveConfiguredHost: %v", err)
		}

		if got != "http://127.0.0.1:9999" {
			t.Errorf("effectiveConfiguredHost() = %q, want the explicit host attribute unchanged", got)
		}
	})

	t.Run("CIRCLE_HOST wins when the host attribute is null", func(t *testing.T) {
		t.Setenv("CIRCLE_HOST", "http://127.0.0.1:12345")

		req := &tfprotov6.ConfigureProviderRequest{Config: newProviderConfigDynamicValue(t, "")}

		got, err := effectiveConfiguredHost(ctx, req)
		if err != nil {
			t.Fatalf("effectiveConfiguredHost: %v", err)
		}

		if got != "http://127.0.0.1:12345" {
			t.Errorf("effectiveConfiguredHost() = %q, want CIRCLE_HOST's value", got)
		}
	})

	t.Run("circleci.DefaultHost is the last resort", func(t *testing.T) {
		req := &tfprotov6.ConfigureProviderRequest{Config: newProviderConfigDynamicValue(t, "")}

		got, err := effectiveConfiguredHost(ctx, req)
		if err != nil {
			t.Fatalf("effectiveConfiguredHost: %v", err)
		}

		if got != circleci.DefaultHost {
			t.Errorf("effectiveConfiguredHost() = %q, want circleci.DefaultHost (%q)", got, circleci.DefaultHost)
		}
	})

	t.Run("a nil Config is a decode error, not a guess", func(t *testing.T) {
		if _, err := effectiveConfiguredHost(ctx, &tfprotov6.ConfigureProviderRequest{}); err == nil {
			t.Error("effectiveConfiguredHost(nil Config) = nil error, want an error rather than silently defaulting")
		}
	})
}

// stubProviderServer is a minimal tfprotov6.ProviderServer double for
// TestNetworkCoverageProviderServerRecordsAndDelegates: only ConfigureProvider
// is implemented, and the embedded nil ProviderServer means calling any other
// method would panic -- appropriate, since networkCoverageProviderServer must
// never be the thing that calls one.
type stubProviderServer struct {
	tfprotov6.ProviderServer

	configureCalls int
}

func (s *stubProviderServer) ConfigureProvider(
	_ context.Context, _ *tfprotov6.ConfigureProviderRequest,
) (*tfprotov6.ConfigureProviderResponse, error) {
	s.configureCalls++

	return &tfprotov6.ConfigureProviderResponse{}, nil
}

// TestNetworkCoverageProviderServerRecordsAndDelegates is the end-to-end proof
// of the instrument networkCoverageProviderServer.ConfigureProvider adds: it
// always calls through to the real server exactly once (Terraform must still
// get configured), and it classifies real vs. fake and attributes by name
// correctly -- using a private networkCoverageState throughout, so this test
// never touches the process-wide networkCoverage a real acceptance test run
// also writes to.
func TestNetworkCoverageProviderServerRecordsAndDelegates(t *testing.T) {
	ctx := context.Background()

	t.Run("a real host is recorded by name and the real server still runs", func(t *testing.T) {
		state := newNetworkCoverageState()
		stub := &stubProviderServer{}
		wrapped := &networkCoverageProviderServer{
			ProviderServer: stub, state: state, testName: "TestAccProbeReal", hasName: true,
		}

		if _, err := wrapped.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{
			Config: newProviderConfigDynamicValue(t, ""), // null host -> falls back to circleci.DefaultHost
		}); err != nil {
			t.Fatalf("ConfigureProvider: %v", err)
		}

		if stub.configureCalls != 1 {
			t.Errorf("underlying ConfigureProvider called %d time(s), want exactly 1 -- the real delegate "+
				"must always run regardless of what this instrument observes", stub.configureCalls)
		}

		state.mu.Lock()
		defer state.mu.Unlock()

		if !state.real["TestAccProbeReal"] {
			t.Errorf("state.real = %v, want it to contain %q: a null host attribute falls back to "+
				"circleci.DefaultHost, which is not loopback", state.real, "TestAccProbeReal")
		}

		if len(state.fake) != 0 {
			t.Errorf("state.fake = %v, want empty", state.fake)
		}
	})

	t.Run("an explicit loopback host is recorded as fake, by name", func(t *testing.T) {
		state := newNetworkCoverageState()
		stub := &stubProviderServer{}
		wrapped := &networkCoverageProviderServer{
			ProviderServer: stub, state: state, testName: "TestAccProbeFake", hasName: true,
		}

		if _, err := wrapped.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{
			Config: newProviderConfigDynamicValue(t, "http://127.0.0.1:9999"),
		}); err != nil {
			t.Fatalf("ConfigureProvider: %v", err)
		}

		state.mu.Lock()
		defer state.mu.Unlock()

		if !state.fake["TestAccProbeFake"] {
			t.Errorf("state.fake = %v, want it to contain %q", state.fake, "TestAccProbeFake")
		}

		if len(state.real) != 0 {
			t.Errorf("state.real = %v, want empty", state.real)
		}
	})

	t.Run("a name that is not a real acceptance test's is counted but never named", func(t *testing.T) {
		state := newNetworkCoverageState()
		stub := &stubProviderServer{}
		wrapped := &networkCoverageProviderServer{
			ProviderServer: stub, state: state,
			testName: "TestNetworkCoverageProviderServerRecordsAndDelegates", hasName: true,
		}

		if _, err := wrapped.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{
			Config: newProviderConfigDynamicValue(t, ""),
		}); err != nil {
			t.Fatalf("ConfigureProvider: %v", err)
		}

		state.mu.Lock()
		defer state.mu.Unlock()

		if len(state.real) != 0 {
			t.Errorf("state.real = %v, want empty: this package's own tests must never be attributed as "+
				"acceptance coverage (see isRealAcceptanceTestName)", state.real)
		}

		if state.unattributedReal != 1 {
			t.Errorf("state.unattributedReal = %d, want 1: a real-host configuration this instrument could "+
				"not attribute must still be counted, never silently dropped", state.unattributedReal)
		}
	})

	t.Run("no name at all is counted but never named", func(t *testing.T) {
		state := newNetworkCoverageState()
		stub := &stubProviderServer{}
		wrapped := &networkCoverageProviderServer{ProviderServer: stub, state: state, hasName: false}

		if _, err := wrapped.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{
			Config: newProviderConfigDynamicValue(t, "http://127.0.0.1:1"),
		}); err != nil {
			t.Fatalf("ConfigureProvider: %v", err)
		}

		state.mu.Lock()
		defer state.mu.Unlock()

		if state.unattributedFake != 1 {
			t.Errorf("state.unattributedFake = %d, want 1", state.unattributedFake)
		}
	})

	t.Run("an undecodable request is counted as a decode error, not dropped", func(t *testing.T) {
		state := newNetworkCoverageState()
		stub := &stubProviderServer{}
		wrapped := &networkCoverageProviderServer{
			ProviderServer: stub, state: state, testName: "TestAccProbeUndecodable", hasName: true,
		}

		if _, err := wrapped.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{}); err != nil {
			t.Fatalf("ConfigureProvider: %v", err)
		}

		state.mu.Lock()
		defer state.mu.Unlock()

		if state.decodeErrors != 1 {
			t.Errorf("state.decodeErrors = %d, want 1", state.decodeErrors)
		}

		if len(state.real) != 0 || len(state.fake) != 0 {
			t.Errorf("state.real = %v, state.fake = %v, want both empty for an undecodable request",
				state.real, state.fake)
		}
	})
}

// TestMergeExercisedNamesDeduplicatesAndAnnotates pins the union logic
// printVCSCoverageSummary uses to combine the two independent coverage
// mechanisms into one "Exercised by this run" list without double-counting a
// test both mechanisms saw.
func TestMergeExercisedNamesDeduplicatesAndAnnotates(t *testing.T) {
	gated := []string{"TestAccGitHubAppInstallationDataSourceNet_Installed (github_app)"}
	networkReal := map[string]bool{
		"TestAccGitHubAppInstallationDataSourceNet_Installed": true, // also seen by networkCoverage: must not duplicate
		"TestAccIOSSigningCertificateResource_RealAPI":        true, // networkCoverage-only: must gain the annotation
	}

	got := mergeExercisedNames(gated, networkReal)

	want := []string{
		"TestAccGitHubAppInstallationDataSourceNet_Installed (github_app)",
		"TestAccIOSSigningCertificateResource_RealAPI" + networkOnlyAnnotation,
	}

	if len(got) != len(want) {
		t.Fatalf("mergeExercisedNames() = %v (len %d), want len %d: a test seen by both mechanisms must "+
			"appear once, not twice", got, len(got), len(want))
	}

	for i, entry := range want {
		if got[i] != entry {
			t.Errorf("mergeExercisedNames()[%d] = %q, want %q", i, got[i], entry)
		}
	}
}

// testAccRunnerSelectors names the terraform-plugin-testing entry points that
// call the shared, wrapped factory (see init() above): resource.Test,
// resource.UnitTest and resource.ParallelTest. UnitTest and ParallelTest are
// both thin wrappers around Test itself (same runProviderCommand path), so
// all three are equally visible to networkCoverage.
var testAccRunnerSelectors = map[string]bool{"Test": true, "UnitTest": true, "ParallelTest": true}

// testRunnerImportPath is terraform-plugin-testing's own import path, quoted
// exactly as it appears in an *ast.ImportSpec's Path.Value.
const testRunnerImportPath = `"github.com/hashicorp/terraform-plugin-testing/helper/resource"`

// testRunnerLocalName resolves the identifier one file in this package binds
// testRunnerImportPath to. Most files import it as "resource" (the package's
// own name), but several -- cloud_only_test.go, organization_contacts_resource_test.go
// and others -- alias it to "sdkresource" specifically to avoid colliding
// with this package's own "resource" import
// (terraform-plugin-framework/resource). Hardcoding "resource" here would
// have missed every one of those, which is exactly the false-positive this
// function's own test (TestTestRunnerLocalNameResolvesTheActualAlias) pins.
func testRunnerLocalName(file *ast.File) (string, bool) {
	for _, imp := range file.Imports {
		if imp.Path.Value != testRunnerImportPath {
			continue
		}

		if imp.Name != nil {
			return imp.Name.Name, true
		}

		return "resource", true
	}

	return "", false
}

// funcBodyCallsTestRunner reports whether body -- a top-level test function's
// block, including every t.Run closure nested inside it -- contains a call to
// one of testAccRunnerSelectors through runnerLocalName, the identifier this
// function's own file binds testRunnerImportPath to. ast.Inspect walks into
// nested FuncLits on its own, so a call inside a
// t.Run("...", func(t *testing.T){...}) closure is found exactly the same as
// one at the top level.
func funcBodyCallsTestRunner(body *ast.BlockStmt, runnerLocalName string) bool {
	found := false

	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == runnerLocalName && testAccRunnerSelectors[sel.Sel.Name] {
			found = true
		}

		return true
	})

	return found
}

// TestTestRunnerLocalNameResolvesTheActualAlias pins the specific gap a
// hardcoded "resource" identifier check would have: several files in this
// package alias terraform-plugin-testing's helper/resource import to
// "sdkresource" to avoid colliding with terraform-plugin-framework's own
// "resource" package, and the alias -- not the import path's own package
// name -- is what a CallExpr's selector actually uses.
func TestTestRunnerLocalNameResolvesTheActualAlias(t *testing.T) {
	src := func(importSpec string) string {
		return "package provider\n\nimport (\n" + importSpec + "\n)\n\nfunc x() {}\n"
	}

	cases := []struct {
		name       string
		importSpec string
		wantName   string
		wantOK     bool
	}{
		{
			name:       "unaliased import resolves to the package's own name",
			importSpec: `"github.com/hashicorp/terraform-plugin-testing/helper/resource"`,
			wantName:   "resource",
			wantOK:     true,
		},
		{
			name:       "an alias resolves to the alias, not the package name",
			importSpec: `sdkresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"`,
			wantName:   "sdkresource",
			wantOK:     true,
		},
		{
			name:       "a file that never imports it resolves to nothing",
			importSpec: `"fmt"`,
			wantName:   "",
			wantOK:     false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fset := token.NewFileSet()

			file, err := parser.ParseFile(fset, "probe", src(c.importSpec), parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parsing probe source: %v", err)
			}

			name, ok := testRunnerLocalName(file)
			if name != c.wantName || ok != c.wantOK {
				t.Errorf("testRunnerLocalName() = (%q, %v), want (%q, %v)", name, ok, c.wantName, c.wantOK)
			}
		})
	}
}

// TestEveryTestAccFunctionUsesAnInstrumentedRunner is the static half of this
// file's "cannot silently miss a test" claim. networkCoverage (above) sees a
// test the moment it calls resource.Test/UnitTest/ParallelTest, with nothing
// for the test author to remember -- but that guarantee is only as good as
// "every TestAcc* function actually calls one of those." This test checks
// that claim directly, by parsing every *_test.go file in this package and
// failing on any top-level TestAcc* function whose body (or a t.Run closure
// inside it) never calls one of them.
//
// Verified empty against the actual source tree when this test was added:
// every func TestAcc* in this package already calls resource.Test, UnitTest
// or ParallelTest. A future test that reaches the API some other way --
// calling circleci.New directly, say, the way testAccDeleteDefinitionOutOfBand
// does from inside a test that also runs resource.Test -- would fail this
// test if it were the *only* thing that function did, rather than silently
// going uncounted the way the RealAPI/Net tests did before this file's
// networkCoverage existed.
//
// knownDirectAPITests is the narrow, explicit exception this claim already
// has one of, found by running this very test while writing it:
// TestAccProjectSettingsDefaults (project_settings_data_source_test.go) talks
// to a *circleci.Client it builds itself (testAccProjectSettingsClient),
// never touching the Terraform provider or resource.Test/UnitTest/
// ParallelTest at all, so networkCoverage cannot see it today no matter what
// it does. That test is outside this file's remit to change, so this is
// recorded here, honestly, as a real and currently open gap rather than
// papered over by weakening funcBodyCallsTestRunner to stop asking the
// question. Closing it means giving that test (or testAccProjectSettingsClient
// itself) an explicit way to tell networkCoverage what it did; nothing here
// invents one on that file's behalf. Same idea as citableForeignGoFiles in
// no_internal_references_test.go: a short, named, commented list is how this
// codebase already marks "yes, we know, and here is exactly why".
var knownDirectAPITests = map[string]bool{
	"TestAccProjectSettingsDefaults": true,
}

func TestEveryTestAccFunctionUsesAnInstrumentedRunner(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed; cannot locate this package's own directory to scan")
	}

	dir := filepath.Dir(thisFile)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	fset := token.NewFileSet()

	var offenders []string

	seenKnownException := make(map[string]bool, len(knownDirectAPITests))

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}

		path := filepath.Join(dir, entry.Name())

		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}

		runnerLocalName, importsRunner := testRunnerLocalName(file)

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Body == nil {
				continue
			}

			if !strings.HasPrefix(fn.Name.Name, "TestAcc") {
				continue
			}

			if knownDirectAPITests[fn.Name.Name] {
				seenKnownException[fn.Name.Name] = true

				continue
			}

			if !importsRunner || !funcBodyCallsTestRunner(fn.Body, runnerLocalName) {
				offenders = append(offenders,
					fmt.Sprintf("%s (%s)", fn.Name.Name, entry.Name()))
			}
		}
	}

	sort.Strings(offenders)

	for _, offender := range offenders {
		t.Errorf("%s never calls resource.Test/UnitTest/ParallelTest: networkCoverage's wrapped "+
			"provider factory (see init() above) would never see this test run, so it would be "+
			"invisible to the real-API coverage count no matter what it actually does", offender)
	}

	// A stale entry here -- a name that has been renamed, removed, or fixed to
	// call resource.Test after all -- would silently stop meaning anything,
	// which is exactly the kind of drift a short exception list is supposed to
	// be immune to.
	for name := range knownDirectAPITests {
		if !seenKnownException[name] {
			t.Errorf("knownDirectAPITests names %q, but no such TestAcc* function was found in this "+
				"package; the entry is stale (renamed, removed, or no longer needed) and should be "+
				"removed", name)
		}
	}
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

// networkOnlyAnnotation marks a name in "Exercised by this run" that
// networkCoverage discovered on its own -- a test that never called
// testRequireVCSType or testRequireStandaloneOrg, so recordVCSCoverageRan
// never named it. This exact text is part of this report's contract with
// .circleci/scripts/summarize-acceptance-run.sh, which greps for it to report
// how many of "exercised" came from network observation alone rather than a
// VCS/org-class gate; keep the two in sync if this ever changes.
const networkOnlyAnnotation = " (real API, not VCS/org-class-gated)"

// mergeExercisedNames unions gated (the "name (detail)" entries
// recordVCSCoverageRan already produced, from testRequireVCSType/
// testRequireStandaloneOrg) with networkReal (bare test names networkCoverage
// observed configuring the provider against a real host), keyed by test name
// so a test recorded by both mechanisms -- checkout_key_net_test.go's and
// github_app_net_test.go's tests call testRequireVCSType AND get observed by
// networkCoverage -- is not double-counted. A name present only in
// networkReal is annotated with networkOnlyAnnotation, so both a human reader
// and the summarize script can see which mechanism found which entry.
func mergeExercisedNames(gated []string, networkReal map[string]bool) []string {
	entries := make(map[string]string, len(gated)+len(networkReal))

	for _, entry := range gated {
		name := entry
		if i := strings.IndexByte(entry, ' '); i >= 0 {
			name = entry[:i]
		}
		entries[name] = entry
	}

	for name := range networkReal {
		if _, exists := entries[name]; !exists {
			entries[name] = name + networkOnlyAnnotation
		}
	}

	merged := make([]string, 0, len(entries))
	for _, entry := range entries {
		merged = append(merged, entry)
	}
	sort.Strings(merged)

	return merged
}

// printVCSCoverageSummary is the whole of "reporting" here: one block of
// stdout, no separate command, no framework. TESTING.md's "What a run covers"
// section is the human-readable statement of the same claim.
func printVCSCoverageSummary() {
	vcsCoverage.mu.Lock()
	gated := append([]string(nil), vcsCoverage.ran...)
	skipped := append([]string(nil), vcsCoverage.skipped...)
	vcsCoverage.mu.Unlock()

	networkCoverage.mu.Lock()
	networkReal := make(map[string]bool, len(networkCoverage.real))
	for name := range networkCoverage.real {
		networkReal[name] = true
	}
	fakeCount := len(networkCoverage.fake)
	unattributedReal := networkCoverage.unattributedReal
	unattributedFake := networkCoverage.unattributedFake
	decodeErrors := networkCoverage.decodeErrors
	networkCoverage.mu.Unlock()

	exercised := mergeExercisedNames(gated, networkReal)

	if len(exercised) == 0 && len(skipped) == 0 && unattributedReal == 0 {
		// Nothing to report about real-API coverage: no VCS-gated test asked,
		// and no test in this run -- gated or not -- was ever observed
		// configuring the provider against anything but an in-process fake.
		// Staying silent here matters — this is what keeps `task test:fast`
		// and `task test` quiet on a fresh checkout, where every acceptance
		// test that runs at all is fake-backed (fakeCount deliberately does
		// not affect this decision; see below).
		//
		// The acceptance jobs in .circleci/config.yml rely on that silence
		// having exactly one meaning: they fail when the block is absent,
		// because for a job that configures an integration its absence means
		// no test ever proved real-API coverage at all. That only holds
		// because the guard's own mutation tests, and this instrument's own
		// unit tests, can never land in either record — see
		// isRealAcceptanceTestName and TestNetworkCoverageProviderServerRecordsAndDelegates.
		return
	}

	sort.Strings(skipped)

	fmt.Println()
	fmt.Println("=== VCS integration coverage (CIRCLECI_TEST_VCS_TYPE=" + os.Getenv("CIRCLECI_TEST_VCS_TYPE") + ") ===")
	fmt.Printf("Exercised by this run (%d):\n", len(exercised))
	for _, name := range exercised {
		fmt.Println("  " + name)
	}
	if unattributedReal > 0 {
		fmt.Printf("  + %d more real-API provider configuration(s) observed but not attributable to one test name\n",
			unattributedReal)
	}
	fmt.Printf("Skipped, configured fixture is a different integration (%d):\n", len(skipped))
	for _, name := range skipped {
		fmt.Println("  " + name)
	}
	fmt.Printf("Configured against an in-process fake this run (%d distinct test(s)): informational only, not a failure\n",
		fakeCount)
	if unattributedFake > 0 || decodeErrors > 0 {
		fmt.Printf("  (+ %d unattributed fake configuration(s), %d that could not be decoded)\n",
			unattributedFake, decodeErrors)
	}
}
