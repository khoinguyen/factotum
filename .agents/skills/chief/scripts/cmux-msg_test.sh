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
  target="${3:-}"
  case "$target" in
    surface:*) surf="$target" ;;
    workspace:52) surf=surface:463 ;;
    workspace:61) surf=surface:461 ;;
    workspace:71) surf=surface:471 ;;
    *) surf="$target" ;;
  esac
  no_agent=false
  for t in ${CMUX_STUB_NO_AGENT:-}; do
    [ "$t" = "$target" ] && no_agent=true
  done
  if [ "$no_agent" = true ] || [ "${CMUX_STUB_HAS_AGENT:-true}" = "false" ]; then
    has=false
  else
    has=true
  fi
  if [ "${CMUX_STUB_NO_SURFACE_REF:-}" = "true" ]; then
    printf '{"recipient_has_agent" : %s}\n' "$has"
  elif [ "${CMUX_STUB_AGENT_LAST:-}" = "true" ]; then
    printf '{"recipient_surface_ref" : "%s", "recipient_has_agent" : %s}\n' "$surf" "$has"
  elif [ "$has" = "false" ]; then
    printf '{"recipient_has_agent" : false, "recipient_surface_ref" : "%s"}\n' "$surf"
  else
    printf '{"recipient_has_agent" : true, "recipient_surface_ref" : "%s"}\n' "$surf"
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

# assert_log <name> <expected-call-log> -- <script args...>
assert_log() {
  local name="$1" want="$2"; shift 3
  : >"$CMUX_STUB_LOG"
  local out
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

# assert_fail_log <name> <grep> <expected-call-log> -- <script args...>
# A failing resolution may probe candidates (and mark the stale copies read), but
# it must fail loudly, never paste, and never leak a raw column separator.
assert_fail_log() {
  local name="$1" grepfor="$2" want="$3"; shift 4
  : >"$CMUX_STUB_LOG"
  local out rc=0
  out="$("$script" "$@" 2>&1)" || rc=$?
  local got
  got="$(cat "$CMUX_STUB_LOG")"
  if [ "$rc" -eq 0 ]; then
    echo "FAIL $name: expected non-zero exit"
    fail=$((fail + 1))
    return
  fi
  case "$got" in
    *paste*) echo "FAIL $name: pasted into a surface: $got"; fail=$((fail + 1)); return ;;
  esac
  case "$out" in
    *$'\t'*) echo "FAIL $name: error output leaks a raw column separator: $out"; fail=$((fail + 1)); return ;;
  esac
  if [ "$got" != "$want" ]; then
    echo "FAIL $name"
    echo "  want: $want"
    echo "  got:  $got"
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

# A restored session can leave two surfaces with the same title: the stale one
# in an old window, the live pair in the current window. Prefer the current
# window instead of first-match-wins.
cat >"$CMUX_STUB_TREE" <<'TREE'
window window:1
├── workspace workspace:10 "restored"
│   └── pane pane:21
│       └── surface surface:37 [terminal] "builder-t-demo" [selected]
window window:2 [current] ◀ active
├── workspace workspace:20 "live" [selected] ◀ active
│   └── pane pane:22 [focused] ◀ active
│       └── surface surface:38 [terminal] "builder-t-demo" [selected] ◀ active
TREE

assert_run "duplicate titles prefer the current window" \
  "agent message surface:38 --json -- ping" \
  -- builder-t-demo ping

# The current window can still hold two same-titled surfaces (two panes, no
# active surface). Rank is tied, so the recipient's agent decides: probe until
# one reports a hook-capable agent, and never paste into the bare one.
cat >"$CMUX_STUB_TREE" <<'TREE'
window window:1 [current] ◀ active
├── workspace workspace:20 "live" [selected] ◀ active
│   ├── pane pane:21 [focused] ◀ active
│   │   └── surface surface:37 [terminal] "builder-t-demo" [selected]
│   └── pane pane:22
│       └── surface surface:38 [terminal] "builder-t-demo" [selected]
TREE

export CMUX_STUB_NO_AGENT="surface:37"
assert_log "tied duplicates pick the surface with a live agent" \
"agent message surface:37 --json -- ping
agent inbox --surface surface:37 --state queued --mark-read
agent message surface:38 --json -- ping" \
  -- builder-t-demo ping
unset CMUX_STUB_NO_AGENT

