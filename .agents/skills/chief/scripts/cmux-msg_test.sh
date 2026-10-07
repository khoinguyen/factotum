#!/usr/bin/env bash
# Tests for cmux-msg.sh. Hermetic: a stub `cmux` on PATH records the argv it is
# called with, so no real surface is ever messaged.
#
# Run: bash .agents/skills/chief/scripts/cmux-msg_test.sh
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="$here/cmux-msg.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

export CMUX_STUB_LOG="$tmp/calls.log"
export CMUX_STUB_TREE="$tmp/tree.txt"
: >"$CMUX_STUB_LOG"

mkdir -p "$tmp/bin"
cat >"$tmp/bin/cmux" <<'STUB'
#!/usr/bin/env bash
if [ "${1:-}" = tree ]; then
  cat "$CMUX_STUB_TREE"
else
  printf '%s\n' "$*" >>"$CMUX_STUB_LOG"
fi
STUB
chmod +x "$tmp/bin/cmux"
export PATH="$tmp/bin:$PATH"

cat >"$CMUX_STUB_TREE" <<'TREE'
window window:1 [current]
├── workspace workspace:10 "chief"
│   └── pane pane:21
│       └── surface surface:37 [terminal] "chief" [selected]
├── workspace workspace:2 "a1"
│   └── pane pane:9
│       └── surface surface:9 [terminal] "one" [selected]
├── workspace workspace:9 "a[1]"
│   └── pane pane:8
│       └── surface surface:8 [terminal] "two" [selected]
└── workspace workspace:52 "t-demo"
    └── pane pane:262
        └── surface surface:463 [terminal] "builder-t-demo" [selected]
TREE

fail=0
pass=0

# assert_run <name> <expected-call> -- <script args...>
assert_run() {
  local name="$1" want="$2"; shift 3
  : >"$CMUX_STUB_LOG"
  if ! out="$("$script" "$@" 2>&1)"; then
    echo "FAIL $name: script exited non-zero: $out"
    fail=$((fail + 1))
    return
  fi
  local got
  got="$(cat "$CMUX_STUB_LOG")"
  if [ "$got" != "$want" ]; then
    echo "FAIL $name"
    echo "  want: $want"
    echo "  got:  $got"
    fail=$((fail + 1))
    return
  fi
  pass=$((pass + 1))
}

# assert_fail <name> <grep> -- <script args...>
assert_fail() {
  local name="$1" grepfor="$2"; shift 3
  : >"$CMUX_STUB_LOG"
  local out rc=0
  out="$("$script" "$@" 2>&1)" || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "FAIL $name: expected non-zero exit"
    fail=$((fail + 1))
    return
  fi
  if [ -n "$(cat "$CMUX_STUB_LOG")" ]; then
    echo "FAIL $name: messaged despite resolving nothing: $(cat "$CMUX_STUB_LOG")"
    fail=$((fail + 1))
    return
  fi
  case "$out" in
    *"$grepfor"*) pass=$((pass + 1)) ;;
    *) echo "FAIL $name: stderr lacks '$grepfor': $out"; fail=$((fail + 1)) ;;
  esac
}

assert_run "explicit surface ref" \
  "agent message surface:999 -- hello world" \
  -- surface:999 hello world

assert_run "explicit workspace ref" \
  "agent message workspace:52 -- hi" \
  -- workspace:52 hi

assert_run "tab title resolves to surface" \
  "agent message surface:463 -- ping" \
  -- builder-t-demo ping

assert_run "workspace title falls back" \
  "agent message workspace:52 -- ping" \
  -- t-demo ping

assert_run "workspace title with regex metachars stays literal" \
  "agent message workspace:9 -- ping" \
  -- 'a[1]' ping

export CMUX_MSG_FROM=builder-t-demo
assert_run "CMUX_MSG_FROM becomes --from" \
  "agent message surface:463 --from builder-t-demo -- ping" \
  -- builder-t-demo ping
unset CMUX_MSG_FROM

assert_fail "unknown target errors" \
  "no surface/workspace named" \
  -- nobody-here ping

echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
