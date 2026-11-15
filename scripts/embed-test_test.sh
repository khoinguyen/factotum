#!/usr/bin/env bash
# Contract test for scripts/embed-test.sh's container name, host port, model
# cache, and startup.
#
# Parallel `mise run test-embed` runs on one machine must not share the
# container or the host port: a fixed ft-embed-test container made the second
# run's `docker rm -f` delete the first run's container, and a fixed port 18080
# made it die with "port is already allocated".
# FACTOTUM_EMBED_TEST_CONTAINER/PORT must still pin both for anyone who needs a
# predictable one.
#
# The *model cache*, by contrast, is deliberately shared: one volume
# (ft-llamacpp-models) so a re-run skips the ~80MB download. Two concurrent runs
# on that cold cache used to kill each other (one reads the half-written GGUF
# blob the other is still downloading), so the download is serialized by a
# per-volume lock instead of a per-run cache. Both invariants are checked here,
# and a container that dies must fail at once with its own logs rather than burn
# the 180s health wait.
#
# A regression is reported as a labelled FAIL, not a bare non-zero exit: the
# source guard is probed in a subshell before the helpers are sourced, and the
# concurrency check is proved against a copy with the lock removed.
#
# Run: bash scripts/embed-test_test.sh
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"
script="$here/embed-test.sh"
mise="$root/mise.toml"

pass=0
fail=0
ok()  { pass=$((pass + 1)); }
bad() { echo "FAIL $1"; fail=$((fail + 1)); }

# check <name> <want> <got>
check() {
  if [ "$2" = "$3" ]; then ok; else bad "$1: got '$3', want '$2'"; fi
}

# probe_source_guard <script> sources <script> in a subshell and reports whether
# it stopped at its own source guard. The subshell matters: a guard that `exit`s
# fails only the probe instead of ending this suite before it can say why. The
# endpoint is stripped because the probe is about the guard, not about how a
# guard-less body would run: an exported endpoint is the documented real-run
# path and would send that body to `go test` instead of the docker stub.
probe_source_guard() {
  ( export FACTOTUM_EMBED_TEST_ENDPOINT=; . "$1" ) >/dev/null 2>&1
}

# guard_label <script> names a source-guard regression, so the run reads as a
# labelled failure rather than an unexplained non-zero exit.
guard_label() {
  printf 'embed test guard: sourcing %s did not stop at the source guard' "$1"
}

# write_endpoint_stubs <dir> fills <dir>/bin with docker/curl/go recorders, so a
# script run starts no container, opens no socket, and runs no Go test. The
# docker recorder is the harness's cache simulator: `run` records LOCKED when
# the download lock is held by a live process at that moment, then holds
# STUB_RUNNING_MARK for STUB_RUN_SECS (a cold download) and creates
# STUB_OVERLAP when another run's `run` is already in flight - the collision
# that killed both runs. STUB_INSPECT_RUNNING=false simulates a container that
# exited, and STUB_CURL_EXIT=1 keeps its health endpoint dark.
write_endpoint_stubs() {
  local dir="$1"
  mkdir -p "$dir/bin" "$dir/locks"
  cat >"$dir/bin/docker" <<'STUB'
#!/usr/bin/env bash
printf 'docker %s\n' "$*" >>"$STUB_LOG"
case "$1" in
  run)
    if [ -L "$STUB_LOCK" ] && kill -0 "$(readlink "$STUB_LOCK")" 2>/dev/null; then
      printf 'LOCKED\n' >>"$STUB_LOG"
    else
      printf 'UNLOCKED\n' >>"$STUB_LOG"
    fi
    if [ -e "$STUB_RUNNING_MARK" ]; then : >"$STUB_OVERLAP"; fi
    : >"$STUB_RUNNING_MARK"
    sleep "${STUB_RUN_SECS:-0}"
    rm -f "$STUB_RUNNING_MARK"
    printf 'ft-embed-test-stub-id\n'
    ;;
  port) printf '127.0.0.1:49153\n' ;;
  inspect) printf '%s\n' "${STUB_INSPECT_RUNNING:-true}" ;;
  logs) printf '%s\n' "${STUB_CONTAINER_LOG:-stub container log}" ;;
esac
exit 0
STUB
  cat >"$dir/bin/curl" <<'STUB'
#!/usr/bin/env bash
printf 'curl %s\n' "$*" >>"$STUB_LOG"
exit "${STUB_CURL_EXIT:-0}"
STUB
  cat >"$dir/bin/go" <<'STUB'
