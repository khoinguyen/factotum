#!/usr/bin/env bash
# Contract test for the lint concurrency fix. golangci-lint takes a
# machine-global lock ($TMPDIR/golangci-lint.lock) on every `run`, so lint runs
# from two worktrees at once used to fail with "parallel golangci-lint is
# running" after a 5s wait, reading a clean branch as red. The committed fix is
# `run.allow-parallel-runners: true` in .golangci.yml, which skips the lock
# (the cache it shares is Go's own, built for concurrent processes).
#
# This asserts the config still turns the lock off, and — in throwaway
# modules, never this repository — that concurrent runs both exit 0 while a
# real finding still fails the run (the fix must not weaken lint).
#
# Run: bash scripts/lint-parallel-runners_test.sh
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"
cfg="$root/.golangci.yml"

fail=0
pass=0

ok()  { pass=$((pass + 1)); }
bad() { echo "FAIL $1"; fail=$((fail + 1)); }

if ! command -v golangci-lint >/dev/null 2>&1; then
  echo "FAIL toolchain: golangci-lint not on PATH (run via 'mise run test:scripts')"
  echo "passed=$pass failed=$((fail + 1))"
  exit 1
fi

if [ ! -f "$cfg" ]; then
  echo "FAIL config: $cfg does not exist"
  echo "passed=$pass failed=$((fail + 1))"
  exit 1
fi

# The config must stay valid and must keep the parallel-runner opt-in.
if golangci-lint config verify --config "$cfg" >/dev/null 2>&1; then ok; else bad "config: 'golangci-lint config verify' rejects $cfg"; fi

if grep -qE '^[[:space:]]+allow-parallel-runners:[[:space:]]+true[[:space:]]*$' "$cfg"; then
  ok
else
  bad "config: $cfg does not set run.allow-parallel-runners: true"
fi

goversion="$(sed -n 's/^go //p' "$root/go.mod" | head -1)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# newmodule <dir> <file> <body>: a gofmt-clean throwaway module.
newmodule() {
  local dir="$1" file="$2" body="$3"
  mkdir -p "$dir"
  printf 'module throwaway\n\ngo %s\n' "$goversion" >"$dir/go.mod"
  printf '%s\n' "$body" >"$dir/$file"
}

clean='package throwaway

import "fmt"

func F() { fmt.Println("ok") }'
broken='package throwaway

var unusedVar = 1'

newmodule "$work/clean" ok.go "$clean"
newmodule "$work/broken" bad.go "$broken"
newmodule "$work/clean2" ok.go "$clean"

# The lock must actually be off for a run, not merely present in the file.
probe="$(cd "$work/clean" && GL_DEBUG=exec golangci-lint run --config "$cfg" 2>&1)" && probe_rc=0 || probe_rc=$?
if [ "$probe_rc" -eq 0 ]; then ok; else bad "probe: a clean module does not lint clean (exit $probe_rc)"; fi
if printf '%s\n' "$probe" | grep -q 'Parallel runners are allowed, no locking'; then
  ok
else
  bad "probe: golangci-lint still takes the machine-global lock (expected 'Parallel runners are allowed, no locking')"
fi

# A real finding still fails: the fix must not weaken lint.
if (cd "$work/broken" && golangci-lint run --config "$cfg" >/dev/null 2>&1); then
  bad "regression: an unused variable no longer fails the run"
else
  ok
fi

# The reported failure: two lint runs at once, as two worktrees do.
(cd "$work/clean"  && golangci-lint run --config "$cfg" >"$work/one.log" 2>&1) & first=$!
(cd "$work/clean2" && golangci-lint run --config "$cfg" >"$work/two.log" 2>&1) & second=$!
one=0; wait "$first"  || one=$?
two=0; wait "$second" || two=$?
if [ "$one" -eq 0 ] && [ "$two" -eq 0 ]; then
  ok
else
  bad "concurrent runs: exits were $one and $two"
  echo "--- run one"; cat "$work/one.log"
  echo "--- run two"; cat "$work/two.log"
fi

echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
