#!/usr/bin/env bash
# Tests for loop-agent.sh. Hermetic: stub `ft` and `opencode` on PATH record the
# argv/env they are called with, so no real receiver is installed and no agent
# is ever started.
#
# Run: bash .agents/skills/chief/scripts/loop-agent_test.sh
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="$here/loop-agent.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# Hermetic: a real loop agent's env carries FACTOTUM_HARNESS and, as the very
# bug this tests, may set OPENCODE_BIN to a --standalone wrapper. Either would
# mask a regression, so clear them; the script must supply both itself. FT_BIN
# and the FACTOTUM_* identity vars are cleared for the same reason.
unset FT_BIN OPENCODE_BIN FACTOTUM_HARNESS FACTOTUM_SESSION \
  FACTOTUM_PROJECT FACTOTUM_ACTOR FACTOTUM_TASK_ID FACTOTUM_DEFAULT_ACTOR

export FT_STUB_LOG="$tmp/ft.log"
export OPENCODE_STUB_LOG="$tmp/opencode.log"
export OPENCODE_PROBE_LOG="$tmp/opencode-probe.log"
: >"$FT_STUB_LOG"
: >"$OPENCODE_STUB_LOG"
: >"$OPENCODE_PROBE_LOG"

mkdir -p "$tmp/bin" "$tmp/worktree"
cat >"$tmp/bin/ft" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$FT_STUB_LOG"
if [ "${1:-}" = "session" ] && [ "${2:-}" = "start" ]; then
  printf 'session_id: sid-test\n'
fi
exit 0
STUB
# The opencode stub answers the two probes the directory guard makes
# (`service status`, and `api --server <url> debug.location.list`) from
# $SERVICE_STATUS_FILE / $LOCATIONS_FILE, and logs them to a separate probe log
# so a test can assert a refused launch never reached the TUI launch path.
cat >"$tmp/bin/opencode" <<'STUB'
#!/usr/bin/env bash
case "${1:-}" in
  service)
    printf '%s\n' "$*" >>"$OPENCODE_PROBE_LOG"
    if [ -n "${SERVICE_STATUS_FILE:-}" ]; then cat "$SERVICE_STATUS_FILE"; fi
    exit 0
    ;;
  api)
    printf '%s\n' "$*" >>"$OPENCODE_PROBE_LOG"
    if [ -n "${LOCATIONS_FILE:-}" ]; then cat "$LOCATIONS_FILE"; fi
    exit "${API_EXIT:-0}"
    ;;
esac
{
  printf 'cwd=%s\n' "$PWD"
  printf 'project=%s actor=%s task=%s default=%s session=%s harness=%s\n' \
    "${FACTOTUM_PROJECT:-}" "${FACTOTUM_ACTOR:-}" "${FACTOTUM_TASK_ID:-}" "${FACTOTUM_DEFAULT_ACTOR:-}" "${FACTOTUM_SESSION:-}" "${FACTOTUM_HARNESS:-}"
  printf 'argv=%s\n' "$*"
} >>"$OPENCODE_STUB_LOG"
exit 0
STUB
chmod +x "$tmp/bin/ft" "$tmp/bin/opencode"
export PATH="$tmp/bin:$PATH"

fail=0
pass=0

# Task-scoped role: installs the receiver, exports actor+task env, runs in the
# worktree, and forwards the prompt.
: >"$FT_STUB_LOG"; : >"$OPENCODE_STUB_LOG"
"$script" builder-t-1 factotum t-1 "$tmp/worktree" "you are builder-t-1" >/dev/null
if grep -q "msg install --harness opencode2" "$FT_STUB_LOG"; then pass=$((pass+1)); else
  echo "FAIL builder: did not install the V2 receiver: $(cat "$FT_STUB_LOG")"; fail=$((fail+1)); fi
if grep -q "cwd=$tmp/worktree" "$OPENCODE_STUB_LOG" \
  && grep -q "project=factotum actor=builder-t-1 task=t-1 default=builder-t-1" "$OPENCODE_STUB_LOG" \
  && grep -q "argv=--standalone --prompt you are builder-t-1 --auto" "$OPENCODE_STUB_LOG"; then pass=$((pass+1)); else
  echo "FAIL builder: wrong agent launch: $(cat "$OPENCODE_STUB_LOG")"; fail=$((fail+1)); fi

