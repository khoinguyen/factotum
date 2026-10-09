#!/usr/bin/env bash
# Tests for playwright-version.sh. Hermetic: fixtures are tiny lockfile
# snippets, so the real web/pnpm-lock.yaml is only read by one case.
#
# Run: bash scripts/playwright-version_test.sh
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="$here/playwright-version.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail=0
pass=0

# assert_version <name> <expected> <fixture>
assert_version() {
  local name="$1" want="$2" file="$3" got rc=0
  got="$("$script" "$file" 2>&1)" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "FAIL $name: exited $rc: $got"
    fail=$((fail + 1))
    return
  fi
  if [ "$got" != "$want" ]; then
    echo "FAIL $name: want '$want', got '$got'"
    fail=$((fail + 1))
    return
  fi
  pass=$((pass + 1))
}

# assert_error <name> <grep> <fixture>
assert_error() {
  local name="$1" grepfor="$2" file="$3" out rc=0
  out="$("$script" "$file" 2>&1)" || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "FAIL $name: expected non-zero exit, got '$out'"
    fail=$((fail + 1))
    return
  fi
  case "$out" in
    *"$grepfor"*) pass=$((pass + 1)) ;;
    *) echo "FAIL $name: stderr lacks '$grepfor': $out"; fail=$((fail + 1)) ;;
  esac
}

cat >"$tmp/v9.yaml" <<'YAML'
lockfileVersion: '9.0'

importers:

  .:
    devDependencies:
      playwright:
        specifier: ^1.64.0
        version: 1.64.0

packages:

  playwright-core@1.64.0:
    resolution: {integrity: sha512-aaa}

  playwright@1.64.0:
    resolution: {integrity: sha512-bbb}
    hasBin: true
YAML
assert_version "reads the resolved version" "1.64.0" "$tmp/v9.yaml"

cat >"$tmp/bumped.yaml" <<'YAML'
lockfileVersion: '9.0'

packages:

  playwright-core@1.99.2:
    resolution: {integrity: sha512-aaa}

  playwright@1.99.2:
    resolution: {integrity: sha512-bbb}
YAML
assert_version "a bump changes the version" "1.99.2" "$tmp/bumped.yaml"

cat >"$tmp/scoped.yaml" <<'YAML'
lockfileVersion: '9.0'

packages:

  '@vitest/browser-playwright@5.0.3':
    resolution: {integrity: sha512-ccc}

  playwright@1.70.1:
    resolution: {integrity: sha512-ddd}
YAML
assert_version "ignores playwright-core and scoped wrappers" "1.70.1" "$tmp/scoped.yaml"

cat >"$tmp/none.yaml" <<'YAML'
lockfileVersion: '9.0'

packages:

  playwright-core@1.64.0:
    resolution: {integrity: sha512-aaa}
YAML
assert_error "missing playwright entry errors" "no playwright entry" "$tmp/none.yaml"

# The committed lockfile must yield a plain semver, not the caret range.
real="$("$script" "$here/../web/pnpm-lock.yaml")"
case "$real" in
  [0-9]*.[0-9]*.[0-9]*) pass=$((pass + 1)) ;;
  *) echo "FAIL real lockfile: not a semver: '$real'"; fail=$((fail + 1)) ;;
esac

echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
