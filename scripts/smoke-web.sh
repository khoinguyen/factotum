#!/usr/bin/env bash
#
# smoke-web: prove the shadcn/ui app ships in the binary. It builds nothing
# itself (run `mise run build` first, which embeds web/dist), then:
#   1. starts `ft serve` against a throwaway sqlite store,
#   2. fetches the SPA shell and the hashed asset it references from /app,
#   3. opens /events, mutates the graph through a second ft process, and
#      asserts a live `update` arrives,
#   4. asserts /api/snapshot now shows the new task,
#   5. posts a capture through the token-gated /api/capture and asserts it
#      stores an idea and rejects an unauthorised write.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

bin="$root/bin/ft"
if [ ! -x "$bin" ]; then
  echo "smoke-web: $bin is missing; run 'mise run build' first" >&2
  exit 1
fi

tmp="$(mktemp -d)"
port="${FACTOTUM_SMOKE_PORT:-18484}"
base="http://127.0.0.1:${port}"
server_pid=""
sse_pid=""

cleanup() {
  if [ -n "$sse_pid" ]; then kill "$sse_pid" 2>/dev/null || true; fi
  if [ -n "$server_pid" ]; then kill "$server_pid" 2>/dev/null || true; fi
  wait 2>/dev/null || true
  rm -rf "$tmp"
}
trap cleanup EXIT INT TERM

db="$tmp/ft.sqlite"
ft() { "$bin" --store sqlite --store-opt "path=$db" --no-hints "$@"; }

ft project create "Acme" >/dev/null
ft task create -p acme -t "First smoke task" >/dev/null

FACTOTUM_SERVE_TOKEN="smoketoken" \
  "$bin" --store sqlite --store-opt "path=$db" --no-hints serve -p acme \
  --bind "127.0.0.1:${port}" >"$tmp/serve.log" 2>&1 &
server_pid=$!

for _ in $(seq 1 100); do
  if curl -fsS "$base/app/" >/dev/null 2>&1; then break; fi
  sleep 0.1
done

index="$(curl -fsS "$base/app/")"
case "$index" in
  *'id="root"'*) ;;
  *)
    echo "smoke-web: /app/ did not serve the SPA shell:" >&2
    echo "$index" >&2
    exit 1
    ;;
esac

asset="$(printf '%s' "$index" | grep -o '/app/assets/[^"]*\.js' | head -n 1)"
if [ -z "$asset" ]; then
  echo "smoke-web: the served index references no hashed JS asset" >&2
  exit 1
fi
if ! curl -fsS "$base$asset" >/dev/null; then
  echo "smoke-web: the embedded asset $asset was not served" >&2
  exit 1
fi

curl -fsS -N "$base/events" >"$tmp/sse.log" 2>/dev/null &
sse_pid=$!
for _ in $(seq 1 100); do
  if grep -q 'event: hello' "$tmp/sse.log" 2>/dev/null; then break; fi
  sleep 0.1
done

ft task create -p acme -t "Second smoke task" >/dev/null

saw_update=""
for _ in $(seq 1 100); do
  if grep -q 'event: update' "$tmp/sse.log" 2>/dev/null; then saw_update=1; break; fi
  sleep 0.1
done
if [ -z "$saw_update" ]; then
  echo "smoke-web: a mutation did not push a live SSE update:" >&2
  cat "$tmp/sse.log" >&2
  exit 1
fi

snapshot="$(curl -fsS "$base/api/snapshot")"
case "$snapshot" in
  *'Second smoke task'*) ;;
  *)
    echo "smoke-web: /api/snapshot does not reflect the new task" >&2
    exit 1
    ;;
esac

capture="$(curl -fsS -X POST \
  -H 'Authorization: Bearer smoketoken' \
  -H 'Content-Type: application/json' \
  --data '{"text":"Captured from smoke"}' \
  "$base/api/capture")"
case "$capture" in
  *'/idea/'*) ;;
  *)
    echo "smoke-web: /api/capture did not store an idea: $capture" >&2
    exit 1
    ;;
esac

capture_snapshot="$(curl -fsS "$base/api/snapshot")"
case "$capture_snapshot" in
  *'Captured from smoke'*) ;;
  *)
    echo "smoke-web: the captured idea did not reach /api/snapshot" >&2
    exit 1
    ;;
esac

status="$(curl -s -o /dev/null -w '%{http_code}' -X POST \
  -H 'Content-Type: application/json' \
  --data '{"text":"unauthenticated"}' \
  "$base/api/capture")"
if [ "$status" != "401" ]; then
  echo "smoke-web: unauthenticated capture returned $status, want 401" >&2
  exit 1
fi

echo "smoke-web: ok - ft serve served the embedded app, a mutation pushed a live SSE update, and capture stored an idea while rejecting an unauthorised write"
