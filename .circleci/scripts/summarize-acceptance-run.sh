#!/usr/bin/env bash
# Copyright (c) CircleCI
# SPDX-License-Identifier: MPL-2.0
#
# Turns one acceptance job's raw `task ci:test` output and JUnit XML into
# test-reports/vcs-coverage.txt, and fails the job when that report shows the
# job proved nothing about the integration it exists to cover.
#
# Pulled out of .circleci/config.yml so it can be run, and tested, outside a
# CircleCI job — see summarize-acceptance-run_test.sh in this directory for
# the fixture-driven tests, including a reconstruction of the actual
# acceptance-gh-hybrid artifact that motivated this rewrite.
#
# Usage: summarize-acceptance-run.sh <log-file> <junit-xml-file> <output-file>
#
# CIRCLECI_TEST_VCS_TYPE, read from the environment, names the integration in
# the "exercised zero real-API tests" failure message. It is not required —
# an unset value just prints "<unset>" there — because the VCS integration
# coverage block itself (printed by internal/provider's TestMain) already
# carries it, and this script does not need to re-derive it to do its job.
set -euo pipefail

log="${1:?usage: summarize-acceptance-run.sh <log-file> <junit-xml-file> <output-file>}"
xml="${2:?usage: summarize-acceptance-run.sh <log-file> <junit-xml-file> <output-file>}"
out="${3:?usage: summarize-acceptance-run.sh <log-file> <junit-xml-file> <output-file>}"

if [ ! -s "$log" ]; then
  echo "No $log, so the test step did not get far enough to produce one." >&2
  echo "Read that step's failure instead; there is nothing to summarize here." >&2
  exit 0
fi

# The block TestMain prints once every test has finished. It ends where `go
# test` prints the package's own ok/FAIL line.
block="$(awk '/^=== VCS integration coverage/ { inside = 1 }
              inside && /^(ok|FAIL|---)/       { inside = 0 }
              inside                           { print }' "$log")"

# "Exercised by this run (N):" is TestMain's own headline; pull N back out of
# it rather than recomputing it, so this script and that block can never
# disagree about what a real-API test is.
exercised=0
if [ -n "$block" ]; then
  exercised="$(printf '%s\n' "$block" \
    | sed -nE 's/^Exercised by this run \(([0-9]+)\).*/\1/p' \
    | head -1)"
  : "${exercised:=0}"
fi

# --- Every TestAcc* testcase, skipped or not, straight from the JUnit XML ---
#
# The XML is the source of truth for both the ran/skipped counts AND each
# skip's own reason, and deriving both from the same rows is what keeps them
# from disagreeing: total and skipped come from counting rows, causes come
# from categorizing the skipped rows' own messages, and "other" catches
# anything a category doesn't recognise. There is no second, independent path
# that could drift out of step with the first.
#
# Why not the human-readable log instead, the way the reason list used to be
# built (`grep '^=== SKIP: .*TestAcc'`, which found nothing against a real
# run despite 36 skips): gotestsum's console format is a rendering choice —
# which heading it prints, whether a reason wraps, how nesting is shown — not
# a contract, and it had already drifted out from under that grep once. The
# JUnit `message` attribute is the one piece of this pipeline actually meant
# to be parsed by something else; every skip reason lives there verbatim,
# XML-escaped but otherwise unmodified, one row per testcase, which is a
# format worth relying on instead of the printable log.
extract_testcases() {
  # Emits one row per <testcase>, tab-separated: name, and (only if the
  # testcase skipped) its decoded skip message. A passing testcase's row has
  # an empty second field.
  #
  # &#xA; decodes to the placeholder \x01, not a real newline: the row this
  # function emits has to stay on one line for the `while read` loop below to
  # see it as one record, and a skip message is exactly the kind of value
  # that legitimately contains a decoded newline. The caller turns \x01 back
  # into '\n' once the record has been split into fields.
  awk '
    function decode(s) {
      gsub(/&#xA;/, "\x01", s)
      gsub(/&quot;/, "\"", s)
      gsub(/&apos;/, "\x27", s)
      gsub(/&lt;/, "<", s)
      gsub(/&gt;/, ">", s)
      gsub(/&amp;/, "\\&", s)
      return s
    }
    /<testcase / {
      if (name != "") { print name "\t" msg }
      name = ""; msg = ""
      line = $0
      # The leading space is load-bearing: without it this matches inside
      # classname="..." too, since "classname=" ends in "name=".
      if (match(line, / name="[^"]*"/)) {
        name = substr(line, RSTART + 7, RLENGTH - 8)
      }
    }
    /<skipped message="/ {
      line = $0
      if (match(line, /message="/)) {
        rest = substr(line, RSTART + RLENGTH)
        if (match(rest, /"[ \t]*\/?>/)) {
          msg = decode(substr(rest, 1, RSTART - 1))
        }
      }
    }
    END { if (name != "") { print name "\t" msg } }
  ' "$1"
}

# The actual t.Skip/t.Skipf text, stripped of the "=== RUN"/"--- SKIP" frame
# gotestsum wraps around it and the "<source file>:NN: " prefix testing adds. A
# multi-line message (more than one t.Log before the skip) keeps every line.
skip_reason() {
  printf '%s' "$1" \
    | tr '\001' '\n' \
    | grep -v -E '^(=== RUN|--- SKIP:)' \
    | sed -E 's/^[[:space:]]*[A-Za-z0-9_.]+\.go:[0-9]+:[[:space:]]*//' \
    | sed '/^[[:space:]]*$/d'
}

# Categorizes one skip reason. Every branch here is a message this repository
# actually emits (acctest_test.go, provider_test.go, vcs_gating_test.go,
# project_resource_test.go, project_data_source_test.go); "other" is not a
# vocabulary gap to fill in later, it is the reason the numbers reconcile no
# matter what a future skip message says.
categorize() {
  case "$1" in
    *"no API token for acceptance tests"*)          echo "no_token" ;;
    *"is set to a placeholder value"*)               echo "placeholder" ;;
    *"must be set for this acceptance test"*)        echo "fixture_unset" ;;
    *"is not a recognized integration key"*)         echo "unrecognized_vcs_type" ;;
    *"does not support this feature"*)                echo "vcs_type_unsupported" ;;
    *"only a standalone"*"organization"*"supports"*)  echo "standalone_org_required" ;;
    *"has not been measured"*)                        echo "unmeasured_vcs_info_shape" ;;
    *)                                                 echo "other" ;;
  esac
}