#!/usr/bin/env bash
printf 'go %s\n' "$*" >>"$STUB_LOG"
exit 0
STUB
  chmod +x "$dir/bin/docker" "$dir/bin/curl" "$dir/bin/go"
}

# serialization_verdict <script> runs two concurrent instances of <script>
# against the stub docker and prints OK, or the label of the invariant that
# broke. Both runs share the default model cache (no volume pin) and start from
# a cold one, so they contend exactly as two `mise run test-embed` calls on a
# fresh machine do.
serialization_verdict() {
  local script="$1" dir lock overlap rc_a rc_b
  dir="$(mktemp -d)"
  write_endpoint_stubs "$dir"
  lock="$(TMPDIR="$dir/locks" embed_lock_file)"
  overlap="$dir/overlap"
  PATH="$dir/bin:$PATH" TMPDIR="$dir/locks" STUB_LOG="$dir/a.log" \
    STUB_LOCK="$lock" STUB_RUNNING_MARK="$dir/run.mark" STUB_OVERLAP="$overlap" \
    STUB_RUN_SECS=1 FACTOTUM_EMBED_TEST_ENDPOINT= FACTOTUM_EMBED_TEST_VOLUME= \
    FACTOTUM_EMBED_TEST_CONTAINER= bash "$script" >/dev/null 2>&1 &
  local pid_a=$!
  PATH="$dir/bin:$PATH" TMPDIR="$dir/locks" STUB_LOG="$dir/b.log" \
    STUB_LOCK="$lock" STUB_RUNNING_MARK="$dir/run.mark" STUB_OVERLAP="$overlap" \
    STUB_RUN_SECS=1 FACTOTUM_EMBED_TEST_ENDPOINT= FACTOTUM_EMBED_TEST_VOLUME= \
    FACTOTUM_EMBED_TEST_CONTAINER= bash "$script" >/dev/null 2>&1 &
  local pid_b=$!
  if wait "$pid_a"; then rc_a=0; else rc_a=$?; fi
  if wait "$pid_b"; then rc_b=0; else rc_b=$?; fi
  if [ "$rc_a" -ne 0 ] || [ "$rc_b" -ne 0 ]; then
    printf 'a concurrent run failed (rc %s and %s)' "$rc_a" "$rc_b"
  elif ! grep -q '^LOCKED$' "$dir/a.log" || ! grep -q '^LOCKED$' "$dir/b.log"; then
    printf 'a run started the server without holding the download lock'
  elif [ -e "$overlap" ]; then
    printf 'two runs started into the cold cache at the same time'
  elif ! grep -q '^go ' "$dir/a.log" || ! grep -q '^go ' "$dir/b.log"; then
    # Carry both records: this arm is timing-sensitive, and the logs are the
    # only evidence of where a run stopped.
    printf 'a concurrent run never reached the Go test (a: %s; b: %s)' \
      "$(tr '\n' ';' <"$dir/a.log")" "$(tr '\n' ';' <"$dir/b.log")"
  elif [ -L "$lock" ]; then
    printf 'the download lock outlived its run'
  else
    printf 'OK'
  fi
  rm -rf "$dir"
}

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

if [ ! -f "$script" ]; then
  echo "FAIL script: $script does not exist"
  echo "passed=$pass failed=$((fail + 1))"
  exit 1
fi

# Hermeticity lock: every docker/curl/go call is recorded, so sourcing the
# script must record nothing. If the guard moves below the container start (or
# goes away), the test fails loudly here instead of starting a real container
# and running the real embedding test.
mkdir -p "$tmp/bin"
for cmd in docker curl go; do
  cat >"$tmp/bin/$cmd" <<'STUB'
#!/usr/bin/env bash
printf '%s %s\n' "$(basename "$0")" "$*" >>"$STUB_LOG"
exit "${STUB_EXIT:-1}"
STUB
  chmod +x "$tmp/bin/$cmd"
done
export STUB_LOG="$tmp/stub.log"
: >"$STUB_LOG"
export PATH="$tmp/bin:$PATH"

# A regressed source guard is the one failure that can kill this suite before it
# reports: a guard that `exit`s takes the shell with it and leaves a bare status
# and no FAIL/passed= line. Probe the guard in a subshell first, so the
# regression is labelled and counted here; the stubs above keep the probe
# offline even when a guard-less script gets past the guard and starts the body.
if probe_source_guard "$script"; then
  ok
