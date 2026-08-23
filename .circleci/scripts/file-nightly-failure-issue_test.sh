#!/usr/bin/env bash
# Copyright (c) CircleCI
# SPDX-License-Identifier: MPL-2.0
#
# Fixture-driven tests for file-nightly-failure-issue.sh, runnable anywhere
# (`bash .circleci/scripts/file-nightly-failure-issue_test.sh`) with no
# CircleCI, no GitHub token and no network call: build_issue_content is
# exercised directly against fixture JUnit XML, and main's two `gh`-calling
# outcomes (open a new issue / comment on an existing one) are exercised by
# overriding gh_find_open_issue/gh_create_issue/gh_comment_issue after
# sourcing the script -- ordinary shell functions, reassignable like any
# other, so no real `gh` process ever runs.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

fail=0

assert_contains() {
  local haystack="$1" needle="$2" what="$3"
  if [[ "$haystack" != *"$needle"* ]]; then
    echo "FAIL: $what: expected to find:"
    echo "  $needle"
    echo "--- actual ---"
    echo "$haystack"
    echo "---"
    fail=1
  fi
}

assert_not_contains() {
  local haystack="$1" needle="$2" what="$3"
  if [[ "$haystack" == *"$needle"* ]]; then
    echo "FAIL: $what: did not expect to find:"
    echo "  $needle"
    echo "--- actual ---"
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

# Source, not exec: build_issue_content and the gh_* wrappers below are
# called directly, and gh_find_open_issue/gh_create_issue/gh_comment_issue are
# overridden per test, after sourcing, by simply redefining them -- ordinary
# shell functions are reassignable like any other name.
# shellcheck source=SCRIPTDIR/file-nightly-failure-issue.sh
source "$here/file-nightly-failure-issue.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# --- fixture JUnit XML, in the shape summarize-acceptance-run.sh's own
# extract_testcases already relies on: a <failure> child marks a failing
# testcase, a passing one has neither <failure> nor <skipped>. ---
write_xml() {
  local xml="$1"
  shift
  {
    echo '<?xml version="1.0" encoding="UTF-8"?>'
    echo '<testsuites><testsuite name="internal/provider">'
    for name in "$@"; do
      case "$name" in
      FAIL:*)
        name="${name#FAIL:}"
        echo "<testcase classname=\"internal/provider\" name=\"${name}\" time=\"1.0\">"
        echo '<failure message="assertion failed">boom</failure>'
        echo '</testcase>'
        ;;
      *)
        echo "<testcase classname=\"internal/provider\" name=\"${name}\" time=\"1.0\"></testcase>"
        ;;
      esac
    done
    echo '</testsuite></testsuites>'
  } >"$xml"
}

test_build_issue_content_names_the_failing_tests() {
  local xml="$tmp/one.xml" out="$tmp/one-content"
  write_xml "$xml" "TestAccPassing1" "FAIL:TestAccGithubProjectResource" "FAIL:TestAccPipelineResource"

  build_issue_content "acceptance-gh-app" "$xml" "https://circleci.example/build/1" "$out"

  local title body
  title="$(cat "$out/title")"
  body="$(cat "$out/body")"

  assert_contains "$title" "acceptance-gh-app" "content: title names the job"
  assert_contains "$body" "TestAccGithubProjectResource" "content: body lists the first failing test"
  assert_contains "$body" "TestAccPipelineResource" "content: body lists the second failing test"
  assert_not_contains "$body" "TestAccPassing1" "content: body does not list the passing test as a failure"
  assert_contains "$body" "https://circleci.example/build/1" "content: body links the build"
  assert_contains "$body" "<!-- nightly-acceptance-failure-acceptance-gh-app-" "content: body carries the dedup marker"
}

# test_search_token_has_no_github_search_syntax_characters pins the fix for a
# real defect: an earlier version of this script built its dedup marker as
# "<!-- nightly-acceptance-failure: job=X fingerprint=Y -->" and searched for
# that exact string via `gh issue list --search`. GitHub's issue search
# treats "-", "<", "!" and "=" as query syntax (a leading "-" excludes a
# term), not literal text, so that marker searched for something other than
# what it wrote -- silently breaking the one thing this script exists to do.
# search_token must be a single alphanumeric-and-hyphen-only token with no
# leading hyphen, which GitHub's search has no special reading of.
test_search_token_has_no_github_search_syntax_characters() {
  local xml="$tmp/token.xml" out="$tmp/token-content"
  write_xml "$xml" "FAIL:TestAccSomething"

  build_issue_content "acceptance-gh-app" "$xml" "https://x/1" "$out"

  local token
  token="$(cat "$out/search_token")"

  if [[ "$token" =~ ^- ]]; then
    echo "FAIL: search token: %q begins with '-', which GitHub's issue search reads as exclusion syntax: $token"
    fail=1
  fi

  if [[ ! "$token" =~ ^[A-Za-z0-9-]+$ ]]; then
    echo "FAIL: search token: contains a character outside [A-Za-z0-9-], which GitHub's issue search may not treat as literal: $token"
    fail=1
  fi
}

