#!/usr/bin/env bash
# Copyright (c) CircleCI
# SPDX-License-Identifier: MPL-2.0
#
# Turns one failed nightly acceptance job into a GitHub issue naming the
# integration and the failing test(s), instead of a red build nobody sees.
#
# Called only from the "when: on_fail" step in .circleci/config.yml's
# acceptance_test command, and only when that step already checked
# pipeline.parameters.nightly-acceptance-run is true -- a PR's own failure is
# already visible as its own failing check, and does not need a second,
# separate report.
#
# Deliberately split into two halves so the interesting part is testable
# without CircleCI or a network call:
#
#   * build_issue_content (pure): turns a JUnit XML file plus a job name into
#     a fingerprint, title and body. No I/O beyond reading the XML file
#     given to it. See file-nightly-failure-issue_test.sh, which calls it
#     directly.
#   * main (the CLI entry point below): calls build_issue_content, then talks
#     to GitHub through gh_find_open_issue/gh_create_issue/gh_comment_issue --
#     three one-line wrappers, so a test can redefine them after sourcing
#     this file (a plain shell function, reassignable like any other) to
#     fake "an issue with this fingerprint already exists" or "it doesn't"
#     without ever calling `gh` for real. See that test file for exactly this.
#
# Usage: file-nightly-failure-issue.sh <job-name> <junit-xml-file> <build-url> <repo>
#   job-name      $CIRCLE_JOB, e.g. "acceptance-gh-app"
#   junit-xml     test-reports/tests.xml from this job's test step
#   build-url     $CIRCLE_BUILD_URL, linked from the issue for triage
#   repo          "owner/name", passed to every `gh` call's --repo
#
# CIRCLECI_TEST_VCS_TYPE, read from the environment, names the integration in
# the issue title and body, the same way summarize-acceptance-run.sh reads it
# for its own failure message. Not required: an unset value prints "<unset>".
set -euo pipefail

# search_token_prefix, followed by the job name and the fingerprint, is
# embedded as a plain alphanumeric-and-hyphen token inside an HTML comment in
# every issue this script creates -- see build_issue_content below. Plain on
# purpose: it is also exactly what gh_find_open_issue passes to
# `gh issue list --search`, and GitHub's search treats characters like
# `<`, `!`, `--` and `=` (all of which an HTML comment or a "key=value" marker
# would otherwise contain) as query syntax, not literal text -- a marker
# built from those would search for something other than what it wrote. A
# single hyphenated token has no such ambiguity.
search_token_prefix="nightly-acceptance-failure"

# extract_failing_testcases emits one failing TestAcc* name per line, read
# from a JUnit XML file the same shape summarize-acceptance-run.sh already
# parses (a <testcase> element with a nested <failure> is a failure; one with
# a nested <skipped> is not, and is not a failure this script reports).
#
# Awk, not the XML-aware tools this repository has no dependency on, for the
# same reason summarize-acceptance-run.sh's extract_testcases is awk: the
# shape is simple enough (one flat, non-nested element per testcase in
# practice) that a real parser would be a new dependency to solve a problem
# three patterns already solve.
extract_failing_testcases() {
  awk '
    /<testcase / {
      name = ""
      failed = 0
      line = $0
      if (match(line, / name="[^"]*"/)) {
        name = substr(line, RSTART + 7, RLENGTH - 8)
      }
    }
    /<failure/ { failed = 1 }
    /<\/testcase>/ {
      if (failed && name != "" && name ~ /^TestAcc/) { print name }
    }
  ' "$1"
}

# fingerprint_of prints a short, stable, content-derived id for input on
# stdin. sha256 (via shasum, present on every image this runs on -- macOS and
# the cimg/go Linux image both ship it) rather than a name-based id, so the
# *same* failing test set always maps to the *same* fingerprint regardless of
# ordering -- the caller sorts its input first.
fingerprint_of() {
  shasum -a 256 | cut -c1-12
}