else
  bad "$(guard_label "$script")"
  echo "passed=$pass failed=$fail"
  exit 1
fi

# The helpers are the unit under test. The probe above proved the guard returns
# before the body, so this source only defines them.
# shellcheck source=scripts/embed-test.sh
if ! . "$script"; then
  bad "sourcing the script failed (is the source guard missing?)"
fi
if [ -s "$STUB_LOG" ]; then
  bad "sourcing the helpers ran: $(tr '\n' ';' <"$STUB_LOG")"
else
  ok
fi

# The guard check itself must be labelled. Run this suite against a copy whose
# source guard `exit`s instead of returning - the regression that used to end
# the run with a bare non-zero status and no FAIL/passed= line - and require a
# labelled failure plus the summary. The nested run stops at its own guard
# check, so it cannot recurse.
mkdir -p "$tmp/scripts"
broken="$tmp/scripts/embed-test.sh"
sed 's/^\(.*BASH_SOURCE\[0\].*then\) return 0; fi$/\1 exit 1; fi/' "$script" >"$broken"
if cmp -s "$script" "$broken"; then
  # Without the mutation the nested run would exercise the intact guard and
  # nest again from its own copy, so stop here rather than recurse.
  bad "the broken-guard copy is unchanged (has the source guard moved?)"
  echo "passed=$pass failed=$fail"
  exit 1
else
  ok
fi
cp "$mise" "$tmp/mise.toml"
cp "$here/embed-test_test.sh" "$tmp/scripts/embed-test_test.sh"
if nested="$(bash "$tmp/scripts/embed-test_test.sh" 2>&1)"; then
  bad "a source guard that exits was reported as a pass"
else
  case "$nested" in
    *"FAIL embed test guard"*) ok ;;
    *) bad "a source guard that exits was not labelled: $(tr '\n' ';' <<<"$nested")" ;;
  esac
  case "$nested" in
    *"passed="*"failed=1"*) ok ;;
    *) bad "the labelled guard failure printed no summary: $(tr '\n' ';' <<<"$nested")" ;;
  esac
fi

# A guard that is gone lets the body run, so the probe must be absorbing a
# stubbed call: only the stubs stand between the probe and a real container.
# This is also what pins the stubs above the probe - reorder the two and the
# call goes to the real docker instead of the log.
: >"$STUB_LOG"
unguarded="$tmp/scripts/embed-test-unguarded.sh"
grep -v 'BASH_SOURCE\[0\]' "$script" >"$unguarded"
if cmp -s "$script" "$unguarded"; then
  bad "the guard-less copy is unchanged (has the source guard moved?)"
elif probe_source_guard "$unguarded"; then
  bad "a script with no source guard was accepted as guarded"
else
  ok
fi
if grep -q '^docker ' "$STUB_LOG"; then
  ok
else
  bad "the guard-less probe reached no stub: $(tr '\n' ';' <"$STUB_LOG")"
fi

# An exported endpoint is the documented way to run the real embed test, so a
# developer's shell may carry one. It must not divert the guard-less probe to
# `go test` and turn this suite red, so the probe strips it.
: >"$STUB_LOG"
if FACTOTUM_EMBED_TEST_ENDPOINT=http://127.0.0.1:1 probe_source_guard "$unguarded"; then
  bad "a guard-less script with an endpoint exported was accepted as guarded"
fi
if grep -q '^docker ' "$STUB_LOG"; then
  ok
else
  bad "an exported endpoint diverted the guard-less probe to: $(tr '\n' ';' <"$STUB_LOG")"
fi

# A default container name is per-run: it carries a pid and stays clear of the
# old fixed name.
default="$(unset FACTOTUM_EMBED_TEST_CONTAINER; embed_container)"
case "$default" in
  ft-embed-test-[0-9]*-[0-9]*) ok ;;
  *) bad "default container is ft-embed-test-<pid>-<salt>: got '$default'" ;;
esac
if [ "$default" = "ft-embed-test" ]; then bad "default container must not be the fixed ft-embed-test"; else ok; fi

# Two concurrent runs are separate processes, so their names must differ.
run_a="$(bash -c '. "$1"; embed_container' _ "$script")"
run_b="$(bash -c '. "$1"; embed_container' _ "$script")"
if [ "$run_a" != "$run_b" ]; then ok; else bad "two concurrent runs share the container name '$run_a'"; fi

