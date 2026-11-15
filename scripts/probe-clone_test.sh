#!/usr/bin/env bash
# Contract test for spike/github/probe.sh's throwaway wiki checkout. The probe
# used to clone into the fixed /tmp/ft-spike-wiki-clone and never remove it, so
# a second concurrent run failed `git clone` with "destination path already
# exists". The checkout must now live in a per-run directory that is removed on
# exit. Hermetic: a stub `gh` on PATH records the deletes, so no repository is
# ever created or deleted.
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

# The helpers are the unit under test. Sourcing stops at the script's source
# guard, so no repository is created and no probe runs.
# shellcheck source=spike/github/probe.sh
. "$script"

# Every run gets its own directory under the temp root.
base_a="$(probe_clone_base)"
base_b="$(probe_clone_base)"
case "$base_a" in
  "${TMPDIR:-/tmp}"/ft-spike-wiki-clone.*) ok ;;
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
mkdir -p "$tmp/bin"
cat >"$tmp/bin/gh" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$GH_STUB_LOG"
STUB
chmod +x "$tmp/bin/gh"

export GH_STUB_LOG="$tmp/calls.log"
: >"$GH_STUB_LOG"
export PATH="$tmp/bin:$PATH"
OWNER=acme
REPO=ft-spike-probe-1
PUB=ft-spike-wiki-1
CLONE_DIR="$(probe_clone_base)"
mkdir -p "$CLONE_DIR/wiki"

cleanup >/dev/null

if [ ! -e "$CLONE_DIR" ]; then ok; else bad "cleanup left the checkout at '$CLONE_DIR' behind"; fi
for want in "repo delete acme/ft-spike-probe-1 --yes" "repo delete acme/ft-spike-wiki-1 --yes"; do
  if grep -qxF -- "$want" "$GH_STUB_LOG"; then ok; else bad "cleanup did not run 'gh $want'"; fi
done
rm -rf "$CLONE_DIR"

echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
