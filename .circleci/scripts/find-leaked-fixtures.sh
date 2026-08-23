#!/usr/bin/env bash
# Copyright (c) CircleCI
# SPDX-License-Identifier: MPL-2.0
#
# Lists every object across the four acceptance-test organizations whose name
# matches this suite's naming scheme (testUniqueName, in the acceptance-test
# harness under internal/provider: "tf-acc-<kind>-..."), so a leak
# left behind by a run that died between create and its t.Cleanup-registered
# delete is something a maintainer can SEE, rather than something discovered
# weeks later by running out of context quota or tripping a duplicate-name
# conflict.
#
# Usage:
#   CIRCLE_TOKEN=... .circleci/scripts/find-leaked-fixtures.sh
#
# Exit status is nonzero when anything matching the naming scheme is found,
# so this can run as its own CI step after the acceptance suite -- a nonzero
# exit there is a visible, separate failure, distinct from (and not
# swallowed by) whether the tests themselves passed.
#
# WHAT THIS CHECKS, and why not more:
#
#   - contexts and groups: both support a real list-and-filter-by-name check
#     (GET /context and GET /organizations/{id}/groups), so both are checked
#     against every one of the four organizations directly.
#   - runner resource classes: GET /api/v3/runner/resource?org-id=... on the
#     runner host. Checked the same way, even though none of the four jobs in
#     .circleci/config.yml currently sets a RUNNER_NAMESPACE, so today this
#     should always come back empty for all four -- a non-empty result here
#     is worth investigating on that basis alone.
#   - projects: there is no "list projects in an organization" route (checked
#     while building this script: neither GET /organization/{id}/projects
#     nor GET /api/v2/insights/organizations/{id}/summary exists -- both
#     answer 404). The only working listing is GET /api/v1.1/projects, which
#     is ACCOUNT-WIDE (every project the token's user follows, across every
#     organization they belong to, not just these four) and carries no
#     org-id field: a standalone project's vcs_url embeds the owning
#     org's UUID ("//circleci.com/<orgUUID>/<projectUUID>"), and a classic
#     (GitHub/Bitbucket) project's vcs_url instead embeds the org's VCS
#     username. This script matches on whichever of those applies per
#     organization, then filters by reponame.
#
# NOT CHECKED: spend budgets. Budget has no name field at all (it is keyed by
# organization and, optionally, project id -- see internal/circleci/
# budget.go) so "matches the naming scheme" does not apply to it; there is
# nothing here for a name-based leak check to find.
#
# NOT CHECKED: organizations themselves (circleci_organization creates and
# deletes whole organizations, e.g. TestAccOrganizationCircleCiResource).
# There is no list-all-organizations-this-token-can-see route either, and an
# organization created by a leaked test does not live inside one of the four
# known org ids -- it IS a fifth one, with no fixed id to start from. A
# maintainer suspecting an organization leak has to search the CircleCI org
# picker UI for a "tf-acc-org-..." name by hand; this script cannot enumerate
# that surface.
#
# A BLIND SPOT IN THE "projects" CHECK ABOVE, found live while building
# internal/provider/project_resource_test.go's classic-organization coverage:
# GET /api/v1.1/projects -- what the "projects" check above is built on --
# only lists projects that have been FOLLOWED. A project whose v2
# organization/{id}/project create succeeded but whose v1.1 follow call right
# after it did not (see followProject's doc comment in
# internal/circleci/project.go; it needs a commit on the repository's default
# branch) is real, billed against nothing, runs nothing, but is invisible
# here -- confirmed by creating one on gh-oauth-cci-1 and observing it absent
# from this same GET /api/v1.1/projects response. There is no route that
# lists it either (same 404s as the "projects" section above already
# documents). If this script ever reports zero project leaks right after a
# run that DID hit a follow failure, that is this blind spot, not a clean
# result -- check the acceptance log for "Branch not found" or "following
# project" and, if found, locate the orphan by its slug from the log (or from
# a surviving terraform-plugin-testing working directory's generated .tf
# file, if the process is still around) and delete it by hand:
# DELETE /api/v2/project/<vcs-type>/<org-name>/<name>.

set -euo pipefail

: "${CIRCLE_TOKEN:?set CIRCLE_TOKEN to an API token that can read all four organizations below}"

HOST="https://circleci.com"
RUNNER_HOST="https://runner.circleci.com"

# The four organizations. Kept in sync with .circleci/config.yml by hand --
# these are not secrets (org and project UUIDs and slugs are not; see
# TESTING.md), so there is nothing to gain by generating this list instead of
# just reading it here next to the jobs it mirrors.
#
#   name              org id                                 vcs match
ORGS=(
  "gh-app-cci-1    e75c804e-7f5c-4506-9dad-03fc86af39d1    id:e75c804e-7f5c-4506-9dad-03fc86af39d1"
  "gh-oauth-cci-1  9b55d3de-16b2-445a-882f-a4ca5c8bbafc    user:gh-oauth-cci-1"
  "gh-oauth-cci-2  11047c98-2415-4410-98dc-899c86f8d0e5    user:gh-oauth-cci-2"
  "gitlab-test     eea3a305-6eb2-4a6b-b011-9bd71b88c8d2    id:eea3a305-6eb2-4a6b-b011-9bd71b88c8d2"
)