# An explicit pin still wins.
check "a pinned container wins" "ft-embed-pinned" \
  "$(FACTOTUM_EMBED_TEST_CONTAINER=ft-embed-pinned embed_container)"

# The publish spec asks docker for a free host port by default; nothing may
# hardcode the old 18080.
check "no env asks docker for a free host port" "127.0.0.1::8080" \
  "$(unset FACTOTUM_EMBED_TEST_PORT; embed_port_publish)"
check "an empty env still asks docker for a free host port" "127.0.0.1::8080" \
  "$(FACTOTUM_EMBED_TEST_PORT= embed_port_publish)"
check "a pinned port wins" "127.0.0.1:18999:8080" \
  "$(FACTOTUM_EMBED_TEST_PORT=18999 embed_port_publish)"

# The endpoint comes from the port docker actually bound, which is how the
# default free-port request is resolved back to a concrete port.
check "a bound port is read back" "49153" "$(embed_bound_port '127.0.0.1:49153')"
check "an unbound port yields nothing" "" "$(embed_bound_port '')"

# The model cache is one shared volume for every run on the machine, because a
# per-run cache would re-download ~80MB on every run. Concurrency is made safe
# below by serializing the download instead.
check "the default model cache is the shared ft-llamacpp-models" "ft-llamacpp-models" \
  "$(unset FACTOTUM_EMBED_TEST_VOLUME; embed_volume)"
check "a pinned model cache wins" "ft-embed-pinned" \
  "$(FACTOTUM_EMBED_TEST_VOLUME=ft-embed-pinned embed_volume)"

# Runs that share a cache must share its download lock, and runs on distinct
# caches must never contend: the lock is keyed by volume, not per run.
export TMPDIR="$tmp"
lock_a="$(FACTOTUM_EMBED_TEST_VOLUME=ft-embed-lock-same embed_lock_file)"
lock_b="$(FACTOTUM_EMBED_TEST_VOLUME=ft-embed-lock-same embed_lock_file)"
lock_other="$(FACTOTUM_EMBED_TEST_VOLUME=ft-embed-lock-other embed_lock_file)"
check "one volume has one download lock" "$lock_a" "$lock_b"
if [ "$lock_other" != "$lock_a" ]; then ok; else bad "two volumes share one download lock '$lock_other'"; fi

# The lock must exclude a second process, not merely exist: two concurrent runs
# on a cold cache raced because both were allowed to download at once.
lock_vol=ft-embed-lock-exclusive
lock="$(FACTOTUM_EMBED_TEST_VOLUME=$lock_vol embed_lock_file)"
rm -f "$lock"
held="$tmp/lock-held"
bash -c 'set -euo pipefail
  . "$1"
  export FACTOTUM_EMBED_TEST_VOLUME="$2"
  embed_lock_acquire
  : >"$3"
  sleep 2
  embed_lock_release' _ "$script" "$lock_vol" "$held" &
holder=$!
deadline=$((SECONDS + 10))
while [ ! -e "$held" ] && [ "$SECONDS" -lt "$deadline" ]; do sleep 0.1; done
if [ -e "$held" ]; then ok; else bad "the lock holder never took the download lock"; fi
started=$SECONDS
if latter="$(bash -c 'set -euo pipefail
  . "$1"
  export FACTOTUM_EMBED_TEST_VOLUME="$2"
  embed_lock_acquire
  embed_lock_release
  printf acquired' _ "$script" "$lock_vol" 2>/dev/null)"; then
  waited=$((SECONDS - started))
  if [ "$latter" = acquired ] && [ "$waited" -ge 1 ]; then
    ok
  else
    bad "a second run did not wait for a held download lock (waited ${waited}s)"
  fi
else
  bad "a second run never took the download lock after the holder released it"
fi
if wait "$holder"; then ok; else bad "the lock holder failed"; fi
if [ -L "$lock" ]; then bad "the download lock outlived its owner: $lock"; else ok; fi

# A run killed mid-download leaves its lock behind. That must not wedge every
# later run until someone removes it by hand, so a lock whose owner is gone is
# reclaimed. The owner is a pid that can never exist, so "gone" holds on every
# platform and a just-exited child's pid cannot be reused under the test.
dead_pid=2147483647
rm -f "$lock"
ln -s "$dead_pid" "$lock"
started=$SECONDS
if bash -c 'set -euo pipefail
  . "$1"
  export FACTOTUM_EMBED_TEST_VOLUME="$2"
  embed_lock_acquire
  embed_lock_release' _ "$script" "$lock_vol"; then
  waited=$((SECONDS - started))
  if [ "$waited" -lt 2 ]; then ok; else bad "a dead run's download lock wedged the next run for ${waited}s"; fi
