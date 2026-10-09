#!/usr/bin/env bash
# Contract test for the committed CI workflow. It cannot run the workflow, so it
# asserts the properties the cache depends on: the ms-playwright cache is
# restored before `mise run ci`, and its key is derived from the resolved
# Playwright version (scripts/playwright-version.sh), so a version bump misses
# the cache and re-downloads Chromium exactly once.
#
# Run: bash scripts/ci-workflow_test.sh
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"
wf="$root/.github/workflows/ci.yml"

fail=0
pass=0

ok()   { pass=$((pass + 1)); }
bad()  { echo "FAIL $1"; fail=$((fail + 1)); }

# has <name> <fixed-string>
has() {
  local name="$1" needle="$2"
  if grep -qF -- "$needle" "$wf"; then ok; else bad "$name: missing '$needle'"; fi
}

# before <name> <earlier> <later>  (both fixed strings; earlier must precede later)
before() {
  local name="$1" earlier="$2" later="$3" a b
  a="$(grep -nF -m1 -- "$earlier" "$wf" | cut -d: -f1 || true)"
  b="$(grep -nF -m1 -- "$later" "$wf" | cut -d: -f1 || true)"
  if [ -z "$a" ] || [ -z "$b" ]; then
    bad "$name: could not locate '$earlier' or '$later'"
  elif [ "$a" -lt "$b" ]; then ok; else bad "$name: '$earlier' (line $a) is not before '$later' (line $b)"; fi
}

if [ ! -f "$wf" ]; then
  echo "FAIL workflow: $wf does not exist"
  echo "passed=$pass failed=$((fail + 1))"
  exit 1
fi

has "triggers on pull requests" "pull_request"
has "runs on a GitHub-hosted Linux runner" "runs-on: ubuntu-latest"
has "runs the full suite" "mise run ci"
has "uses actions/cache" "actions/cache@"
has "caches the ms-playwright store" "~/.cache/ms-playwright"
has "keys the cache on the Playwright version output" 'steps.playwright.outputs.version'
has "falls back to a partial cache key" "restore-keys"
has "derives the version from the lockfile script" "scripts/playwright-version.sh"

# The cache key must be scoped to the version, not just anything.
if grep -qE 'key:.*ms-playwright-.*steps\.playwright\.outputs\.version' "$wf"; then
  ok
else
  bad "cache key does not combine ms-playwright- with the version output"
fi

# Restore the cache before the suite runs, or the install re-downloads Chromium.
before "cache restore precedes mise run ci" "Restore Playwright" "run: mise run ci"

echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
