#!/usr/bin/env bash
# Contract test for scripts/smoke-web.sh's port selection. Parallel smoke and QA
# runs must not share one fixed port (during QA t-7772hpkep3 an orphaned
# `ft serve` on 18484 flaked a sibling's SSE assertion), while
# FACTOTUM_SMOKE_PORT must keep pinning a port for anyone who needs a
# predictable one.
#
# Run: bash scripts/smoke-web_test.sh
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="$here/smoke-web.sh"

pass=0
fail=0
ok()  { pass=$((pass + 1)); }
bad() { echo "FAIL $1"; fail=$((fail + 1)); }

# check <name> <want> <got>
check() {
  if [ "$2" = "$3" ]; then ok; else bad "$1: got '$3', want '$2'"; fi
}

# The port helpers are the unit under test. Sourcing stops at the script's
# source guard, so nothing is built, served, or fetched.
# shellcheck source=scripts/smoke-web.sh
. "$script"

check "no env asks the kernel for a free port" "127.0.0.1:0" \
  "$(unset FACTOTUM_SMOKE_PORT; smoke_bind)"
check "an empty env still asks for a free port" "127.0.0.1:0" \
  "$(FACTOTUM_SMOKE_PORT= smoke_bind)"
check "a pinned port wins" "127.0.0.1:18999" \
  "$(FACTOTUM_SMOKE_PORT=18999 smoke_bind)"

# Two default runs each ask for a kernel-assigned port, so they cannot collide;
# each then reads its base URL back from the port serve actually announced.
log_a="$(mktemp)"
log_b="$(mktemp)"
trap 'rm -f "$log_a" "$log_b"' EXIT
printf 'factotum dashboard: http://127.0.0.1:53121 (reads live; capture on at /capture; Ctrl-C to stop)\n' >"$log_a"
printf 'factotum dashboard: http://127.0.0.1:53122 (reads live; capture on at /capture; Ctrl-C to stop)\n' >"$log_b"
check "concurrent run A reads back its own port" "53121" "$(smoke_announced_port "$log_a")"
check "concurrent run B reads back its own port" "53122" "$(smoke_announced_port "$log_b")"
check "an unannounced address yields no port" "" "$(smoke_announced_port /dev/null)"

echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