else
  bad "a dead run's download lock was never reclaimed"
fi
if [ -L "$lock" ]; then bad "the reclaimed download lock outlived its owner: $lock"; else ok; fi

# A lock that cannot name its owner (a plain file, or a symlink with an empty
# target) is stale too: nothing this script writes looks like that, so waiting on
# it would wait for a run that does not exist.
rm -f "$lock"
: >"$lock"
started=$SECONDS
if bash -c 'set -euo pipefail
  . "$1"
  export FACTOTUM_EMBED_TEST_VOLUME="$2"
  embed_lock_acquire
  embed_lock_release' _ "$script" "$lock_vol"; then
  waited=$((SECONDS - started))
  if [ "$waited" -lt 2 ]; then ok; else bad "an ownerless download lock wedged the next run for ${waited}s"; fi
else
  bad "an ownerless download lock was never reclaimed"
fi
if [ -e "$lock" ] || [ -L "$lock" ]; then bad "the ownerless download lock outlived its run: $lock"; else ok; fi

# A stale lock can also sit behind a guard left by a reclaimer that died. That
# guard is a few syscalls old at worst, so an old one must not block reclaim.
rm -f "$lock"
ln -s "$dead_pid" "$lock"
mkdir "${lock}.reclaim"
touch -t 202001010000 "${lock}.reclaim"
started=$SECONDS
if bash -c 'set -euo pipefail
  . "$1"
  export FACTOTUM_EMBED_TEST_VOLUME="$2"
  embed_lock_acquire
  embed_lock_release' _ "$script" "$lock_vol"; then
  waited=$((SECONDS - started))
  if [ "$waited" -lt 2 ]; then ok; else bad "an abandoned reclaim guard blocked the lock for ${waited}s"; fi
else
  bad "an abandoned reclaim guard blocked the lock forever"
fi
rmdir "${lock}.reclaim" 2>/dev/null || true
if [ -e "$lock" ] || [ -L "$lock" ]; then bad "the lock survived an abandoned reclaim guard: $lock"; else ok; fi

# A reclaimer must not remove the lock while a peer holds the reclaim guard: that
# is the `rm` that hands two runs the same download. Proved directly, because the
# stampede below can only catch that race by luck.
guard_lock="$(FACTOTUM_EMBED_TEST_VOLUME=$lock_vol embed_lock_file)"
rm -f "$guard_lock" "${guard_lock}.reclaim"
ln -s "$dead_pid" "$guard_lock"
mkdir "${guard_lock}.reclaim"
if bash -c '. "$1"; embed_lock_reclaim "$2"' _ "$script" "$guard_lock"; then
  bad "a reclaimer removed the lock while a peer held the reclaim guard"
else
  ok
fi
if [ -L "$guard_lock" ]; then ok; else bad "the guarded reclaimer dropped a stale lock it had no guard for"; fi
rmdir "${guard_lock}.reclaim"
if bash -c '. "$1"; embed_lock_reclaim "$2"' _ "$script" "$guard_lock"; then ok; else bad "the guard holder could not reclaim the stale lock"; fi
if [ -e "$guard_lock" ] || [ -L "$guard_lock" ]; then bad "the reclaim left the stale lock behind"; else ok; fi

# A reclaim must only drop a lock it read as present: a lock that was absent at
# the guarded read leaves an empty path, and a peer may publish its own lock into
# it the next instant, so reading current existence there deletes a live lock.
# Uninstrumented that window is a few syscalls wide and needs 3+ simultaneous
# runs to hit, so it is widened with a sleep in a copy of the script.
window_vol=ft-embed-lock-window
window_lock="$(FACTOTUM_EMBED_TEST_VOLUME=$window_vol embed_lock_file)"
rm -f "$window_lock" "${window_lock}.reclaim"
wide="$tmp/scripts/embed-test-wide-window.sh"
window_line="$(grep -n '^  owner="$(embed_lock_owner "$lock")"$' "$script" | cut -d: -f1)"
if [ -z "$window_line" ]; then
  bad "the widened-window copy found no owner read in embed_lock_reclaim (has it moved?)"
else
  ok
