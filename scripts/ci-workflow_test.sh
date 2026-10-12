#!/usr/bin/env bash
# Contract test for the committed CI workflow. It cannot run the workflow, so it
# asserts the properties the fast compile+unit gate depends on: it triggers on
# push and pull_request, runs on a GitHub-hosted Linux runner, runs `go build
# ./...` (compile gate) and `go test -race ./...` (unit tests), runs this
# contract test itself (so a workflow edit that breaks the contract turns the
# job red), and stays dependency-light — no sudo, no browser/Playwright
# download, and not the full `mise run ci`.
#
# Run: bash scripts/ci-workflow_test.sh
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"
wf="$root/.github/workflows/ci.yml"

fail=0
pass=0

ok()  { pass=$((pass + 1)); }
bad() { echo "FAIL $1"; fail=$((fail + 1)); }

# has <name> <fixed-string>
has() {
  local name="$1" needle="$2"
  if grep -qF -- "$needle" "$wf"; then ok; else bad "$name: missing '$needle'"; fi
}

# lacks <name> <extended-regex>
lacks() {
  local name="$1" re="$2"
  if grep -qE -- "$re" "$wf"; then bad "$name: found '$re'"; else ok; fi
}

if [ ! -f "$wf" ]; then
  echo "FAIL workflow: $wf does not exist"
  echo "passed=$pass failed=$((fail + 1))"
  exit 1
fi

has "triggers on pushes" "push:"
has "triggers on pull requests" "pull_request:"
has "runs on a GitHub-hosted Linux runner" "runs-on: ubuntu-latest"
has "sets up a Go toolchain" "setup-go@"
has "runs the compile gate" "go build ./..."
has "runs the unit tests with the race detector" "go test -race ./..."

# The gate must run this contract test, or the workflow can drift from it.
has "runs the workflow contract test" "bash scripts/ci-workflow_test.sh"

# Dependency-light: the gate must not need sudo or download a browser.
lacks "does not use sudo" '(^|[^[:alnum:]_])sudo([^[:alnum:]_]|$)'
lacks "does not download Playwright" 'playwright'
lacks "does not download Chromium" 'chromium'
lacks "does not install a browser" 'install --with-deps'
# Not the full suite — that pulls the browser and pnpm and stays deferred.
lacks "does not run the full mise run ci" 'mise run ci'

echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