# build_issue_content is the pure half described in the module comment above.
# Writes three files under out_dir: fingerprint, title, body. Never touches
# the network, never calls gh -- exactly what
# file-nightly-failure-issue_test.sh exercises directly, with fixture XML
# files, to prove the fingerprint is stable and the failing tests actually
# appear in the body.
build_issue_content() {
  local job="$1" xml="$2" build_url="$3" out_dir="$4"
  local integration="${CIRCLECI_TEST_VCS_TYPE:-<unset>}"

  local failing=""
  if [ -s "$xml" ]; then
    failing="$(extract_failing_testcases "$xml" | sort)"
  fi

  local fingerprint_input failing_display
  if [ -n "$failing" ]; then
    fingerprint_input="tests:$(printf '%s' "$failing" | tr '\n' ',')"
    failing_display="$failing"
  else
    # The job failed with nothing recognisable as a failing TestAcc* testcase
    # in the XML -- task build, task ci:test itself, or
    # summarize-acceptance-run.sh's own "exercised zero real-API tests" check
    # can all fail this way. job-level-failure keeps that case's fingerprint
    # stable per job too, rather than either crashing on empty input or
    # (worse) hashing to the same fingerprint as a genuine test failure would.
    fingerprint_input="job-level-failure:${job}"
    failing_display="(no individual TestAcc* failure recorded in the JUnit XML -- the job itself failed; see the build log)"
  fi

  local fingerprint
  fingerprint="$(printf '%s' "$fingerprint_input" | fingerprint_of)"

  # search_token is what gh_find_open_issue actually searches for -- see
  # search_token_prefix above for why it has to stay plain alphanumeric-and-
  # hyphen. job is already exactly that shape (every job name in
  # .circleci/config.yml is), so it is used as-is rather than re-sanitised.
  local search_token="${search_token_prefix}-${job}-${fingerprint}"
  local marker="<!-- ${search_token} -->"

  mkdir -p "$out_dir"
  printf '%s' "$fingerprint" > "$out_dir/fingerprint"
  printf '%s' "$search_token" > "$out_dir/search_token"
  printf '%s' "$marker" > "$out_dir/marker"
  printf 'Nightly acceptance failure: %s (%s)' "$job" "$integration" > "$out_dir/title"

  {
    printf '%s\n\n' "$marker"
    printf 'The nightly acceptance-test run failed on **%s** (integration: %s).\n\n' "$job" "$integration"
    printf 'Build: %s\n\n' "$build_url"
    printf 'Failing test(s):\n'
    printf '%s\n' '```'
    printf '%s\n' "$failing_display"
    printf '%s\n\n' '```'
    printf 'Filed automatically by .circleci/scripts/file-nightly-failure-issue.sh. If this looks like\n'
    printf 'an upstream API change rather than a regression in this repository, retitle/triage\n'
    printf 'accordingly rather than closing it -- the next nightly run with this exact set of failing\n'
    printf 'test(s) comments here instead of opening a new issue; a run with a *different* failing set\n'
    printf 'opens a new one.\n'
  } > "$out_dir/body"
}

# gh_find_open_issue prints the number of an open issue whose body already
# contains search_token, or nothing if there is none. The real
# implementation; see the module comment above for how a test replaces this.
gh_find_open_issue() {
  local repo="$1" search_token="$2"
  gh issue list --repo "$repo" --state open --search "$search_token" --json number --jq '.[0].number // empty'
}

# gh_create_issue opens a new issue. Real implementation; see gh_find_open_issue.
gh_create_issue() {
  local repo="$1" title="$2" body_file="$3"
  gh issue create --repo "$repo" --title "$title" --body-file "$body_file"
}

# gh_comment_issue adds a comment to an existing issue rather than opening a
# duplicate. Real implementation; see gh_find_open_issue.
gh_comment_issue() {
  local repo="$1" issue_number="$2" body="$3"
  gh issue comment "$issue_number" --repo "$repo" --body "$body"
}

main() {
  local job="${1:?usage: file-nightly-failure-issue.sh <job-name> <junit-xml-file> <build-url> <repo>}"
  local xml="${2:?usage: file-nightly-failure-issue.sh <job-name> <junit-xml-file> <build-url> <repo>}"
  local build_url="${3:?usage: file-nightly-failure-issue.sh <job-name> <junit-xml-file> <build-url> <repo>}"
  local repo="${4:?usage: file-nightly-failure-issue.sh <job-name> <junit-xml-file> <build-url> <repo>}"

  local content_dir
  content_dir="$(mktemp -d)"
  # RETURN, not EXIT: this trap's command is expanded now, with content_dir's
  # actual value baked in, and fires when this function returns -- correct
  # even if main is called more than once in the same process (every test in
  # file-nightly-failure-issue_test.sh does exactly that), where an EXIT trap
  # would only ever run the last call's cleanup, and worse, would still
  # reference a `local` variable that has already gone out of scope by the
  # time the whole process actually exits.
  # shellcheck disable=SC2064
  trap "rm -rf '$content_dir'" RETURN

  build_issue_content "$job" "$xml" "$build_url" "$content_dir"

  local search_token title
  search_token="$(cat "$content_dir/search_token")"
  title="$(cat "$content_dir/title")"

  local existing
  existing="$(gh_find_open_issue "$repo" "$search_token")"

  if [ -n "$existing" ]; then
    echo "An open issue already reports this exact failure (fingerprint $(cat "$content_dir/fingerprint")): #$existing. Commenting instead of opening a duplicate."
    gh_comment_issue "$repo" "$existing" "Recurred: ${build_url}"
  else
    echo "No open issue reports this failure yet (fingerprint $(cat "$content_dir/fingerprint")). Opening one."
    gh_create_issue "$repo" "$title" "$content_dir/body"
  fi
}

# Run main only when executed directly, not when sourced (the test file sources
# this to call build_issue_content and to override the gh_* functions).
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  main "$@"
fi