fi
awk -v n="$window_line" '{ print } NR == n { print "  sleep 0.6" }' "$script" >"$wide"
if cmp -s "$script" "$wide"; then
  bad "the widened-window copy is unchanged (did the owner read disappear?)"
else
  ok
fi
bash -c '. "$1"; embed_lock_reclaim "$2"' _ "$wide" "$window_lock" &
window_reclaimer=$!
sleep 0.2
if ln -s "$$" "$window_lock" 2>/dev/null; then ok; else bad "a peer could not publish its lock into the absent path"; fi
wait "$window_reclaimer" 2>/dev/null || true
if [ "$(readlink "$window_lock" 2>/dev/null)" = "$$" ]; then
  ok
else
  bad "a reclaim removed a live lock a peer published into the window it was reading"
fi
rm -f "$window_lock"
rmdir "${window_lock}.reclaim" 2>/dev/null || true

# Reclaiming must never hand two runs the same lock: several processes released
# at once onto a dead run's lock, each reading back the owner it holds. A reclaim
# that removes a lock a peer has just taken shows up here as a foreign pid.
stampede_vol=ft-embed-lock-stampede
stampede_lock="$(FACTOTUM_EMBED_TEST_VOLUME=$stampede_vol embed_lock_file)"
rm -f "$stampede_lock" "${stampede_lock}.reclaim"
ln -s "$dead_pid" "$stampede_lock"
stolen="$tmp/stolen"
gate="$tmp/stampede-go"
: >"$stolen"
stampede_pids=()
for _ in 1 2 3 4; do
  bash -c 'set -euo pipefail
    . "$1"
    export FACTOTUM_EMBED_TEST_VOLUME="$2"
    while [ ! -e "$3" ]; do sleep 0.05; done
    round=0
    while [ "$round" -lt 2 ]; do
      embed_lock_acquire
      held="$(readlink "$(embed_lock_file)")"
      if [ "$held" != "$$" ]; then printf "held by %s, not %s\n" "$held" "$$" >>"$4"; fi
      embed_lock_release
      round=$((round + 1))
    done' _ "$script" "$stampede_vol" "$gate" "$stolen" 2>/dev/null &
  stampede_pids+=("$!")
done
sleep 0.3
: >"$gate"
stampede_rc=0
for pid in "${stampede_pids[@]}"; do if wait "$pid"; then :; else stampede_rc=1; fi; done
if [ "$stampede_rc" -eq 0 ]; then ok; else bad "a run in the reclaim stampede failed"; fi
if [ -s "$stolen" ]; then bad "a reclaimer stole a live download lock: $(head -n 1 "$stolen")"; else ok; fi
if [ -e "$stampede_lock" ] || [ -L "$stampede_lock" ]; then bad "the reclaim stampede left a lock behind: $stampede_lock"; else ok; fi

# Release must not delete a lock it does not own, or a reclaiming run could drop
# a peer's live lock at the end of its own run.
owner_vol=ft-embed-lock-release
owner_lock="$(FACTOTUM_EMBED_TEST_VOLUME=$owner_vol embed_lock_file)"
rm -f "$owner_lock" "${owner_lock}.reclaim"
owner_ready="$tmp/owner-ready"
bash -c 'set -euo pipefail
  . "$1"
  export FACTOTUM_EMBED_TEST_VOLUME="$2"
  embed_lock_acquire
  : >"$3"
  sleep 2
  embed_lock_release' _ "$script" "$owner_vol" "$owner_ready" &
lock_owner=$!
deadline=$((SECONDS + 10))
while [ ! -e "$owner_ready" ] && [ "$SECONDS" -lt "$deadline" ]; do sleep 0.1; done
bash -c 'set -euo pipefail
  . "$1"
  export FACTOTUM_EMBED_TEST_VOLUME="$2"
  embed_lock_release' _ "$script" "$owner_vol"
if [ "$(readlink "$owner_lock" 2>/dev/null)" = "$lock_owner" ]; then
  ok
else
  bad "a stranger's release deleted the owner's download lock"
fi
if wait "$lock_owner"; then ok; else bad "the lock owner failed"; fi
if [ -e "$owner_lock" ] || [ -L "$owner_lock" ]; then bad "the owner's lock outlived its run: $owner_lock"; else ok; fi

