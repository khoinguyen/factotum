#!/usr/bin/env bash
# Contract test for scripts/smoke-quickstart.sh's signal handling.
#
# `trap cleanup EXIT INT TERM` ran the handler and then resumed whatever it
# interrupted, so a Ctrl-C or a CI TERM removed the throwaway HOME and store and
# carried straight on against them: the next `ft` call failed with a misleading
# error instead of reporting the interruption. The quickstart block also runs as
# a child, so a stopped run must take that child with it rather than leave it
# running against a scratch directory cleanup removed.
#
# The whole script is exercised against a copied tree whose `ft` is a stub that
# records its call and then hangs inside the quickstart block, so nothing is
# built, installed, or run against a real store.
#
# Run: bash scripts/smoke-quickstart_test.sh
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="$here/smoke-quickstart.sh"

pass=0
fail=0
ok()  { pass=$((pass + 1)); }
bad() { echo "FAIL $1"; fail=$((fail + 1)); }

# check <name> <want> <got>
check() {
  if [ "$2" = "$3" ]; then ok; else bad "$1: got '$3', want '$2'"; fi
}

# gone <pid> waits up to five seconds for <pid> to disappear, so a just-reaped
# child that is briefly a zombie is not mistaken for an orphan.
gone() {
  local pid="$1" deadline=$((SECONDS + 5))
  while kill -0 "$pid" 2>/dev/null && [ "$SECONDS" -lt "$deadline" ]; do sleep 0.1; done
  ! kill -0 "$pid" 2>/dev/null
}

# write_case <dir> lays out a copy of the script with a README carrying a
# one-command quickstart block and a stub `ft` that reports success for
# `version` (the script's install check) and hangs on every other call, so the
# run is waiting on the quickstart block when the signal arrives.
write_case() {
  local dir="$1"
  mkdir -p "$dir/scripts" "$dir/bin" "$dir/tmp"
  cp "$script" "$dir/scripts/smoke-quickstart.sh"
  cat >"$dir/README.md" <<'EOF'
# Smoke quickstart fixture

<!-- quickstart:begin -->
ft init -p acme
<!-- quickstart:end -->
EOF
  cat >"$dir/bin/ft" <<'STUB'
#!/usr/bin/env bash
printf 'ft %s\n' "$*" >>"$STUB_LOG"
if [ "$1" = version ]; then exit 0; fi
printf '%s\n' "$$" >"$STUB_CHILD_PID"
exec sleep 300
STUB
  chmod +x "$dir/bin/ft"
  : >"$dir/stub.log"
  rm -f "$dir/stub-child.pid" "$dir/rc" "$dir/secs" "$dir/note"
}

# interrupt_case <signal> <want-status> <want-name> starts the copied script,
# waits until its quickstart block is running, sends it <signal>, and asserts a
# prompt exit with the conventional 128+signal status that names the
# interruption and leaves neither the block's process nor the scratch dir
# behind.
interrupt_case() {
  local signal="$1" want_status="$2" want_name="$3"
  local dir rc secs stub
  dir="$(mktemp -d)"
  write_case "$dir"
  (
    # Job control, so the run is addressable as a job of its own and so INT is
    # delivered rather than inherited as ignored: an ignored signal cannot be
    # trapped at all.
    set -m
    PATH="$dir/bin:$PATH" TMPDIR="$dir/tmp" STUB_LOG="$dir/stub.log" \
      STUB_CHILD_PID="$dir/stub-child.pid" \
      bash "$dir/scripts/smoke-quickstart.sh" >"$dir/out" 2>&1 &
    child=$!
    deadline=$((SECONDS + 5))
    while [ ! -s "$dir/stub-child.pid" ] && [ "$SECONDS" -lt "$deadline" ]; do sleep 0.1; done
    if [ ! -s "$dir/stub-child.pid" ]; then
      kill -9 "$child" 2>/dev/null || true
      wait "$child" 2>/dev/null || true
      printf 'never reached the quickstart block: %s\n' "$(tr '\n' ';' <"$dir/out")" >"$dir/note"
      exit 0
    fi
    started=$SECONDS
    kill -s "$signal" "$child" 2>/dev/null || true
    deadline=$((SECONDS + 10))
    while kill -0 "$child" 2>/dev/null && [ "$SECONDS" -lt "$deadline" ]; do sleep 0.1; done
    if kill -0 "$child" 2>/dev/null; then
      # SIGKILL: the pre-fix handler cleans up and resumes the script, which is
      # exactly the run this deadline bounds.
      kill -9 "$child" 2>/dev/null || true
      wait "$child" 2>/dev/null || true
      printf 'still running 10s after %s\n' "$signal" >"$dir/note"
      exit 0
    fi
    if wait "$child"; then printf '0\n' >"$dir/rc"; else printf '%s\n' "$?" >"$dir/rc"; fi
    printf '%s\n' "$((SECONDS - started))" >"$dir/secs"
  ) 2>/dev/null
  # Reap the stub however this case turns out. The failing outcome is the one
  # that leaks: after the deadline SIGKILLs the script, its quickstart block is
  # still running against a scratch dir this case is about to delete, so the
  # `sleep 300` behind it would outlive the whole suite. The RETURN trap runs
  # after the assertions below, so the `gone` check still proves the script
  # itself stopped the block rather than this test tidying up after it.
  stub="$(cat "$dir/stub-child.pid" 2>/dev/null || true)"
  trap 'if [ -n "$stub" ]; then kill -9 "$stub" 2>/dev/null || true; fi; trap - RETURN' RETURN
  if [ -f "$dir/note" ]; then
    bad "a $signal during the quickstart block: $(cat "$dir/note")"
  else
    rc="$(cat "$dir/rc")"
    secs="$(cat "$dir/secs")"
    check "a $signal exits $want_status instead of resuming after cleanup" "$want_status" "$rc"
    if [ "$secs" -le 3 ]; then ok; else bad "a $signal stopped the run only after ${secs}s"; fi
    case "$(cat "$dir/out")" in
      *"interrupted by $want_name"*) ok ;;
      *) bad "an interrupted run did not name the interruption: $(tr '\n' ';' <"$dir/out")" ;;
    esac
    if gone "$(cat "$dir/stub-child.pid")"; then
      ok
    else
      bad "an interrupted run left its quickstart block running"
    fi
    if [ -n "$(ls -A "$dir/tmp" 2>/dev/null)" ]; then
      bad "an interrupted run left its scratch dir behind: $(ls -A "$dir/tmp" | tr '\n' ' ')"
    else
      ok
    fi
  fi
  rm -rf "$dir"
}

interrupt_case TERM 143 SIGTERM
interrupt_case INT 130 SIGINT

echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