# When the ranking points at a bare surface but a lower-ranked one has the
# agent, the agent still wins; a bare shell is never pasted into.
cat >"$CMUX_STUB_TREE" <<'TREE'
window window:1
├── workspace workspace:10 "restored"
│   └── pane pane:21
│       └── surface surface:37 [terminal] "builder-t-demo" [selected]
window window:2 [current] ◀ active
├── workspace workspace:20 "live" [selected] ◀ active
│   └── pane pane:22 [focused] ◀ active
│       └── surface surface:38 [terminal] "builder-t-demo" [selected] ◀ active
TREE

export CMUX_STUB_NO_AGENT="surface:38"
assert_log "a lower-ranked surface with the agent still wins" \
"agent message surface:38 --json -- ping
agent inbox --surface surface:38 --state queued --mark-read
agent message surface:37 --json -- ping" \
  -- builder-t-demo ping
unset CMUX_STUB_NO_AGENT

# Ambiguity with no live agent anywhere fails loudly instead of first-match-wins
# (and never pastes into a bare shell).
cat >"$CMUX_STUB_TREE" <<'TREE'
window window:1 [current] ◀ active
├── workspace workspace:20 "live" [selected] ◀ active
│   ├── pane pane:21 [focused] ◀ active
│   │   └── surface surface:37 [terminal] "builder-t-demo" [selected]
│   └── pane pane:22
│       └── surface surface:38 [terminal] "builder-t-demo" [selected]
TREE

export CMUX_STUB_NO_AGENT="surface:37 surface:38"
assert_fail_log "ambiguous titles with no live agent fail loudly" \
  "ambiguous target 'builder-t-demo'" \
"agent message surface:37 --json -- ping
agent inbox --surface surface:37 --state queued --mark-read
agent message surface:38 --json -- ping
agent inbox --surface surface:38 --state queued --mark-read" \
  -- builder-t-demo ping
unset CMUX_STUB_NO_AGENT

# Duplicate workspace titles follow the same rule.
cat >"$CMUX_STUB_TREE" <<'TREE'
window window:1
└── workspace workspace:61 "chief"
    └── pane pane:21
        └── surface surface:461 [terminal] "restored bare" [selected]
window window:2 [current] ◀ active
└── workspace workspace:71 "chief" [selected] ◀ active
    └── pane pane:22 [focused] ◀ active
        └── surface surface:471 [terminal] "ft serve" [selected] ◀ active
TREE

assert_run "duplicate workspace titles prefer the current window" \
  "agent message workspace:71 --json -- ping" \
  -- chief ping

cat >"$CMUX_STUB_TREE" <<'TREE'
window window:1
├── workspace workspace:61 "chief"
│   └── pane pane:21
│       └── surface surface:461 [terminal] "restored bare" [selected]
└── workspace workspace:71 "chief"
    └── pane pane:22 [focused]
        └── surface surface:471 [terminal] "ft serve" [selected]
TREE

export CMUX_STUB_NO_AGENT="workspace:61 workspace:71"
assert_fail_log "ambiguous workspace titles with no live agent fail loudly" \
  "ambiguous target 'chief'" \
"agent message workspace:61 --json -- ping
agent inbox --surface surface:461 --state queued --mark-read
agent message workspace:71 --json -- ping
agent inbox --surface surface:471 --state queued --mark-read" \
  -- chief ping
unset CMUX_STUB_NO_AGENT

# The incident shape: duplicate workspace titles where only the stale copy has a
# tab titled the same as the workspace. A lone surface match must not short-circuit
# to the paste fallback while a same-titled workspace elsewhere holds the agent.
cat >"$CMUX_STUB_TREE" <<'TREE'
window window:1
└── workspace workspace:61 "chief"
    └── pane pane:21
        └── surface surface:461 [terminal] "chief" [selected]
window window:2 [current] ◀ active
└── workspace workspace:71 "chief" [selected] ◀ active
    └── pane pane:22 [focused] ◀ active
        └── surface surface:471 [terminal] "ft serve" [selected] ◀ active
TREE

export CMUX_STUB_NO_AGENT="surface:461"
assert_run "a lone stale surface does not hide the live workspace" \
  "agent message workspace:71 --json -- ping" \
  -- chief ping
unset CMUX_STUB_NO_AGENT

# A lone top-ranked bare surface plus a same-titled bare workspace is still
# ambiguous: probe both and fail loudly, never paste.
cat >"$CMUX_STUB_TREE" <<'TREE'
window window:1
└── workspace workspace:61 "chief"
    └── pane pane:21
        └── surface surface:461 [terminal] "restored bare" [selected]
window window:2 [current] ◀ active
└── workspace workspace:20 "live" [selected] ◀ active
    └── pane pane:22 [focused] ◀ active
        └── surface surface:38 [terminal] "chief" [selected] ◀ active
TREE