# A waiting run must say what it is waiting for, and it must not wait forever:
# with a short timeout it gives up naming the lock and leaves it alone.
wait_vol=ft-embed-lock-wait
wait_lock="$(FACTOTUM_EMBED_TEST_VOLUME=$wait_vol embed_lock_file)"
rm -f "$wait_lock" "${wait_lock}.reclaim"
wait_ready="$tmp/wait-ready"
bash -c 'set -euo pipefail
  . "$1"
  export FACTOTUM_EMBED_TEST_VOLUME="$2"
  embed_lock_acquire
  : >"$3"
  sleep 2
  embed_lock_release' _ "$script" "$wait_vol" "$wait_ready" &
lock_holder=$!
deadline=$((SECONDS + 10))
while [ ! -e "$wait_ready" ] && [ "$SECONDS" -lt "$deadline" ]; do sleep 0.1; done
started=$SECONDS
if waiter_out="$(FACTOTUM_EMBED_TEST_LOCK_TIMEOUT=1 bash -c 'set -euo pipefail
  . "$1"
  export FACTOTUM_EMBED_TEST_VOLUME="$2"
  embed_lock_acquire' _ "$script" "$wait_vol" 2>&1)"; then
  bad "a waiting run took a lock another run holds"
else
  waited=$((SECONDS - started))
  case "$waiter_out" in
    *"waiting for the download lock $wait_lock (owner $lock_holder)"*) ok ;;
    *) bad "a waiting run did not name the lock and its owner: $(tr '\n' ';' <<<"$waiter_out")" ;;
  esac
  if [ "$waited" -le 3 ]; then ok; else bad "a waiting run gave up only after ${waited}s, want ~1s"; fi
  case "$waiter_out" in
    *"gave up"*"$wait_lock"*) ok ;;
    *) bad "a waiting run did not name the lock it gave up on: $(tr '\n' ';' <<<"$waiter_out")" ;;
  esac
fi
if [ "$(readlink "$wait_lock" 2>/dev/null)" = "$lock_holder" ]; then
  ok
else
  bad "a run that gave up deleted the holder's lock"
fi
if wait "$lock_holder"; then ok; else bad "the lock holder failed"; fi

# The wait limit must be a whole number of seconds: a value like "2m" used to
# reach the `-ge` test every second, error there, and loop forever, so a run
# waiting on a wedged lock never gave up. It is validated once, up front, and
# fails the run at once naming the variable and the expected format.
check "a numeric lock timeout is the wait limit" "5" \
  "$(FACTOTUM_EMBED_TEST_LOCK_TIMEOUT=5 embed_lock_timeout)"
check "the default lock timeout is 240s" "240" \
  "$(unset FACTOTUM_EMBED_TEST_LOCK_TIMEOUT; embed_lock_timeout)"
if bad_out="$(FACTOTUM_EMBED_TEST_LOCK_TIMEOUT=2m embed_lock_timeout 2>&1)"; then
  bad "a non-numeric lock timeout was accepted"
else
  case "$bad_out" in
    *FACTOTUM_EMBED_TEST_LOCK_TIMEOUT*seconds*) ok ;;
    *) bad "a non-numeric lock timeout was not explained: $(tr '\n' ';' <<<"$bad_out")" ;;
  esac
fi

# The whole script must fail on that value before it takes the lock or starts a
# container, and without retrying. The lock is held by this live suite, so the
# pre-fix loop is exactly the hang the deadline below catches.
bad_vol=ft-embed-lock-bad-timeout
bad_lock="$(FACTOTUM_EMBED_TEST_VOLUME=$bad_vol embed_lock_file)"
rm -f "$bad_lock" "${bad_lock}.reclaim"
ln -s "$$" "$bad_lock"
bad_dir="$tmp/bad-timeout"
write_endpoint_stubs "$bad_dir"
started=$SECONDS
PATH="$bad_dir/bin:$PATH" STUB_LOG="$bad_dir/stub.log" \
  FACTOTUM_EMBED_TEST_LOCK_TIMEOUT=2m FACTOTUM_EMBED_TEST_ENDPOINT= \
  FACTOTUM_EMBED_TEST_VOLUME="$bad_vol" FACTOTUM_EMBED_TEST_CONTAINER= \
  bash "$script" >"$bad_dir/out" 2>&1 &
