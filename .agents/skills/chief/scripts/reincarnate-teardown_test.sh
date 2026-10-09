#!/usr/bin/env bash
# Tests for reincarnate-teardown.sh. Hermetic: stub pkill/cmux on PATH record
# the argv they receive, so no real process is ever killed and no real cmux
# surface is touched.
#
# Run: bash .agents/skills/chief/scripts/reincarnate-teardown_test.sh
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="$here/reincarnate-teardown.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

export TEARDOWN_STUB_LOG="$tmp/calls.log"
export TEARDOWN_PKILL_RC=0
export CMUX_STUB_TREE="$tmp/tree.txt"
: >"$TEARDOWN_STUB_LOG"

mkdir -p "$tmp/bin"
cat >"$tmp/bin/pkill" <<'STUB'
#!/usr/bin/env bash
printf 'pkill %s\n' "$*" >>"$TEARDOWN_STUB_LOG"
exit "${TEARDOWN_PKILL_RC:-0}"
STUB
cat >"$tmp/bin/cmux" <<'STUB'
#!/usr/bin/env bash
if [ "${1:-}" = tree ]; then
  cat "$CMUX_STUB_TREE"
  exit 0
fi
printf 'cmux %s\n' "$*" >>"$TEARDOWN_STUB_LOG"
STUB
chmod +x "$tmp/bin/pkill" "$tmp/bin/cmux"
export PATH="$tmp/bin:$PATH"

# The successor shares the chief workspace; surface:39 is the successor's live
# surface, surface:37 is the retired chief's.
cat >"$CMUX_STUB_TREE" <<'TREE'
window window:1 [current]
└── workspace workspace:10 "chief"
    ├── pane pane:21
    │   └── surface surface:37 [terminal] "chief" [selected]
    └── pane pane:23
        └── surface surface:39 [terminal] "chief-successor" [selected]
TREE

fail=0
pass=0

# assert_run <name> <expected-log> -- <script args...>
assert_run() {
  local name="$1" want="$2"; shift 3
  : >"$TEARDOWN_STUB_LOG"
  local out rc=0
  out="$("$script" "$@" 2>&1)" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "FAIL $name: script exited $rc: $out"
    fail=$((fail + 1))
    return
  fi
  local got
  got="$(cat "$TEARDOWN_STUB_LOG")"
  if [ "$got" != "$want" ]; then
    echo "FAIL $name"
    echo "  want: $want"
    echo "  got:  $got"
    fail=$((fail + 1))
    return
  fi
  pass=$((pass + 1))
}

# The orphaned receiver is matched by PPID 1, then the retired surface is
# closed under its workspace.
assert_run "kills the orphaned chief receiver, closes the retired surface" \
  "pkill -9 -P 1 -f ft msg agent claim.*--actor chief
cmux close-surface --workspace workspace:10 --surface surface:37" \
  -- surface:37

# pkill exits 1 when no orphan matches; the surface close must still run.
export TEARDOWN_PKILL_RC=1
assert_run "no orphan still closes the retired surface" \
  "pkill -9 -P 1 -f ft msg agent claim.*--actor chief
cmux close-surface --workspace workspace:10 --surface surface:37" \
  -- surface:37
export TEARDOWN_PKILL_RC=0

# A missing arg is a usage error and touches nothing.
: >"$TEARDOWN_STUB_LOG"
rc=0
out="$("$script" 2>&1)" || rc=$?
if [ "$rc" -eq 0 ]; then
  echo "FAIL missing arg should fail"; fail=$((fail + 1))
elif [ -n "$(cat "$TEARDOWN_STUB_LOG")" ]; then
  echo "FAIL missing arg still called the tools: $(cat "$TEARDOWN_STUB_LOG")"; fail=$((fail + 1))
else pass=$((pass + 1)); fi

# An unknown surface ref fails loudly after the kill, without closing anything.
: >"$TEARDOWN_STUB_LOG"
rc=0
out="$("$script" surface:999 2>&1)" || rc=$?
if [ "$rc" -eq 0 ]; then
  echo "FAIL unknown surface should fail"; fail=$((fail + 1))
elif grep -q 'close-surface' "$TEARDOWN_STUB_LOG"; then
  echo "FAIL unknown surface still closed something"; fail=$((fail + 1))
elif ! printf '%s' "$out" | grep -qi 'surface'; then
  echo "FAIL unknown surface: error does not mention the surface: $out"; fail=$((fail + 1))
else pass=$((pass + 1)); fi

echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
