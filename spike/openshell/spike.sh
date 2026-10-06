#!/usr/bin/env bash
#
# OpenShell isolation-backend spike (task t-k3duthkrxo).
#
# Exercises the OpenShell CLI end to end against a throwaway workspace and a
# fake provider credential: create --detach, upload, exec a harness, download
# results, stop/start, delete. It asserts the safety properties the ft run
# isolation backend must rely on (non-root, read-only system paths,
# deny-by-default egress, mount/unshare denied, provider placeholder
# injection) and prints a transcript. Exits non-zero if any assertion fails.
#
# Safety: throwaway workspace under mktemp, a fake API key, and cleanup of the
# sandbox and provider on exit. Never point this at real credentials.
#
# Requires a running local OpenShell gateway (brew service sh.brew.openshell)
# and the opencode image. Run: mise run spike-openshell

set -u

SANDBOX="${FT_OS_SPIKE_SANDBOX:-ft-os-spike-$$}"
PROVIDER="${FT_OS_SPIKE_PROVIDER:-ft-os-spike-provider-$$}"
IMAGE="${FT_OS_SPIKE_IMAGE:-ghcr.io/anomalyco/opencode:latest}"
MODEL="${FT_OS_SPIKE_MODEL:-openrouter/nvidia/nemotron-3.5-lightning:free}"
FAKE_KEY="sk-spike-not-a-real-key"

WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/ft-os-spike.XXXXXX")"
WS="$WORKDIR/ws"
OUT="$WORKDIR/out"
FAILURES=0

log() { printf '\n=== %s ===\n' "$*"; }
pass() { printf 'PASS: %s\n' "$*"; }
fail() {
  printf 'FAIL: %s\n' "$*"
  FAILURES=$((FAILURES + 1))
}
expect_contains() { # label haystack needle
  case "$2" in
  *"$3"*) pass "$1" ;;
  *) fail "$1 (missing: $3)" ;;
  esac
}
expect_not_contains() {
  case "$2" in
  *"$3"*) fail "$1 (unexpected: $3)" ;;
  *) pass "$1" ;;
  esac
}

cleanup() {
  log "cleanup"
  openshell sandbox delete "$SANDBOX" >/dev/null 2>&1 || true
  # `sandbox delete` is asynchronous, so the provider is still attached for a
  # moment; retry the provider delete instead of swallowing the failure, or the
  # provider (and its credential material) leaks into gateway state.
  if openshell provider get "$PROVIDER" >/dev/null 2>&1; then
    i=0
    deleted=0
    while [ "$i" -lt 60 ]; do
      if openshell provider delete "$PROVIDER" >/dev/null 2>&1; then
        deleted=1
        break
      fi
      i=$((i + 1))
      sleep 1
    done
    if [ "$deleted" -eq 1 ]; then
      echo "provider $PROVIDER deleted"
    else
      echo "WARNING: provider $PROVIDER not deleted after ${i}s; clean it manually"
    fi
  fi
  rm -rf "$WORKDIR"
}
trap cleanup EXIT INT TERM

guest() { openshell sandbox exec -n "$SANDBOX" --no-login-shell -- "$@"; }
wait_ready() {
  i=0
  while [ "$i" -lt 30 ]; do
    if guest true >/dev/null 2>&1; then return 0; fi
    i=$((i + 1))
    sleep 2
  done
  return 1
}
decode_b64() { # reads stdin, writes stdout; macOS base64 uses -D
  if [ "$(uname)" = "Darwin" ]; then
    base64 -D
  else
    base64 -d
  fi
}

command -v openshell >/dev/null || {
  echo "openshell not found on PATH" >&2
  exit 2
}
openshell sandbox list >/dev/null 2>&1 || {
  echo "no reachable OpenShell gateway (start sh.brew.openshell)" >&2
  exit 2
}

mkdir -p "$WS" "$OUT"
printf 'hello from the throwaway workspace\n' >"$WS/input.txt"

cat >"$WORKDIR/policy.yaml" <<'YAML'
version: 1
filesystem_policy:
  read_only:
    - /usr
    - /lib
    - /bin
    - /etc
    - /proc
    - /dev/urandom
  read_write:
    - /tmp
    - /dev/null
  include_workdir: true
process:
  run_as_user: "1000"
  run_as_group: "1000"
YAML

log "create fake provider (no real credential)"
openshell provider create --name "$PROVIDER" --type openrouter \
  --credential "OPENROUTER_API_KEY=$FAKE_KEY"

log "create sandbox --detach (strict policy, deny-by-default egress)"
openshell sandbox create --name "$SANDBOX" --from "$IMAGE" \
  --policy "$WORKDIR/policy.yaml" --provider "$PROVIDER" \
  --no-auto-providers --detach -- sh -c 'sleep 3600'

log "non-root identity"
id_out="$(guest id 2>&1)"
echo "$id_out"
expect_contains "runs as non-root uid 1000" "$id_out" "uid=1000"

log "read-only system path (/etc)"
ro_out="$(guest sh -c 'touch /etc/ft-os-spike-probe 2>&1; echo rc=$?' 2>&1)"
echo "$ro_out"
expect_contains "write to /etc is denied" "$ro_out" "rc=1"
expect_not_contains "write to /etc did not succeed" "$ro_out" "rc=0"