bad_pid=$!
deadline=$((SECONDS + 5))
while kill -0 "$bad_pid" 2>/dev/null && [ "$SECONDS" -lt "$deadline" ]; do sleep 0.1; done
if kill -0 "$bad_pid" 2>/dev/null; then
  # SIGKILL: the script traps TERM and resumes its loop after the handler, so a
  # TERM would leave the hang the deadline is meant to bound.
  kill -9 "$bad_pid" 2>/dev/null || true
  wait "$bad_pid" 2>/dev/null || true
  bad "a non-numeric lock timeout retried forever instead of failing fast"
else
  bad_rc=0
  wait "$bad_pid" || bad_rc=$?
  if [ "$bad_rc" -ne 0 ]; then ok; else bad "a non-numeric lock timeout exited 0"; fi
  bad_waited=$((SECONDS - started))
  if [ "$bad_waited" -le 3 ]; then ok; else bad "a non-numeric lock timeout failed only after ${bad_waited}s"; fi
  case "$(cat "$bad_dir/out")" in
    *FACTOTUM_EMBED_TEST_LOCK_TIMEOUT*seconds*) ok ;;
    *) bad "a non-numeric lock timeout printed no actionable error: $(tr '\n' ';' <"$bad_dir/out")" ;;
  esac
  if grep -q '^docker run ' "$bad_dir/stub.log"; then
    bad "a non-numeric lock timeout still started a container"
  else
    ok
  fi
fi
rm -f "$bad_lock"

# The whole point: two concurrent runs on a cold shared cache must both pass,
# with their startup windows serialized.
check "two concurrent runs on a cold cache serialize the download" "OK" \
  "$(serialization_verdict "$script")"

# A lock that is gone must fail that check loudly, so it cannot go vacuous.
nolock="$tmp/scripts/embed-test-nolock.sh"
grep -v '^[[:space:]]*embed_lock_acquire$' "$script" >"$nolock"
if cmp -s "$script" "$nolock"; then
  bad "the lock-less copy is unchanged (has the lock acquire left its own line?)"
elif verdict="$(serialization_verdict "$nolock")"; then
  case "$verdict" in
    OK) bad "the concurrency check passes a script with no download lock" ;;
    *) ok ;;
  esac
else
  bad "the concurrency check on a lock-less script reported no verdict"
fi

# A container that exits must fail the run at once, print its own logs, and not
# wait out the 180s health timeout.
dead_dir="$tmp/dead"
write_endpoint_stubs "$dead_dir"
dead_lock="$(TMPDIR="$dead_dir/locks" embed_lock_file)"
started=$SECONDS
if out="$(PATH="$dead_dir/bin:$PATH" TMPDIR="$dead_dir/locks" STUB_LOG="$dead_dir/stub.log" \
  STUB_LOCK="$dead_lock" STUB_CURL_EXIT=1 STUB_INSPECT_RUNNING=false \
  STUB_CONTAINER_LOG='boom: gguf_init_from_reader' \
  FACTOTUM_EMBED_TEST_ENDPOINT= FACTOTUM_EMBED_TEST_VOLUME= \
  FACTOTUM_EMBED_TEST_CONTAINER= bash "$script" 2>&1)"; then
  bad "a container that exited was reported as a healthy run"
else
  dead_wait=$((SECONDS - started))
  if [ "$dead_wait" -le 5 ]; then ok; else bad "a container that exited failed only after ${dead_wait}s"; fi
  case "$out" in
    *"docker logs ft-embed-test-"*) ok ;;
    *) bad "an exited container did not print 'docker logs <container>': $(tr '\n' ';' <<<"$out")" ;;
  esac
  case "$out" in
    *'boom: gguf_init_from_reader'*) ok ;;
    *) bad "an exited container's own logs were not printed: $(tr '\n' ';' <<<"$out")" ;;
  esac
  case "$out" in
    *'did not become healthy'*) bad "an exited container was reported as a health timeout" ;;
    *) ok ;;
  esac
fi
if [ -L "$dead_lock" ]; then bad "a failed run kept its download lock: $dead_lock"; else ok; fi

# The mise task delegates to this script, and its own body carries neither fixed
# value, so neither can creep back into the task.
block="$(awk '/^\[tasks\.test-embed\]/{found=1;next} /^\[/{found=0} found' "$mise")"
case "$block" in
  *'run = "bash scripts/embed-test.sh"'*) ok ;;
  *) bad "test-embed does not run scripts/embed-test.sh" ;;
esac
for stale in ft-embed-test 18080; do
  case "$block" in
    *"$stale"*) bad "test-embed still carries the fixed '$stale'" ;;
    *) ok ;;
  esac
done

echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
