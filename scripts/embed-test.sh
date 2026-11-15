#!/usr/bin/env bash
#
# embed-test: run the opt-in real-embedding test. Unless
# FACTOTUM_EMBED_TEST_ENDPOINT points at a running embedding server, this starts
# a llama.cpp container, waits for its health endpoint, runs the Go test, and
# removes the container again.
#
# The container name and host port are per-run by default, so two concurrent
# runs on one machine cannot delete or bind over each other.
# FACTOTUM_EMBED_TEST_CONTAINER and FACTOTUM_EMBED_TEST_PORT pin them when a
# fixed name or address is wanted.
set -euo pipefail

# embed_container is the docker --name for the server, pinned when
# FACTOTUM_EMBED_TEST_CONTAINER is set.
embed_container() {
  if [ -n "${FACTOTUM_EMBED_TEST_CONTAINER:-}" ]; then
    printf '%s\n' "$FACTOTUM_EMBED_TEST_CONTAINER"
    return
  fi
  printf 'ft-embed-test-%s-%s\n' "$$" "$RANDOM"
}

# embed_port_publish is the docker -p spec. An empty host port asks docker (and
# so the kernel) for a free one; FACTOTUM_EMBED_TEST_PORT pins it instead.
embed_port_publish() {
  if [ -n "${FACTOTUM_EMBED_TEST_PORT:-}" ]; then
    printf '127.0.0.1:%s:8080\n' "$FACTOTUM_EMBED_TEST_PORT"
    return
  fi
  printf '127.0.0.1::8080\n'
}

# embed_bound_port reads the host port back out of `docker port`, which is the
# only way to learn the port docker handed out for a free-port request.
embed_bound_port() {
  sed -n 's#.*:\([0-9][0-9]*\)$#\1#p' <<<"${1:-}" | head -n 1
}

# Sourced by scripts/embed-test_test.sh to exercise the helpers above; stop here.
if [ "${BASH_SOURCE[0]}" != "$0" ]; then return 0; fi

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

endpoint="${FACTOTUM_EMBED_TEST_ENDPOINT:-}"
model="${FACTOTUM_EMBED_TEST_MODEL:-nomic-ai/nomic-embed-text-v1.5-GGUF:Q4_K_M}"
provider="${FACTOTUM_EMBED_TEST_PROVIDER:-openai}"
volume="${FACTOTUM_EMBED_TEST_VOLUME:-ft-llamacpp-models}"
image="${FACTOTUM_EMBED_TEST_IMAGE:-ghcr.io/ggml-org/llama.cpp:server}"

container=""
cleanup() {
  if [ -n "$container" ]; then
    docker rm -f "$container" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT INT TERM

if [ -z "$endpoint" ]; then
  if ! command -v docker >/dev/null 2>&1; then
    echo "no FACTOTUM_EMBED_TEST_ENDPOINT set and docker is not installed" >&2
    echo "start an embedding server, then set FACTOTUM_EMBED_TEST_ENDPOINT to it" >&2
    exit 1
  fi
  container="$(embed_container)"
  # The named volume keeps the downloaded model, so re-runs skip the download.
  docker volume create "$volume" >/dev/null
  docker run -d --name "$container" -p "$(embed_port_publish)" -e LLAMA_CACHE=/cache -v "${volume}:/cache" "$image" --embeddings --host 0.0.0.0 --port 8080 --ctx-size 2048 -hf "$model" >/dev/null
  port="$(embed_bound_port "$(docker port "$container" 8080)")"
  if [ -z "$port" ]; then
    echo "embed-test: docker reported no host port for container ${container}" >&2
    exit 1
  fi
  endpoint="http://127.0.0.1:${port}"
  echo "waiting for ${endpoint}/health"
  i=0
  while [ "$i" -lt 180 ]; do
    if curl -fsS "${endpoint}/health" >/dev/null 2>&1; then
      break
    fi
    i=$((i + 1))
    sleep 1
  done
  if [ "$i" -ge 180 ]; then
    echo "embedding server did not become healthy" >&2
    exit 1
  fi
fi

export FACTOTUM_EMBED_TEST_ENDPOINT="$endpoint"
export FACTOTUM_EMBED_TEST_PROVIDER="$provider"
export FACTOTUM_EMBED_TEST_MODEL="$model"
go test -count=1 -run TestRealEmbedding -v ./internal/cli/
