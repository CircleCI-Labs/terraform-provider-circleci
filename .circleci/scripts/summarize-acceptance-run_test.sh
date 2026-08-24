#!/usr/bin/env bash
# Copyright (c) CircleCI
# SPDX-License-Identifier: MPL-2.0
#
# Fixture-driven tests for summarize-acceptance-run.sh, runnable anywhere
# (`bash .circleci/scripts/summarize-acceptance-run_test.sh`) — this is the
# part of the CI shell logic the discipline in this change asked for
# something testable for, since CircleCI itself cannot be run locally.
#
# Every fixture XML/log pair here is built by this script, from literal skip
# messages copied out of the Go source (acctest_test.go, provider_test.go,
# vcs_gating_test.go, project_resource_test.go), not by fabricating numbers.
# The "hybrid" fixture's total/skipped/cause counts (403/36/29/4/0) are the
# ones verbatim from the incident report; the extra 3 causes it left
# unaccounted for are reconstructed from the two gating helpers' own skip
# messages, which is the actual vocabulary gap this file's rewrite closes.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script="$here/summarize-acceptance-run.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail=0

assert_contains() {
  local haystack="$1" needle="$2" what="$3"
  if [[ "$haystack" != *"$needle"* ]]; then
    echo "FAIL: $what: expected to find:"
    echo "  $needle"
    echo "--- actual output ---"
    echo "$haystack"
    echo "---"
    fail=1
  fi
}

assert_eq() {
  local actual="$1" expected="$2" what="$3"
  if [ "$actual" != "$expected" ]; then
    echo "FAIL: $what: got '$actual', want '$expected'"
    fail=1
  fi
}

# --- Fixture 1: acceptance-gh-hybrid, reconstructed from the incident report ---
#
# 403 TestAcc* testcases, 36 skipped: 29 fixture-unset, 4 placeholder, 2
# gated by testRequireVCSType (github_hybrid is not in any test's supported
# list), 1 gated by testRequireStandaloneOrg (gh-oauth-cci-2 is classic). 0
# exercised, matching "no VCS- or org-class-gated test in this package
# supports github_hybrid today" — see TESTING.md.
gen_hybrid() {
  local xml="$1" log="$2"
  {
    echo '<?xml version="1.0" encoding="UTF-8"?>'
    echo '<testsuites tests="403" failures="0" errors="0" time="612.0">'
    echo '<testsuite tests="403" failures="0" skipped="36" time="612.0" name="internal/provider">'
    for i in $(seq 1 367); do
      echo "<testcase classname=\"internal/provider\" name=\"TestAccMock${i}\" time=\"0.010000\"></testcase>"
    done
    for i in $(seq 1 29); do
      local n="TestAccFixtureUnset${i}"
      echo "<testcase classname=\"internal/provider\" name=\"${n}\" time=\"0.000000\">"
      echo "<skipped message=\"=== RUN   ${n}&#xA;    acctest_test.go:72: CIRCLECI_TEST_GH_HYBRID_SOME_FIXTURE_${i} must be set for this acceptance test (some fixture); see the Development section of README.md.&#xA;--- SKIP: ${n} (0.00s)&#xA;\"></skipped>"
      echo "</testcase>"
    done
    for i in $(seq 1 4); do
      local n="TestAccPlaceholder${i}"
      echo "<testcase classname=\"internal/provider\" name=\"${n}\" time=\"0.000000\">"
      echo "<skipped message=\"=== RUN   ${n}&#xA;    acctest_test.go:74: CIRCLECI_TEST_GH_HYBRID_PLACEHOLDER_${i} is set to a placeholder value (&quot;REPLACE_ME&quot;), which counts as unset; it must be set to a real value for this acceptance test (some fixture); see the Development section of README.md.&#xA;--- SKIP: ${n} (0.00s)&#xA;\"></skipped>"
      echo "</testcase>"
    done
    for n in TestAccPipelineDefinitionResource TestAccTriggerResourceWebhook; do
      echo "<testcase classname=\"internal/provider\" name=\"${n}\" time=\"0.000000\">"
      echo "<skipped message=\"=== RUN   ${n}&#xA;    vcs_gating_test.go:67: ${n} only runs against github_app; CIRCLECI_TEST_VCS_TYPE=github_hybrid does not support this feature (see the compatibility matrix in README.md)&#xA;--- SKIP: ${n} (0.00s)&#xA;\"></skipped>"
      echo "</testcase>"
    done
    local n="TestAccCircleCiProjectResource"
    echo "<testcase classname=\"internal/provider\" name=\"${n}\" time=\"0.000000\">"
    echo "<skipped message=\"=== RUN   ${n}&#xA;    project_resource_test.go:96: ${n} creates a project, which only a standalone (&quot;circleci/…&quot;) organization supports; the configured organization gh/gh-oauth-cci-2 is classic and VCS-backed, where this route can only adopt a repository that already exists.&#xA;--- SKIP: ${n} (0.00s)&#xA;\"></skipped>"
    echo "</testcase>"
    echo '</testsuite>'
    echo '</testsuites>'
  } > "$xml"

  cat > "$log" <<'LOG'
=== VCS integration coverage (CIRCLECI_TEST_VCS_TYPE=github_hybrid) ===
Exercised by this run (0):
Skipped, configured fixture is a different integration (3):
  TestAccCircleCiProjectResource (needs a standalone organization, got the classic org gh/gh-oauth-cci-2)
  TestAccPipelineDefinitionResource (needs github_app, got github_hybrid)
  TestAccTriggerResourceWebhook (needs github_app/github_oauth/github_server, got github_hybrid)
ok  	terraform-provider-circleci/internal/provider	612.000s	coverage: 41.2% of statements
LOG
}