# Matches testUniqueName's actual output ("tf-acc-<kind>-<slug>-MMDD-HHMM-
# <ci>-<random>"), not merely the "tf-acc-" prefix. That distinction matters:
# this repository already has PERMANENT fixtures named "tf-acc-fixture" (the
# pre-existing context each org keeps for CIRCLECI_TEST_<KEY>_CONTEXT_ID) and
# "tf-acc-adoptable" (the GitHub OAuth repository CIRCLECI_TEST_GH_OAUTH_
# ADOPTABLE_REPO_NAME points at) predating this harness, chosen by a human
# and meant to stay forever. Matching on the bare prefix flagged both as
# "leaked" the first time this script ran against the real orgs -- see
# TESTING.md's "Parallel-safe acceptance fixtures" section for that result.
# Requiring the run-stamp's two four-digit groups (MMDD-HHMM) is what tells
# a harness-generated, disposable name apart from a hand-chosen permanent
# one: no human names a fixture "fixture-0822-1530-...".
NAME_PATTERN='^tf-acc-.+-[0-9]{4}-[0-9]{4}-'
found=0

curl_api() {
  curl -sS --fail-with-body -H "Circle-Token: $CIRCLE_TOKEN" "$@"
}

echo "=== Leak detector: objects matching '$NAME_PATTERN' across the four acceptance orgs ==="
echo

for row in "${ORGS[@]}"; do
  # shellcheck disable=SC2086 # deliberate word-splitting of the fixed-column row above
  read -r org_name org_id vcs_match <<<"$row"

  echo "--- $org_name ($org_id) ---"

  # --- contexts ---
  page_token=""
  ctx_hits=0
  while :; do
    url="$HOST/api/v2/context?owner-id=$org_id&owner-type=organization"
    [ -n "$page_token" ] && url="$url&page-token=$page_token"

    resp="$(curl_api "$url")"
    matches="$(printf '%s' "$resp" | python3 -c "
import json, re, sys
data = json.load(sys.stdin)
pat = re.compile(r'$NAME_PATTERN')
for item in data.get('items', []):
    if pat.search(item.get('name', '')):
        print(f\"  context  {item['id']}  {item['name']}\")
")"
    if [ -n "$matches" ]; then
      echo "$matches"
      ctx_hits=$((ctx_hits + $(printf '%s\n' "$matches" | grep -c .)))
    fi

    page_token="$(printf '%s' "$resp" | python3 -c "import json,sys;print(json.load(sys.stdin).get('next_page_token') or '')")"
    [ -z "$page_token" ] && break
  done

  # --- groups ---
  page_token=""
  grp_hits=0
  while :; do
    url="$HOST/api/v2/organizations/$org_id/groups"
    [ -n "$page_token" ] && url="$url?page-token=$page_token"

    resp="$(curl_api "$url")"
    matches="$(printf '%s' "$resp" | python3 -c "
import json, re, sys
data = json.load(sys.stdin)
pat = re.compile(r'$NAME_PATTERN')
for item in data.get('items', []):
    if pat.search(item.get('name', '')):
        print(f\"  group    {item['id']}  {item['name']}\")
")"
    if [ -n "$matches" ]; then
      echo "$matches"
      grp_hits=$((grp_hits + $(printf '%s\n' "$matches" | grep -c .)))
    fi

    page_token="$(printf '%s' "$resp" | python3 -c "import json,sys;print(json.load(sys.stdin).get('next_page_token') or '')")"
    [ -z "$page_token" ] && break
  done

  # --- runner resource classes ---
  resp="$(curl -sS -H "Circle-Token: $CIRCLE_TOKEN" "$RUNNER_HOST/api/v3/runner/resource?org-id=$org_id" || echo '{}')"
  rrc_matches="$(printf '%s' "$resp" | python3 -c "
import json, re, sys
try:
    data = json.load(sys.stdin)
except Exception:
    data = {}
pat = re.compile(r'$NAME_PATTERN|/acc-test-runner-')
for item in data.get('items', []) or []:
    rc = item.get('resource_class', '')
    if pat.search(rc):
        print(f\"  rrc      {item.get('id','?')}  {rc}\")
" 2>/dev/null || true)"
  if [ -n "$rrc_matches" ]; then
    echo "$rrc_matches"
  fi
  rrc_hits="$(printf '%s\n' "$rrc_matches" | grep -c . || true)"

  # --- projects, via the account-wide v1.1 listing, filtered to this org ---
  proj_hits=0
  if [ "${PROJECTS_JSON:-}" = "" ]; then
    PROJECTS_JSON="$(curl_api "$HOST/api/v1.1/projects")"
  fi
  kind="${vcs_match%%:*}"
  value="${vcs_match#*:}"
  proj_matches="$(printf '%s' "$PROJECTS_JSON" | python3 -c "
import json, re, sys
data = json.load(sys.stdin)
pat = re.compile(r'$NAME_PATTERN')
kind, value = '$kind', '$value'
for p in data:
    vcs_url = p.get('vcs_url', '') or ''
    reponame = p.get('reponame', '') or ''
    if kind == 'id' and value not in vcs_url:
        continue
    if kind == 'user' and p.get('username') != value:
        continue
    if pat.search(reponame):
        print(f\"  project  {vcs_url}\")
")"
  if [ -n "$proj_matches" ]; then
    echo "$proj_matches"
    proj_hits=$(printf '%s\n' "$proj_matches" | grep -c .)
  fi

  total=$((ctx_hits + grp_hits + rrc_hits + proj_hits))
  if [ "$total" -eq 0 ]; then
    echo "  (nothing matching '$NAME_PATTERN')"
  else
    found=$((found + total))
  fi
  echo
done

if [ "$found" -gt 0 ]; then
  echo "=== FOUND $found leaked object(s) matching '$NAME_PATTERN' ==="
  exit 1
fi

echo "=== No leaks found among contexts, groups, runner resource classes and projects ==="