total=0
skipped=0
no_token=0
placeholder=0
fixture_unset=0
unrecognized_vcs_type=0
vcs_type_unsupported=0
standalone_org_required=0
unmeasured_vcs_info_shape=0
other=0
skip_lines=""

if [ -s "$xml" ]; then
  while IFS=$'\t' read -r name msg; do
    case "$name" in
      TestAcc*) ;;
      *) continue ;;
    esac

    total=$((total + 1))

    if [ -z "$msg" ]; then
      continue
    fi

    skipped=$((skipped + 1))
    reason="$(skip_reason "$msg")"
    [ -n "$reason" ] || reason="(skipped with no logged reason)"

    cause="$(categorize "$reason")"
    case "$cause" in
      no_token)                 no_token=$((no_token + 1)) ;;
      placeholder)               placeholder=$((placeholder + 1)) ;;
      fixture_unset)             fixture_unset=$((fixture_unset + 1)) ;;
      unrecognized_vcs_type)     unrecognized_vcs_type=$((unrecognized_vcs_type + 1)) ;;
      vcs_type_unsupported)      vcs_type_unsupported=$((vcs_type_unsupported + 1)) ;;
      standalone_org_required)   standalone_org_required=$((standalone_org_required + 1)) ;;
      unmeasured_vcs_info_shape) unmeasured_vcs_info_shape=$((unmeasured_vcs_info_shape + 1)) ;;
      *)                         other=$((other + 1)) ;;
    esac

    skip_lines="${skip_lines}${name}: ${reason}
"
  done < <(extract_testcases "$xml")
fi

causes_total=$((no_token + placeholder + fixture_unset + unrecognized_vcs_type + \
  vcs_type_unsupported + standalone_org_required + unmeasured_vcs_info_shape + other))

{
  if [ -n "$block" ]; then
    printf '%s\n' "$block"
  else
    echo "=== VCS integration coverage ==="
    echo "  (never printed: no VCS-gated test reached testRequireVCSType)"
  fi

  echo
  echo "*** REAL-API TESTS EXERCISED THIS RUN: $exercised ***"
  echo "This is the number that matters. Every count below is context, not evidence of API coverage —"
  echo "most TestAcc tests in this package are mock-backed and run with no credential at all."
  echo
  echo "=== Acceptance tests (TestAcc*) in internal/provider ==="
  printf '  ran (mock-backed or real; NOT a proxy for real-API coverage): %s\n' "$((total - skipped))"
  printf '  skipped:                                                      %s\n' "$skipped"
  printf '  total:                                                        %s\n' "$total"
  echo
  echo "=== Skips by cause ==="
  printf '  fixture variable unset:                 %s\n' "$fixture_unset"
  printf '  fixture is placeholder:                 %s\n' "$placeholder"
  printf '  no API token:                           %s\n' "$no_token"
  printf '  integration unsupported by test:        %s\n' "$vcs_type_unsupported"
  printf '  organization class unsupported by test: %s\n' "$standalone_org_required"
  printf '  CIRCLECI_TEST_VCS_TYPE not recognized:   %s\n' "$unrecognized_vcs_type"
  printf '  vcs_info shape not yet measured:         %s\n' "$unmeasured_vcs_info_shape"
  printf '  other:                                   %s\n' "$other"
  printf '  (causes total, must equal skipped above): %s\n' "$causes_total"
  echo
  echo "=== Every skip, with its reason ==="
  if [ -n "$skip_lines" ]; then
    printf '%s' "$skip_lines"
  else
    echo "  (none)"
  fi
} | tee "$out"

