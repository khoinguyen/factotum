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
trap 'rm -rf "${interrupt_dir:-}"; rm -f "$log_a" "$log_b"' EXIT
printf 'factotum dashboard: http://127.0.0.1:53121 (reads live; capture on via the dashboard Capture button; /capture fallback; Ctrl-C to stop)\n' >"$log_a"
printf 'factotum dashboard: http://127.0.0.1:53122 (reads live; capture on via the dashboard Capture button; /capture fallback; Ctrl-C to stop)\n' >"$log_b"
check "concurrent run A reads back its own port" "53121" "$(smoke_announced_port "$log_a")"
check "concurrent run B reads back its own port" "53122" "$(smoke_announced_port "$log_b")"
check "an unannounced address yields no port" "" "$(smoke_announced_port /dev/null)"

# A Ctrl-C or CI TERM must stop the run, not clean up and carry on. The old
# `trap cleanup EXIT INT TERM` ran the handler, tore down the server and the
# throwaway database, and then resumed the loop it interrupted - QA saw the run
# continue against the removed store and fail with a misleading rc=3 'project
# acme: not found' instead of reporting the interruption. The copy below runs
# under a temp root with a stub `ft`/`curl`, so nothing is built or served, and
# the stub `serve` announces a port and then sleeps so the run is inside its
# startup loop when the signal arrives.
interrupt_dir="$(mktemp -d)"
mkdir -p "$interrupt_dir/scripts" "$interrupt_dir/bin" "$interrupt_dir/tmp"
cp "$script" "$interrupt_dir/scripts/smoke-web.sh"
cat >"$interrupt_dir/bin/ft" <<'STUB'
#!/usr/bin/env bash
printf 'ft %s\n' "$*" >>"$STUB_LOG"
case "$*" in
  *serve*)
    printf 'factotum dashboard: http://127.0.0.1:53121 (reads live; capture on via the dashboard Capture button; /capture fallback; Ctrl-C to stop)\n'
    printf '%s\n' "$$" >"$STUB_SERVER_PID"
    exec sleep 300
    ;;
esac
exit 0
STUB
cat >"$interrupt_dir/bin/curl" <<'STUB'
#!/usr/bin/env bash
printf 'curl %s\n' "$*" >>"$STUB_LOG"
exit 1
STUB
chmod +x "$interrupt_dir/bin/ft" "$interrupt_dir/bin/curl"
: >"$interrupt_dir/stub.log"
PATH="$interrupt_dir/bin:$PATH" TMPDIR="$interrupt_dir/tmp" \
  STUB_LOG="$interrupt_dir/stub.log" STUB_SERVER_PID="$interrupt_dir/server.pid" \
  bash "$interrupt_dir/scripts/smoke-web.sh" >"$interrupt_dir/out" 2>&1 &
interrupt_pid=$!
interrupt_started=""
deadline=$((SECONDS + 5))
while [ "$SECONDS" -lt "$deadline" ]; do
  if grep -q 'serve' "$interrupt_dir/stub.log" 2>/dev/null; then break; fi
  sleep 0.1
done
if grep -q 'serve' "$interrupt_dir/stub.log" 2>/dev/null; then
  interrupt_started=$SECONDS
  kill -TERM "$interrupt_pid" 2>/dev/null || true
fi
deadline=$((SECONDS + 10))
while kill -0 "$interrupt_pid" 2>/dev/null && [ "$SECONDS" -lt "$deadline" ]; do sleep 0.1; done
if [ -z "$interrupt_started" ]; then
  kill -9 "$interrupt_pid" 2>/dev/null || true
  wait "$interrupt_pid" 2>/dev/null || true
  bad "a TERM test never reached the serve loop: $(tr '\n' ';' <"$interrupt_dir/stub.log")"
elif kill -0 "$interrupt_pid" 2>/dev/null; then
  kill -9 "$interrupt_pid" 2>/dev/null || true
  wait "$interrupt_pid" 2>/dev/null || true
  bad "a TERM did not stop the run (still running 10s later)"
else
  interrupt_rc=0
  wait "$interrupt_pid" || interrupt_rc=$?
  check "a TERM exits 143" 143 "$interrupt_rc"
  interrupt_secs=$((SECONDS - interrupt_started))
  if [ "$interrupt_secs" -le 3 ]; then ok; else bad "a TERM stopped the run only after ${interrupt_secs}s"; fi
  case "$(cat "$interrupt_dir/out")" in
    *interrupted*SIGTERM*) ok ;;
    *) bad "an interrupted run did not name the interruption: $(tr '\n' ';' <"$interrupt_dir/out")" ;;
  esac
  if grep -q 'Second smoke task' "$interrupt_dir/stub.log"; then
    bad "an interrupted run resumed and mutated the store cleanup removed"
  else
    ok
  fi
  if kill -0 "$(cat "$interrupt_dir/server.pid" 2>/dev/null)" 2>/dev/null; then
    bad "an interrupted run left its server running"
  else
    ok
  fi
  if [ -n "$(ls -A "$interrupt_dir/tmp" 2>/dev/null)" ]; then
    bad "an interrupted run left its scratch dir behind"
  else
    ok
  fi
fi

echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