test_hybrid_reconciles_and_fails_on_zero_exercised() {
  local xml="$tmp/hybrid.xml" log="$tmp/hybrid.log" out="$tmp/hybrid.out"
  gen_hybrid "$xml" "$log"

  local rc=0
  out_text="$(CIRCLECI_TEST_VCS_TYPE=github_hybrid bash "$script" "$log" "$xml" "$out" 2>&1)" || rc=$?

  assert_eq "$rc" "1" "hybrid: exit code"
  assert_contains "$out_text" "REAL-API TESTS EXERCISED THIS RUN: 0" "hybrid: headline"
  assert_contains "$out_text" "ran (mock-backed or real; NOT a proxy for real-API coverage): 367" "hybrid: ran count relabeled"
  assert_contains "$out_text" "skipped:                                                      36" "hybrid: skipped count"
  assert_contains "$out_text" "(causes total, must equal skipped above): 36" "hybrid: causes reconcile to 36, not 33"
  assert_contains "$out_text" "TestAccFixtureUnset29: CIRCLECI_TEST_GH_HYBRID_SOME_FIXTURE_29 must be set" "hybrid: every-skip list has real entries"
  assert_contains "$out_text" "TestAccPipelineDefinitionResource: TestAccPipelineDefinitionResource only runs against github_app" "hybrid: VCS-type-gated skip is listed with its reason"
  assert_contains "$out_text" "TestAccCircleCiProjectResource: TestAccCircleCiProjectResource creates a project, which only a standalone" "hybrid: org-class-gated skip is listed with its reason"
  if [[ "$out_text" == *"(none)"* ]]; then
    echo "FAIL: hybrid: 'Every skip, with its reason' printed (none) despite 36 skips"
    fail=1
  fi
  assert_contains "$out_text" "FAIL: github_hybrid exercised zero real-API tests" "hybrid: job2 guard fires and names the integration"
}

