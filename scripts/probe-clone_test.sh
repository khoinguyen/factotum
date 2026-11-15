#!/usr/bin/env bash
# Contract test for spike/github/probe.sh's throwaway wiki checkout. The probe
# used to clone into the fixed /tmp/ft-spike-wiki-clone and never remove it, so
# a second concurrent run failed `git clone` with "destination path already
# exists". The checkout must now live in a per-run directory that is removed on
# exit. Hermetic: a stub `gh`/`git` on PATH records the calls, so no repository
# is ever created or deleted - and a source-guard regression fails this test
# instead of running the real probe.
#
# Run: bash scripts/probe-clone_test.sh
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"
script="$root/spike/github/probe.sh"

pass=0
fail=0
ok()  { pass=$((pass + 1)); }
bad() { echo "FAIL $1"; fail=$((fail + 1)); }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

if [ ! -f "$script" ]; then
  echo "FAIL script: $script does not exist"
  echo "passed=$pass failed=$((fail + 1))"
  exit 1
fi

# Hermeticity lock: every gh/git call is recorded, so sourcing the probe must
# record nothing. If the guard moves below the gh calls (or goes away), the test
# fails loudly here instead of creating real GitHub repos.
mkdir -p "$tmp/bin"
for cmd in gh git; do
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
# guard, so no repository is created and no probe runs.
# shellcheck source=spike/github/probe.sh
if ! . "$script"; then
  bad "sourcing the probe failed (is the source guard missing?)"
fi
if [ -s "$STUB_LOG" ]; then
  bad "sourcing the probe ran: $(tr '\n' ';' <"$STUB_LOG")"
else
  ok
fi

# Every run gets its own directory under the temp root.
base_a="$(probe_clone_base)"
base_b="$(probe_clone_base)"
tmp_root="${TMPDIR:-/tmp}"
tmp_root="${tmp_root%/}"
case "$base_a" in
  "$tmp_root"/ft-spike-wiki-clone.*) ok ;;
  *) bad "clone base is a per-run mktemp dir: got '$base_a'" ;;
esac
if [ -d "$base_a" ] && [ -d "$base_b" ]; then ok; else bad "clone base must be a created directory"; fi
if [ "$base_a" != "$base_b" ]; then ok; else bad "two runs share the clone dir '$base_a'"; fi
rm -rf "$base_a" "$base_b"

# The fixed path must not survive anywhere in the probe.
if grep -qF '/tmp/ft-spike-wiki-clone' "$script"; then
  bad "the fixed /tmp/ft-spike-wiki-clone path is still in the probe"
else
  ok
fi
if grep -qF '$CLONE_DIR/wiki' "$script"; then ok; else bad "the probe does not clone into its per-run dir"; fi

# Cleanup removes the per-run checkout as well as the throwaway repos.
export STUB_EXIT=0
: >"$STUB_LOG"
OWNER=acme
REPO=ft-spike-probe-1
PUB=ft-spike-wiki-1
CLONE_DIR="$(probe_clone_base)"
mkdir -p "$CLONE_DIR/wiki"

cleanup >/dev/null

if [ ! -e "$CLONE_DIR" ]; then ok; else bad "cleanup left the checkout at '$CLONE_DIR' behind"; fi
for want in "gh repo delete acme/ft-spike-probe-1 --yes" "gh repo delete acme/ft-spike-wiki-1 --yes"; do
  if grep -qxF -- "$want" "$STUB_LOG"; then ok; else bad "cleanup did not run '$want'"; fi
done
rm -rf "$CLONE_DIR"

echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