# OpenCode V2 runs a shared background service that owns plugin loading; its
# process env carries no FACTOTUM_*, so a plugin loaded there is inert for every
# agent. Each agent must instead run on a PRIVATE server (--standalone) that
# inherits its env, and register as the opencode2 harness so the claim metadata
# is right.
if grep -q "harness=opencode2" "$OPENCODE_STUB_LOG"; then pass=$((pass+1)); else
  echo "FAIL builder: FACTOTUM_HARNESS should be opencode2: $(cat "$OPENCODE_STUB_LOG")"; fail=$((fail+1)); fi
if grep -q -- "--standalone" "$OPENCODE_STUB_LOG"; then pass=$((pass+1)); else
  echo "FAIL builder: launch must use a private server (--standalone): $(cat "$OPENCODE_STUB_LOG")"; fail=$((fail+1)); fi

# The chief has no task: FACTOTUM_TASK_ID must not leak in. A chief launch also
# mints a session id, records its start, and exports FACTOTUM_SESSION.
: >"$FT_STUB_LOG"; : >"$OPENCODE_STUB_LOG"
"$script" chief factotum - "$tmp/worktree" "you are chief" >/dev/null
if grep -q "project=factotum actor=chief task= default=chief session=sid-test" "$OPENCODE_STUB_LOG"; then pass=$((pass+1)); else
  echo "FAIL chief: task should be unset and session exported: $(cat "$OPENCODE_STUB_LOG")"; fail=$((fail+1)); fi
if grep -q "session start --project factotum" "$FT_STUB_LOG"; then pass=$((pass+1)); else
  echo "FAIL chief: did not record the session start: $(cat "$FT_STUB_LOG")"; fail=$((fail+1)); fi

# A subagent inherits the chief's session from the environment.
: >"$FT_STUB_LOG"; : >"$OPENCODE_STUB_LOG"
FACTOTUM_SESSION=sid-inherit "$script" builder-t-2 factotum t-2 "$tmp/worktree" "you are builder-t-2" >/dev/null
if grep -q "actor=builder-t-2 task=t-2 default=builder-t-2 session=sid-inherit" "$OPENCODE_STUB_LOG"; then pass=$((pass+1)); else
  echo "FAIL builder: did not inherit FACTOTUM_SESSION: $(cat "$OPENCODE_STUB_LOG")"; fail=$((fail+1)); fi
if grep -q "session start" "$FT_STUB_LOG"; then
  echo "FAIL builder: should not start its own session: $(cat "$FT_STUB_LOG")"; fail=$((fail+1));
else pass=$((pass+1)); fi

# Missing args and a bad worktree fail loudly without launching anything.
: >"$OPENCODE_STUB_LOG"
if "$script" builder-t-1 factotum >/dev/null 2>&1; then
  echo "FAIL missing args should fail"; fail=$((fail+1));
elif [ -s "$OPENCODE_STUB_LOG" ]; then
  echo "FAIL missing args launched the agent"; fail=$((fail+1));
else pass=$((pass+1)); fi

if "$script" builder-t-1 factotum t-1 "$tmp/nope" "x" >/dev/null 2>&1; then
  echo "FAIL bad worktree should fail"; fail=$((fail+1));
elif [ -s "$OPENCODE_STUB_LOG" ]; then
  echo "FAIL bad worktree launched the agent"; fail=$((fail+1));
else pass=$((pass+1)); fi

# --- Directory note ---------------------------------------------------------
# OpenCode attaches a TUI started in a directory that already hosts one as a tab
# of that instance, and the tab inherits that instance's env: a *plain* second
# launch there leaves its messages queued (reviewer control on 2.3.0). This
# script always launches --standalone, and a standalone TUI keeps its own server
# and its own env even in such a directory, so the launch still registers. That
# makes a hosted directory worth reporting, never worth blocking: a false
# refusal would stall every agent in the loop, and the service also keeps
# locations for directories where nothing runs any more.

target="$(cd "$tmp/worktree" && pwd -P)"
ln -s "$tmp/worktree" "$tmp/link"
printf 'stopped\n' >"$tmp/status-stopped"
printf 'http://127.0.0.1:49374\n' >"$tmp/status-live"
printf '[{"directory":"%s"}]\n' "$target" >"$tmp/loc-self"
printf '[{"directory":"%s"},{"directory":"/somewhere/else"}]' "$tmp" >"$tmp/loc-other"
printf '[{"directory":"%s"}]\n' "$tmp/link" >"$tmp/loc-symlinked"
printf 'not json at all\n' >"$tmp/loc-garbage"
printf 'Error: could not reach server\n' >"$tmp/status-broken"