test_build_issue_content_fingerprint_is_order_independent() {
  local xml_a="$tmp/order-a.xml" xml_b="$tmp/order-b.xml"
  local out_a="$tmp/order-a-content" out_b="$tmp/order-b-content"

  write_xml "$xml_a" "FAIL:TestAccOne" "FAIL:TestAccTwo"
  write_xml "$xml_b" "FAIL:TestAccTwo" "FAIL:TestAccOne"

  build_issue_content "acceptance-gh-app" "$xml_a" "https://x/1" "$out_a"
  build_issue_content "acceptance-gh-app" "$xml_b" "https://x/2" "$out_b"

  assert_eq "$(cat "$out_a/fingerprint")" "$(cat "$out_b/fingerprint")" \
    "fingerprint: the same failing set in a different order must fingerprint the same, so a rerun does not open a duplicate"
}

test_build_issue_content_fingerprint_differs_for_a_different_failure_set() {
  local xml_a="$tmp/diff-a.xml" xml_b="$tmp/diff-b.xml"
  local out_a="$tmp/diff-a-content" out_b="$tmp/diff-b-content"

  write_xml "$xml_a" "FAIL:TestAccOne"
  write_xml "$xml_b" "FAIL:TestAccOne" "FAIL:TestAccTwo"

  build_issue_content "acceptance-gh-app" "$xml_a" "https://x/1" "$out_a"
  build_issue_content "acceptance-gh-app" "$xml_b" "https://x/2" "$out_b"

  if [ "$(cat "$out_a/fingerprint")" = "$(cat "$out_b/fingerprint")" ]; then
    echo "FAIL: fingerprint: a genuinely different failing set must not fingerprint the same as another one, or a real new failure would be silently folded into an old issue's comment thread"
    fail=1
  fi
}

test_build_issue_content_job_level_failure_has_a_stable_fingerprint_too() {
  local xml="$tmp/empty.xml" out_a="$tmp/joblevel-a" out_b="$tmp/joblevel-b"
  : >"$xml" # empty: the test step never produced a JUnit file at all

  build_issue_content "acceptance-gl-cloud" "$xml" "https://x/1" "$out_a"
  build_issue_content "acceptance-gl-cloud" "$xml" "https://x/2" "$out_b"

  assert_eq "$(cat "$out_a/fingerprint")" "$(cat "$out_b/fingerprint")" \
    "job-level failure: the same job failing with no recorded test failure twice must fingerprint the same"
  assert_contains "$(cat "$out_a/body")" "the job itself failed" "job-level failure: body says so plainly"
}

test_main_creates_a_new_issue_when_none_exists() {
  local xml="$tmp/new.xml"
  write_xml "$xml" "FAIL:TestAccNewFailure"

  local create_calls=0 comment_calls=0 created_title=""

  gh_find_open_issue() { echo ""; }
  gh_create_issue() {
    create_calls=$((create_calls + 1))
    created_title="$2"
  }
  gh_comment_issue() { comment_calls=$((comment_calls + 1)); }

  main "acceptance-gh-app" "$xml" "https://x/new" "CircleCI-Labs/terraform-provider-circleci"

  assert_eq "$create_calls" "1" "main/new: gh_create_issue called exactly once"
  assert_eq "$comment_calls" "0" "main/new: gh_comment_issue never called when no issue exists"
  assert_contains "$created_title" "acceptance-gh-app" "main/new: the created issue's title names the job"
}

test_main_comments_on_an_existing_issue_instead_of_duplicating() {
  local xml="$tmp/dup.xml"
  write_xml "$xml" "FAIL:TestAccRecurringFailure"

  local create_calls=0 comment_calls=0 commented_issue=""

  gh_find_open_issue() { echo "987"; }
  gh_create_issue() { create_calls=$((create_calls + 1)); }
  gh_comment_issue() {
    comment_calls=$((comment_calls + 1))
    commented_issue="$2"
  }

  main "acceptance-gh-app" "$xml" "https://x/dup" "CircleCI-Labs/terraform-provider-circleci"

  assert_eq "$create_calls" "0" "main/dup: gh_create_issue never called when a matching issue already exists"
  assert_eq "$comment_calls" "1" "main/dup: gh_comment_issue called exactly once"
  assert_eq "$commented_issue" "987" "main/dup: commented on the exact issue gh_find_open_issue returned"
}

test_build_issue_content_names_the_failing_tests
test_search_token_has_no_github_search_syntax_characters
test_build_issue_content_fingerprint_is_order_independent
test_build_issue_content_fingerprint_differs_for_a_different_failure_set
test_build_issue_content_job_level_failure_has_a_stable_fingerprint_too
test_main_creates_a_new_issue_when_none_exists
test_main_comments_on_an_existing_issue_instead_of_duplicating

if [ "$fail" -ne 0 ]; then
  echo "FAILED"
  exit 1
fi
echo "ok"