# --- Fixture 2: acceptance-gh-app, a healthy run — must NOT fail ---
gen_app() {
  local xml="$1" log="$2"
  cat > "$log" <<'LOG'
=== VCS integration coverage (CIRCLECI_TEST_VCS_TYPE=github_app) ===
Exercised by this run (7):
  TestAccCircleCiProjectResource (standalone org circleci/Va2k7FVcHE7EyFDbRioifr, github_app)
  TestAccPipelineDefinitionResource (github_app)
  TestAccPipelineResource (github_app)
Skipped, configured fixture is a different integration (1):
  TestAccPipelineDefinitionResourceGithubServer (needs github_server, got github_app)
ok  	terraform-provider-circleci/internal/provider	540.000s
LOG
  {
    echo '<?xml version="1.0" encoding="UTF-8"?>'
    echo '<testsuites tests="5" failures="0" errors="0" time="1.0">'
    echo '<testsuite tests="5" failures="0" skipped="2" time="1.0" name="internal/provider">'
    echo '<testcase classname="internal/provider" name="TestAccPassing1" time="0.010000"></testcase>'
    echo '<testcase classname="internal/provider" name="TestAccPassing2" time="0.010000"></testcase>'
    echo '<testcase classname="internal/provider" name="TestAccPassing3" time="0.010000"></testcase>'
    echo '<testcase classname="internal/provider" name="TestAccWeird" time="0.000000">'
    echo '<skipped message="=== RUN   TestAccWeird&#xA;    vcs_gating_test.go:1: a reason this script has never seen before&#xA;--- SKIP: TestAccWeird (0.00s)&#xA;"></skipped>'
    echo '</testcase>'
    echo '<testcase classname="internal/provider" name="TestAccPipelineDefinitionResourceGithubServer" time="0.000000">'
    echo '<skipped message="=== RUN   TestAccPipelineDefinitionResourceGithubServer&#xA;    vcs_gating_test.go:67: TestAccPipelineDefinitionResourceGithubServer only runs against github_server; CIRCLECI_TEST_VCS_TYPE=github_app does not support this feature&#xA;--- SKIP: TestAccPipelineDefinitionResourceGithubServer (0.00s)&#xA;"></skipped>'
    echo '</testcase>'
    echo '</testsuite>'
    echo '</testsuites>'
  } > "$xml"
}

test_app_passes_and_other_bucket_catches_unknown_message() {
  local xml="$tmp/app.xml" log="$tmp/app.log" out="$tmp/app.out"
  gen_app "$xml" "$log"

  local rc=0
  out_text="$(CIRCLECI_TEST_VCS_TYPE=github_app bash "$script" "$log" "$xml" "$out" 2>&1)" || rc=$?

  assert_eq "$rc" "0" "app: exit code (a healthy job must not fail)"
  assert_contains "$out_text" "REAL-API TESTS EXERCISED THIS RUN: 7" "app: headline"
  assert_contains "$out_text" "other:                                   1" "app: unrecognized message lands in other, not silently dropped"
  assert_contains "$out_text" "(causes total, must equal skipped above): 2" "app: causes still reconcile with an unknown message present"
  assert_contains "$out_text" "TestAccWeird: a reason this script has never seen before" "app: even the unrecognized skip gets listed with its reason"
}

# --- Fixture 3: no token reached this job at all ---
test_no_token_fails_before_the_zero_exercised_check() {
  local xml="$tmp/notoken.xml" log="$tmp/notoken.log" out="$tmp/notoken.out"
  cat > "$log" <<'LOG'
=== VCS integration coverage (CIRCLECI_TEST_VCS_TYPE=github_app) ===
Exercised by this run (0):
Skipped, configured fixture is a different integration (0):
ok  	terraform-provider-circleci/internal/provider	1.0s
LOG
  {
    echo '<?xml version="1.0" encoding="UTF-8"?>'
    echo '<testsuites tests="1" failures="0" errors="0" time="1.0">'
    echo '<testsuite tests="1" failures="0" skipped="1" time="1.0" name="internal/provider">'
    echo '<testcase classname="internal/provider" name="TestAccFoo" time="0.000000">'
    echo '<skipped message="=== RUN   TestAccFoo&#xA;    provider_test.go:53: no API token for acceptance tests: set CIRCLECI_TEST_GH_APP_TOKEN&#xA;--- SKIP: TestAccFoo (0.00s)&#xA;"></skipped>'
    echo '</testcase>'
    echo '</testsuite>'
    echo '</testsuites>'
  } > "$xml"

  local rc=0
  out_text="$(CIRCLECI_TEST_VCS_TYPE=github_app bash "$script" "$log" "$xml" "$out" 2>&1)" || rc=$?

  assert_eq "$rc" "1" "no-token: exit code"
  assert_contains "$out_text" "FAIL: tests skipped for a missing API token" "no-token: the pre-existing token guard still fires"
}