# The checks below are the reason this step is not decoration. A job that
# talks to no organization at all still passes every assertion it makes, so
# without them "green" and "did nothing" look identical from the outside —
# which is the failure mode this whole arrangement exists to rule out.
if [ -z "$block" ]; then
  echo >&2
  echo "FAIL: no VCS integration coverage block was printed, so no VCS-gated test ever" >&2
  echo "resolved CIRCLECI_TEST_VCS_TYPE. Either it is unset for this job or the suite" >&2
  echo "did not reach the gate; either way this job proved nothing about the" >&2
  echo "integration it claims to cover." >&2
  exit 1
fi

# This is the one that catches a detached context. Every fixture variable is
# set in this job's own environment, so a test can only get as far as the
# token check by having found its fixtures — and then skip because
# CIRCLE_TOKEN is absent. Without this check that is a green job that created
# nothing.
if [ "$no_token" -gt 0 ]; then
  echo >&2
  echo "FAIL: tests skipped for a missing API token ($no_token of them), so CIRCLE_TOKEN" >&2
  echo "never reached this job. The terraform-provider-acc-token context is not" >&2
  echo "attached, or its restriction excludes this branch." >&2
  exit 1
fi

if [ "$total" -gt 0 ] && [ "$((total - skipped))" -eq 0 ]; then
  echo >&2
  echo "FAIL: every one of the $total acceptance tests skipped. Even the mock-backed" >&2
  echo "ones did not run, so this is not a fixture problem — read the test step." >&2
  exit 1
fi

# The check the incident that prompted this file exposed a gap in: a job can
# be green, print a VCS integration coverage block, have every fixture
# resolve, and STILL have exercised zero tests against the real API — every
# gated test either skips because the integration doesn't support it or
# because the organization's class doesn't. "Exercised by this run (0)" used
# to be something four of this file's own comments told a reader to expect
# and ignore; it is not something to ignore, it is the one number this whole
# report exists to produce.
#
# No threshold above zero is implemented here. github_app/oauth/gitlab were
# observed at 7/3/2 genuinely-exercised tests in one real run, and nothing
# about the suite's design says what the next number should be — a job's
# count moves as tests gain or lose their VCS/org-class gates for reasons
# that have nothing to do with a regression, so any nonzero floor would be a
# number pulled out of the air. Zero is not: it is the exact line between
# "this job's fixtures fed at least one real assertion" and "this job's
# fixtures fed none", which is the one thing every fixture set here is
# supposed to guarantee.
if [ "$exercised" -eq 0 ]; then
  echo >&2
  echo "FAIL: ${CIRCLECI_TEST_VCS_TYPE:-<unset>} exercised zero real-API tests this run. Every" >&2
  echo "VCS- or organization-class-gated test either does not support this integration or needs" >&2
  echo "an organization class this job's fixtures are not, so a green run here proved nothing" >&2
  echo "about ${CIRCLECI_TEST_VCS_TYPE:-this integration}. See the VCS integration coverage block above for which" >&2
  echo "gate(s) turned every candidate away." >&2
  exit 1
fi

# Guards the guard: if this ever fires, the categorization above has a gap
# for something that isn't landing in "other", which means it isn't the
# report's arithmetic that's wrong but this script's. It should be
# unreachable by construction (every skip falls into a named cause or
# "other"), which is exactly why it is worth asserting rather than assuming.
if [ "$causes_total" -ne "$skipped" ]; then
  echo >&2
  echo "FAIL: this script's own skip-cause tally ($causes_total) does not equal the skipped count" >&2
  echo "it is supposed to add up to ($skipped). That is a bug in this script, not in the suite —" >&2
  echo "see categorize() in summarize-acceptance-run.sh." >&2
  exit 1
fi
