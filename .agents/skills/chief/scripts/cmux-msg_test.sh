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
  exit 0
fi
printf '%s\n' "$*" >>"$CMUX_STUB_LOG"
if [ "${1:-}" = agent ] && [ "${2:-}" = message ]; then
  if [ "${CMUX_STUB_HAS_AGENT:-true}" = "false" ]; then
    printf '%s\n' '{"recipient_has_agent" : false, "recipient_surface_ref" : "surface:463"}'
  else
    printf '%s\n' '{"recipient_has_agent" : true, "recipient_surface_ref" : "surface:463"}'
  fi
elif [ "${1:-}" = paste ]; then
  if [ -n "${CMUX_STUB_PASTE_WARN:-}" ]; then
    printf '%s\n' "$CMUX_STUB_PASTE_WARN"
  else
    printf 'OK %s %s\n' 11111111-1111-1111-1111-111111111111 22222222-2222-2222-2222-222222222222
  fi
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
  "agent message surface:999 --json -- hello world" \
  -- surface:999 hello world

assert_run "explicit workspace ref" \
  "agent message workspace:52 --json -- hi" \
  -- workspace:52 hi

assert_run "tab title resolves to surface" \
  "agent message surface:463 --json -- ping" \
  -- builder-t-demo ping

assert_run "workspace title falls back" \
  "agent message workspace:52 --json -- ping" \
  -- t-demo ping

assert_run "workspace title with regex metachars stays literal" \
  "agent message workspace:9 --json -- ping" \
  -- 'a[1]' ping

export CMUX_STUB_HAS_AGENT=false
assert_run "no recipient agent falls back to paste" \
  "agent message workspace:52 --json -- ping
paste --surface surface:463 --submit -- ping
agent inbox --surface surface:463 --state queued --mark-read" \
  -- t-demo ping

: >"$CMUX_STUB_LOG"
if out="$("$script" t-demo ping 2>&1)" && ! printf '%s' "$out" | grep -q 'OK '; then
  pass=$((pass + 1))
else
  echo "FAIL fallback stdout hides paste confirmation: $out"
  fail=$((fail + 1))
fi

export CMUX_STUB_PASTE_WARN="warning: text was pasted but the submit key was not sent (surface_changed)"
: >"$CMUX_STUB_LOG"
rc=0
out="$("$script" t-demo ping 2>&1)" || rc=$?
if [ "$rc" -eq 0 ]; then
  echo "FAIL failed submit exits non-zero"
  fail=$((fail + 1))
elif ! printf '%s' "$out" | grep -q 'submit key was not sent'; then
  echo "FAIL failed submit not surfaced: $out"
  fail=$((fail + 1))
elif grep -q 'agent inbox' "$CMUX_STUB_LOG"; then
  echo "FAIL failed submit still marked the queued copy read"
  fail=$((fail + 1))
else
  pass=$((pass + 1))
fi
unset CMUX_STUB_PASTE_WARN
unset CMUX_STUB_HAS_AGENT

export CMUX_MSG_FROM=builder-t-demo
assert_run "CMUX_MSG_FROM becomes --from" \
  "agent message surface:463 --from builder-t-demo --json -- ping" \
  -- builder-t-demo ping
unset CMUX_MSG_FROM

assert_fail "unknown target errors" \
  "no surface/workspace named" \
  -- nobody-here ping

echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