export CMUX_STUB_NO_AGENT="surface:38 workspace:61"
assert_fail_log "a bare surface plus a bare same-titled workspace fails loudly" \
  "ambiguous target 'chief'" \
"agent message surface:38 --json -- ping
agent inbox --surface surface:38 --state queued --mark-read
agent message workspace:61 --json -- ping
agent inbox --surface surface:461 --state queued --mark-read" \
  -- chief ping
unset CMUX_STUB_NO_AGENT

# A surface and its OWN same-titled workspace are one match, not an ambiguity:
# the single-match paste fallback must survive.
cat >"$CMUX_STUB_TREE" <<'TREE'
window window:1 [current] ◀ active
└── workspace workspace:10 "chief" [selected] ◀ active
    └── pane pane:21 [focused] ◀ active
        └── surface surface:37 [terminal] "chief" [selected] ◀ active
TREE

export CMUX_STUB_HAS_AGENT=false
assert_log "a surface and its own same-titled workspace keep the paste fallback" \
"agent message surface:37 --json -- ping
paste --surface surface:37 --submit -- ping
agent inbox --surface surface:37 --state queued --mark-read" \
  -- chief ping
unset CMUX_STUB_HAS_AGENT

# Several stale surfaces plus a live same-titled workspace whose tab has another
# title: the workspace is a candidate too, so the live agent still wins.
cat >"$CMUX_STUB_TREE" <<'TREE'
window window:1
├── workspace workspace:61 "chief"
│   └── pane pane:21
│       └── surface surface:461 [terminal] "chief" [selected]
└── workspace workspace:62 "chief"
    └── pane pane:22
        └── surface surface:462 [terminal] "chief" [selected]
window window:2 [current] ◀ active
└── workspace workspace:71 "chief" [selected] ◀ active
    └── pane pane:23 [focused] ◀ active
        └── surface surface:471 [terminal] "ft serve" [selected] ◀ active
TREE

assert_run "a live workspace joins several same-titled surfaces" \
  "agent message workspace:71 --json -- ping" \
  -- chief ping

# cmux may serialize recipient_has_agent last, so its value must not swallow the
# trailing brace: "false}" would read as a live agent and skip the probe.
cat >"$CMUX_STUB_TREE" <<'TREE'
window window:1 [current] ◀ active
└── workspace workspace:20 "live" [selected] ◀ active
    ├── pane pane:21 [focused] ◀ active
    │   └── surface surface:37 [terminal] "builder-t-demo" [selected]
    └── pane pane:22
        └── surface surface:38 [terminal] "builder-t-demo" [selected]
TREE

export CMUX_STUB_AGENT_LAST=true
export CMUX_STUB_NO_AGENT="surface:37"
assert_log "a bare candidate with has_agent last still probes on" \
"agent message surface:37 --json -- ping
agent inbox --surface surface:37 --state queued --mark-read
agent message surface:38 --json -- ping" \
  -- builder-t-demo ping

# ... and a unique bare surface must still take the paste fallback, not report a
# false success.
cat >"$CMUX_STUB_TREE" <<'TREE'
window window:1 [current] ◀ active
└── workspace workspace:20 "live" [selected] ◀ active
    └── pane pane:21 [focused] ◀ active
        └── surface surface:37 [terminal] "builder-t-demo" [selected] ◀ active
TREE

export CMUX_STUB_HAS_AGENT=false
assert_log "a unique bare surface with has_agent last still pastes" \
"agent message surface:37 --json -- ping
paste --surface surface:37 --submit -- ping
agent inbox --surface surface:37 --state queued --mark-read" \
  -- builder-t-demo ping
unset CMUX_STUB_HAS_AGENT
unset CMUX_STUB_AGENT_LAST

# A workspace with no recipient_surface_ref has nowhere to paste; the "-"
# sentinel must not become `paste --surface -`.
cat >"$CMUX_STUB_TREE" <<'TREE'
window window:1 [current] ◀ active
└── workspace workspace:52 "solo" [selected] ◀ active
    └── pane pane:21
        └── surface surface:99 [terminal] "shell" [selected]
TREE

export CMUX_STUB_NO_SURFACE_REF=true
export CMUX_STUB_AGENT_LAST=true
export CMUX_STUB_NO_AGENT="workspace:52"
assert_fail_log "a workspace with no surface to paste into fails loudly" \
  "no surface to paste into" \
"agent message workspace:52 --json -- ping" \
  -- solo ping
unset CMUX_STUB_NO_AGENT
unset CMUX_STUB_AGENT_LAST
unset CMUX_STUB_NO_SURFACE_REF

echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