# run_guard <status-file> <locations-file> [NAME=VALUE...]
# Runs the script in the worktree and leaves rc / out / logs for the assertions.
run_guard() {
  local status="$1" locations="$2"; shift 2
  : >"$FT_STUB_LOG"; : >"$OPENCODE_STUB_LOG"; : >"$OPENCODE_PROBE_LOG"
  set +e
  out="$(SERVICE_STATUS_FILE="$status" LOCATIONS_FILE="$locations" env "$@" \
    "$script" builder-t-9 factotum t-9 "$tmp/worktree" "you are builder-t-9" 2>&1)"
  rc=$?
  set -e
}

# No instance: the service is not running, so there is nothing to report and the
# launch goes ahead without even asking for locations.
run_guard "$tmp/status-stopped" "$tmp/loc-self"
if [ "$rc" -eq 0 ] && [ -s "$OPENCODE_STUB_LOG" ] && [ -z "$out" ]; then pass=$((pass+1)); else
  echo "FAIL note: stopped service should launch quietly (rc=$rc): $out"; fail=$((fail+1)); fi
if grep -q "debug.location.list" "$OPENCODE_PROBE_LOG"; then
  echo "FAIL note: should not list locations when the service is stopped: $(cat "$OPENCODE_PROBE_LOG")"; fail=$((fail+1));
else pass=$((pass+1)); fi

# A live service that owns *other* directories is the normal loop case: launch,
# stage the receiver, and say nothing.
run_guard "$tmp/status-live" "$tmp/loc-other"
if [ "$rc" -eq 0 ] && [ -s "$OPENCODE_STUB_LOG" ] && [ -z "$out" ]; then pass=$((pass+1)); else
  echo "FAIL note: another directory's instance must not block or warn (rc=$rc): $out"; fail=$((fail+1)); fi
if grep -q "msg install --harness opencode2" "$FT_STUB_LOG"; then pass=$((pass+1)); else
  echo "FAIL note: receiver not staged before launch: $(cat "$FT_STUB_LOG")"; fail=$((fail+1)); fi
# The probe only runs once the service reports itself up (a bare `opencode api`
# would otherwise start a background service of its own).
if grep -q "api debug.location.list" "$OPENCODE_PROBE_LOG"; then pass=$((pass+1)); else
  echo "FAIL note: probe should list the service's locations: $(cat "$OPENCODE_PROBE_LOG")"; fail=$((fail+1)); fi

# The reported bug's setting: the target directory hosts (or hosted) an instance.
# Report it, name the directory and the standalone launch that keeps it working,
# and still launch — the agent would otherwise be needlessly lost.
run_guard "$tmp/status-live" "$tmp/loc-self"
if [ "$rc" -eq 0 ] && [ -s "$OPENCODE_STUB_LOG" ]; then pass=$((pass+1)); else
  echo "FAIL note: a hosted directory must not block the launch (rc=$rc): $out"; fail=$((fail+1)); fi
if grep -q "msg install --harness opencode2" "$FT_STUB_LOG"; then pass=$((pass+1)); else
  echo "FAIL note: receiver not staged before launch: $(cat "$FT_STUB_LOG")"; fail=$((fail+1)); fi
if printf '%s' "$out" | grep -q "note:" \
  && printf '%s' "$out" | grep -q -- "$target" \
  && printf '%s' "$out" | grep -q -- "--standalone"; then pass=$((pass+1)); else
  echo "FAIL note: should name the directory and the standalone launch: $out"; fail=$((fail+1)); fi

# A directory recorded under a different spelling (a symlinked worktree) is the
# same location, or the note would be trivially bypassed.
run_guard "$tmp/status-live" "$tmp/loc-symlinked"
if [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -q "note:"; then pass=$((pass+1)); else
  echo "FAIL note: symlinked location should still be reported (rc=$rc): $out"; fail=$((fail+1)); fi

# Fail open and stay quiet. A service that misreports itself, an API that errors,
# and an unparseable answer must neither block the launch nor cry wolf.
for probe in broken-service failing-api garbled-answer; do
  case "$probe" in
    broken-service) run_guard "$tmp/status-broken" "$tmp/loc-self" ;;
    failing-api) run_guard "$tmp/status-live" "$tmp/loc-self" API_EXIT=2 ;;
    garbled-answer) run_guard "$tmp/status-live" "$tmp/loc-garbage" ;;
  esac
  if [ "$rc" -eq 0 ] && [ -s "$OPENCODE_STUB_LOG" ] && ! printf '%s' "$out" | grep -q "note:"; then
    pass=$((pass+1))
  else
    echo "FAIL note/$probe: probe trouble must launch quietly (rc=$rc): $out"; fail=$((fail+1))
  fi
done

echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