log "writable workdir (/sandbox)"
rw_out="$(guest sh -c 'touch /sandbox/ft-os-spike-probe 2>&1; echo rc=$?' 2>&1)"
echo "$rw_out"
expect_contains "workdir is writable" "$rw_out" "rc=0"

log "deny-by-default egress (wget example.com)"
eg_out="$(guest sh -c 'wget -T 5 -q -O - https://example.com 2>&1; echo rc=$?' 2>&1)"
echo "$eg_out"
expect_contains "egress is denied" "$eg_out" "rc=1"
expect_not_contains "egress did not succeed" "$eg_out" "rc=0"

log "process controls (mount / unshare)"
mount_out="$(guest sh -c 'mount -t tmpfs none /mnt 2>&1; echo rc=$?' 2>&1)"
echo "$mount_out"
expect_contains "mount is denied" "$mount_out" "rc=1"
uns_out="$(guest sh -c 'unshare -Ur id 2>&1; echo rc=$?' 2>&1)"
echo "$uns_out"
expect_contains "unshare is denied" "$uns_out" "rc=1"

log "provider placeholder injection"
env_out="$(guest env 2>&1)"
echo "$env_out" | grep -E 'OPENROUTER_API_KEY|OPENSHELL_SANDBOX|^HOME=' || true
expect_contains "provider injects a placeholder" "$env_out" "OPENROUTER_API_KEY=openshell:resolve:env:"
expect_not_contains "raw fake key never reaches the sandbox" "$env_out" "$FAKE_KEY"

log "effective policy is binary-scoped to the harness"
policy_out="$(openshell policy get "$SANDBOX" --full 2>&1)"
echo "$policy_out"
expect_contains "provider endpoint present" "$policy_out" "openrouter.ai"
expect_contains "egress rule scoped to /usr/local/bin/opencode" "$policy_out" "/usr/local/bin/opencode"

log "harness: opencode"
ver_out="$(guest opencode --version 2>&1)"
echo "opencode --version -> $ver_out"
expect_contains "opencode harness runs" "$ver_out" "1.18."

log "upload throwaway workspace"
openshell sandbox upload "$SANDBOX" "$WS" /sandbox
cat_out="$(guest sh -c 'cat /sandbox/ws/input.txt' 2>&1)"
echo "guest read back: $cat_out"
expect_contains "uploaded workspace is readable" "$cat_out" "throwaway workspace"

log "exec harness, capture result.txt inside the sandbox"
HARNESS_CMD="cd /sandbox/ws && { echo \"opencode=\$(opencode --version)\"; opencode run -m ${MODEL} \"reply with exactly OK\"; } > result.txt 2>&1; echo rc=\$?"
guest sh -c "$HARNESS_CMD"
guest sh -c 'cat /sandbox/ws/result.txt' 2>&1

log "download results"
# The CLI's `sandbox download` resolves the source with `realpath -e --`, which
# BusyBox (this image) does not support, so it fails. Record that and fall back
# to streaming the file out through `sandbox exec`.
if openshell sandbox download "$SANDBOX" /sandbox/ws/result.txt "$OUT" 2>&1; then
  echo "note: CLI download unexpectedly succeeded"
else
  echo "note: CLI 'sandbox download' failed on this BusyBox image (realpath); using exec streaming"
fi
guest base64 /sandbox/ws/result.txt 2>/dev/null | decode_b64 >"$OUT/result.txt"
echo "--- downloaded result.txt (local) ---"
cat "$OUT/result.txt"
if [ -s "$OUT/result.txt" ]; then
  pass "results downloaded via exec streaming (non-empty)"
else
  fail "downloaded result.txt is empty"
fi

log "advisor proposals (manual approval, not auto-applied)"
sleep 15 # the supervisor flushes denial analysis to the gateway asynchronously (~10s)
openshell rule get "$SANDBOX" 2>&1 | grep -E 'Status|Rule:|Rationale:' || true

log "workspace persistence across stop/start"
guest sh -c 'date > /sandbox/ws/persist-marker.txt' 2>&1
openshell sandbox stop "$SANDBOX"
openshell sandbox list
openshell sandbox start "$SANDBOX"
wait_ready || fail "sandbox did not become ready after start"
persist_out="$(guest cat /sandbox/ws/persist-marker.txt 2>&1)"
echo "persist-marker.txt after restart: $persist_out"
if [ -n "$persist_out" ]; then
  pass "workspace survived stop/start"
else
  fail "workspace marker missing after restart"
fi

log "policy decisions (OCSF)"
openshell logs "$SANDBOX" --source sandbox -n 200 2>&1 \
  | grep -E 'ALLOWED|DENIED' | grep -E 'openrouter|example.com|opencode.ai|npmjs' | tail -n 12 || true

log "result"
if [ "$FAILURES" -eq 0 ]; then
  echo "ALL CHECKS PASSED"
  exit 0
fi
echo "$FAILURES CHECK(S) FAILED"
exit 1