# --- Fixture 4: no log at all (test step never got far enough) ---
test_missing_log_exits_zero_without_a_report() {
  local out="$tmp/missing.out"
  local rc=0
  out_text="$(bash "$script" "$tmp/does-not-exist.log" "$tmp/does-not-exist.xml" "$out" 2>&1)" || rc=$?
  assert_eq "$rc" "0" "missing log: exit code (nothing to summarize, but not this step's failure to report)"
  assert_contains "$out_text" "so the test step did not get far enough" "missing log: explains itself"
}

# --- Fixture 5: github_hybrid, but with networkCoverage's fix applied ---
#
# Same integration as gen_hybrid above (github_hybrid, zero testRequireVCSType/
# testRequireStandaloneOrg coverage), but this is the block TestMain actually
# prints after vcs_gating_test.go's networkCoverage instrument was added: the
# iOS signing, notification, organization-contacts and URL-orb-allow-list
# families need no VCS/org-class gate at all, and gh-oauth-cci-2 (the
# organization acceptance-gh-hybrid points at) has everything they need
# (testAccPreCheck's token, testOrgID's fixture). This is exactly the run the
# acceptance-gh-hybrid job comment in .circleci/config.yml now describes: not
# "zero real-API coverage" any more, without any test being newly gated to
# github_hybrid.
gen_hybrid_with_network_observed_tests() {
  local xml="$1" log="$2"
  cat > "$log" <<'LOG'
=== VCS integration coverage (CIRCLECI_TEST_VCS_TYPE=github_hybrid) ===
Exercised by this run (2):
  TestAccIOSSigningCertificateResource_RealAPI (real API, not VCS/org-class-gated)
  TestAccOrganizationContactsNet_Lifecycle (real API, not VCS/org-class-gated)
Skipped, configured fixture is a different integration (2):
  TestAccPipelineDefinitionResource (needs github_app, got github_hybrid)
  TestAccTriggerResourceWebhook (needs github_app/github_oauth/github_server, got github_hybrid)
Configured against an in-process fake this run (204 distinct test(s)): informational only, not a failure
ok  	terraform-provider-circleci/internal/provider	588.000s
LOG
  {
    echo '<?xml version="1.0" encoding="UTF-8"?>'
    echo '<testsuites tests="3" failures="0" errors="0" time="1.0">'
    echo '<testsuite tests="3" failures="0" skipped="0" time="1.0" name="internal/provider">'
    echo '<testcase classname="internal/provider" name="TestAccIOSSigningCertificateResource_RealAPI" time="12.010000"></testcase>'
    echo '<testcase classname="internal/provider" name="TestAccOrganizationContactsNet_Lifecycle" time="4.010000"></testcase>'
    echo '<testcase classname="internal/provider" name="TestAccPassing1" time="0.010000"></testcase>'
    echo '</testsuite>'
    echo '</testsuites>'
  } > "$xml"
}

test_hybrid_with_network_observed_tests_passes_and_counts_them_separately() {
  local xml="$tmp/hybrid2.xml" log="$tmp/hybrid2.log" out="$tmp/hybrid2.out"
  gen_hybrid_with_network_observed_tests "$xml" "$log"

  local rc=0
  out_text="$(CIRCLECI_TEST_VCS_TYPE=github_hybrid bash "$script" "$log" "$xml" "$out" 2>&1)" || rc=$?

  assert_eq "$rc" "0" "hybrid+network: exit code (network-observed coverage is real coverage, this job must not fail)"
  assert_contains "$out_text" "REAL-API TESTS EXERCISED THIS RUN: 2" "hybrid+network: headline reflects the union, not zero"
  assert_contains "$out_text" \
    "of which, via network observation only (no VCS/org-class gate): 2" \
    "hybrid+network: both exercised entries are attributed to network observation, not a VCS/org-class gate"
  assert_contains "$out_text" "TestAccIOSSigningCertificateResource_RealAPI (real API, not VCS/org-class-gated)" \
    "hybrid+network: the annotated entry survives the pass-through verbatim"
}

test_hybrid_reconciles_and_fails_on_zero_exercised
test_app_passes_and_other_bucket_catches_unknown_message
test_no_token_fails_before_the_zero_exercised_check
test_missing_log_exits_zero_without_a_report
test_hybrid_with_network_observed_tests_passes_and_counts_them_separately

if [ "$fail" -ne 0 ]; then
  echo "FAILED"
  exit 1
fi
echo "ok"
