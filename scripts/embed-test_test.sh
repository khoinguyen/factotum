#!/usr/bin/env bash
# Contract test for scripts/embed-test.sh's container name and host port.
# Parallel `mise run test-embed` runs on one machine must not share them: a
# fixed ft-embed-test container made the second run's `docker rm -f` delete the
# first run's container, and a fixed port 18080 made it die with "port is
# already allocated". FACTOTUM_EMBED_TEST_CONTAINER/PORT must still pin both for
# anyone who needs a predictable one.
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

# The helpers are the unit under test. Sourcing stops at the script's source
# guard, so no container is started and no test runs.
# shellcheck source=scripts/embed-test.sh
if ! . "$script"; then
  bad "sourcing the script failed (is the source guard missing?)"
fi
if [ -s "$STUB_LOG" ]; then
  bad "sourcing the script ran: $(tr '\n' ';' <"$STUB_LOG")"
else
  ok
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
