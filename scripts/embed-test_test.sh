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

if [ ! -f "$script" ]; then
  echo "FAIL script: $script does not exist"
  echo "passed=$pass failed=$((fail + 1))"
  exit 1
fi

# The helpers are the unit under test. Sourcing stops at the script's source
# guard, so no container is started and no test runs.
# shellcheck source=scripts/embed-test.sh
. "$script"

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

# The mise task delegates to this script, so the fixed values cannot creep back
# into the task body.
has() {
  local name="$1" needle="$2"
  if grep -qF -- "$needle" "$mise"; then ok; else bad "$name: missing '$needle' in mise.toml"; fi
}
lacks() {
  local name="$1" needle="$2"
  if grep -qF -- "$needle" "$mise"; then bad "$name: found '$needle' in mise.toml"; else ok; fi
}
has "test-embed runs the script" 'run = "bash scripts/embed-test.sh"'
lacks "test-embed carries no fixed container name" "ft-embed-test"
lacks "test-embed carries no fixed port" "18080"

echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
