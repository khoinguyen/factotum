#!/usr/bin/env bash
# Tests for cmux-layout.sh. Hermetic: a stub `cmux` on PATH records the argv it
# is called with and serves a fixed tree, so no real workspace, pane, or surface
# is ever touched.
#
# Run: bash .agents/skills/chief/scripts/cmux-layout_test.sh
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="$here/cmux-layout.sh"

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
if [ "${1:-}" = new-workspace ]; then
  printf '%s\n' 'workspace:52'
elif [ "${1:-}" = new-split ]; then
  printf '%s\n' 'surface:464'
elif [ "${1:-}" = new-pane ]; then
  printf '%s\n' 'pane:99'
fi
STUB
chmod +x "$tmp/bin/cmux"
export PATH="$tmp/bin:$PATH"

fail=0
pass=0

# assert_run <name> <expected-log> -- <script args...>
assert_run() {
  local name="$1" want="$2"; shift 3
  : >"$CMUX_STUB_LOG"
  local out rc=0
  out="$("$script" "$@" 2>&1)" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "FAIL $name: script exited $rc: $out"
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

cat >"$CMUX_STUB_TREE" <<'TREE'
window window:1 [current]
├── workspace workspace:10 "chief"
│   └── pane pane:21
│       ├── surface surface:37 [terminal] "chief" [selected]
│       └── surface surface:38 [terminal] "ft serve"
├── workspace workspace:52 "t-demo"
│   └── pane pane:262
│       └── surface surface:463 [terminal] "builder-t-demo" [selected]
└── workspace workspace:7 "other"
    └── pane pane:8
        └── surface surface:8 [terminal] "x" [selected]
TREE

# dashboard creates a browser pane to the right of the chief when none exists.
assert_run "dashboard creates the browser pane" \
  "new-pane --type browser --direction right --workspace workspace:10 --url https://dash" \
  -- dashboard workspace:10 https://dash

# dashboard is idempotent: an existing browser pane in the chief workspace is left alone.
cat >"$CMUX_STUB_TREE" <<'TREE'
window window:1 [current]
└── workspace workspace:10 "chief"
    ├── pane pane:21
    │   └── surface surface:37 [terminal] "chief" [selected]
    └── pane pane:22
        └── surface surface:39 [browser] "Factotum Dashboard"
TREE
: >"$CMUX_STUB_LOG"
rc=0
out="$("$script" dashboard workspace:10 https://dash 2>&1)" || rc=$?
if [ "$rc" -ne 0 ]; then
  echo "FAIL dashboard idempotent: exited $rc: $out"
  fail=$((fail + 1))
elif [ -n "$(cat "$CMUX_STUB_LOG")" ]; then
  echo "FAIL dashboard idempotent: created a duplicate pane: $(cat "$CMUX_STUB_LOG")"
  fail=$((fail + 1))
elif ! printf '%s' "$out" | grep -q 'already'; then
  echo "FAIL dashboard idempotent: no 'already' note: $out"
  fail=$((fail + 1))
else
  pass=$((pass + 1))
fi

# pair creates the task workspace in the group/window, splits the reviewer right,
# and names both tabs. The builder surface is the first in the new workspace.
cat >"$CMUX_STUB_TREE" <<'TREE'
window window:1 [current]
├── workspace workspace:10 "chief"
│   └── pane pane:21
│       └── surface surface:37 [terminal] "chief" [selected]
├── workspace workspace:52 "t-demo"
│   └── pane pane:262
│       └── surface surface:463 [terminal] "builder-t-demo" [selected]
└── workspace workspace:7 "other"
    └── pane pane:8
        └── surface surface:8 [terminal] "x" [selected]
TREE
assert_run "pair lays out builder left, reviewer right" \
  "new-workspace --name t-demo --command BUILDER --window window:1 --group workspace_group:7 --group-placement end
new-split right --workspace workspace:52 --surface surface:463 --command REVIEWER
rename-tab --workspace workspace:52 --surface surface:463 builder-t-demo
rename-tab --workspace workspace:52 --surface surface:464 reviewer-t-demo" \
  -- pair t-demo workspace_group:7 window:1 BUILDER REVIEWER

echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
