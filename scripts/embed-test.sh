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
#
# The model cache is deliberately shared (FACTOTUM_EMBED_TEST_VOLUME, default
# ft-llamacpp-models) so a re-run skips the ~80MB download. A cold shared cache
# is therefore a race: the Hugging Face cache publishes the snapshot before the
# blob is complete, so a second run reads a half-written GGUF and dies in
# gguf_init_from_reader. The download is serialized instead: a per-volume lock is
# held from the container start until the server reports healthy, so the next
# run starts against a warm, consistent cache. A waiting run gives up after
# FACTOTUM_EMBED_TEST_LOCK_TIMEOUT seconds (default 240, validated as a whole
# number up front so a malformed value fails the run at once) and names the lock
# it could not take.
set -euo pipefail

# embed_volume is the named docker volume that keeps the downloaded model,
# shared by every run on this machine so a re-run skips the download.
embed_volume() {
  printf '%s\n' "${FACTOTUM_EMBED_TEST_VOLUME:-ft-llamacpp-models}"
}

# embed_lock_file is the host-side lock guarding one volume's download. Keyed by
# volume, so runs sharing a cache serialize while runs on distinct caches never
# contend.
embed_lock_file() {
  local volume name
  volume="$(embed_volume)"
  name="${volume//[^A-Za-z0-9_.-]/_}"
  printf '%s/ft-embed-test-%s.lock\n' "${TMPDIR:-/tmp}" "$name"
}

# embed_lock_owner prints the pid holding <lock>, or nothing when the lock does
# not exist (or is unreadable, which no lock this script writes can be).
embed_lock_owner() {
  readlink "$1" 2>/dev/null || true
}

# embed_lock_reclaim <lock> drops a lock left by a run that is gone. The
# decision and the removal happen under a guard directory, because two
# reclaimers removing one stale lock is not safe: the second `rm` lands after
# the first has published its own lock and deletes it, handing two runs the
# same download. Under the guard a present lock is still the one whose owner was
# read as gone - a live run cannot publish into a taken path, and a dead owner
# cannot release - and an absent one is left alone, since a peer may publish it
# the next instant.
embed_lock_reclaim() {
  local lock="$1" guard owner
  guard="${lock}.reclaim"
  if ! mkdir "$guard" 2>/dev/null; then
    # This section is a few syscalls, so an old guard is a reclaimer that died.
    if [ -z "$(find "$guard" -maxdepth 0 -mmin +1 2>/dev/null)" ]; then
      return 1
    fi
    rmdir "$guard" 2>/dev/null || true
    if ! mkdir "$guard" 2>/dev/null; then
      return 1
    fi
  fi
  # Presence first: only a lock that exists at this read can be stale, and a
  # present path cannot be published into. An absent one can be published by a
  # peer the next instant, so leaving it is what keeps that peer's live lock.
  if [ ! -e "$lock" ] && [ ! -L "$lock" ]; then
    rmdir "$guard" 2>/dev/null || true
    return 1
  fi
  owner="$(embed_lock_owner "$lock")"
  if [ -n "$owner" ] && kill -0 "$owner" 2>/dev/null; then
    rmdir "$guard" 2>/dev/null || true
    return 1
  fi
  # Re-read the owner immediately before the `rm`: only the lock read here is
  # dropped, so a path republished since the check above is left alone.
  if [ "$(embed_lock_owner "$lock")" != "$owner" ]; then
    rmdir "$guard" 2>/dev/null || true
    return 1
  fi
  rm -f "$lock"
  rmdir "$guard" 2>/dev/null || true
  return 0
}

# embed_lock_timeout prints the wait limit in seconds.
# FACTOTUM_EMBED_TEST_LOCK_TIMEOUT must be a whole number of seconds: it is
# compared with `-ge` every iteration, so a value like "2m" would error there and
# the wait would never end. Validate it once, up front, so a bad value fails the
# run before it takes the lock.
embed_lock_timeout() {
  local timeout="${FACTOTUM_EMBED_TEST_LOCK_TIMEOUT:-240}"
  case "$timeout" in
    *[!0-9]*)
      echo "embed-test: FACTOTUM_EMBED_TEST_LOCK_TIMEOUT must be a whole number of seconds (e.g. 240), got '${timeout}'" >&2
      return 1
      ;;
  esac
  printf '%s\n' "$timeout"
}

# embed_lock_acquire takes the volume's download lock, waiting for a concurrent
# run to finish warming the cache. The owner is the symlink's target (a pid), so
# taking the lock is one atomic `ln -s` on any filesystem, and a run killed
# mid-download is reclaimable instead of wedging every later run. A live holder
# is bounded by this script's own health timeout; the wait here is bounded too,
# so a wedged lock names itself instead of hanging forever.
embed_lock_acquire() {
  local lock owner reclaimed started waited limit
  lock="$(embed_lock_file)"
  if ! limit="$(embed_lock_timeout)"; then
    return 1
  fi
  if [ ! -d "$(dirname "$lock")" ] || [ ! -w "$(dirname "$lock")" ]; then
    echo "embed-test: cannot create the download lock ${lock}" >&2
    return 1
  fi
  started=$SECONDS
  waited=""
  while :; do
    if ln -s "$$" "$lock" 2>/dev/null; then
      return 0
    fi
    owner="$(embed_lock_owner "$lock")"
    if [ -z "$owner" ] || ! kill -0 "$owner" 2>/dev/null; then
      reclaimed=""
      if embed_lock_reclaim "$lock"; then reclaimed=1; fi
      if [ -n "$reclaimed" ] && ln -s "$$" "$lock" 2>/dev/null; then
        return 0
      fi
      owner="$(embed_lock_owner "$lock")"
    fi
    if [ -z "$waited" ]; then
      waited=1
      echo "embed-test: waiting for the download lock ${lock} (owner ${owner:-unknown})" >&2
    elif [ "$((SECONDS - started))" -ge "$limit" ]; then
      echo "embed-test: gave up after ${limit}s on the download lock ${lock} (owner ${owner:-unknown}); remove that file if its run is gone" >&2
      return 1
    fi
    sleep 1
  done
}

# embed_lock_release drops the lock only while this run still owns it, so a lock
# reclaimed by another run is never deleted from under it.
embed_lock_release() {
  local lock
  lock="$(embed_lock_file)"
  if [ "$(embed_lock_owner "$lock")" = "$$" ]; then
    rm -f "$lock"
  fi
}

# embed_running reports whether the container is still up. The health loop needs
# this: a container that dies (a failed download, a bad image) otherwise becomes
# a silent 180s wait that never shows the reason.
embed_running() {
  [ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = true ]
}

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
image="${FACTOTUM_EMBED_TEST_IMAGE:-ghcr.io/ggml-org/llama.cpp:server}"

container=""
lock_held=""
cleanup() {
  if [ -n "$container" ]; then
    docker rm -f "$container" >/dev/null 2>&1 || true
  fi
  if [ -n "$lock_held" ]; then
    embed_lock_release
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
  volume="$(embed_volume)"
  # One download at a time per cache: the lock covers the cold download, and is
  # dropped once the server reports healthy, so the Go tests still run
  # concurrently.
  embed_lock_acquire
  lock_held=1
  docker volume create "$volume" >/dev/null
  docker run -d --name "$container" -p "$(embed_port_publish)" -e LLAMA_CACHE=/cache -v "${volume}:/cache" "$image" --embeddings --host 0.0.0.0 --port 8080 --ctx-size 2048 -hf "$model" >/dev/null
  port="$(embed_bound_port "$(docker port "$container" 8080)")"
  if [ -z "$port" ]; then
    echo "embed-test: docker reported no host port for container ${container}" >&2
    echo "embed-test: docker logs ${container}:" >&2
    docker logs "$container" >&2 || true
    exit 1
  fi
  endpoint="http://127.0.0.1:${port}"
  echo "waiting for ${endpoint}/health"
  i=0
  while [ "$i" -lt 180 ]; do
    if curl -fsS "${endpoint}/health" >/dev/null 2>&1; then
      break
    fi
    if ! embed_running "$container"; then
      echo "embed-test: container ${container} exited before it became healthy" >&2
      echo "embed-test: docker logs ${container}:" >&2
      docker logs "$container" >&2 || true
      exit 1
    fi
    i=$((i + 1))
    sleep 1
  done
  if [ "$i" -ge 180 ]; then
    echo "embed-test: embedding server did not become healthy" >&2
    echo "embed-test: docker logs ${container}:" >&2
    docker logs "$container" >&2 || true
    exit 1
  fi
  # Warm and consistent now, so the next run may download against it.
  embed_lock_release
  lock_held=""
fi

export FACTOTUM_EMBED_TEST_ENDPOINT="$endpoint"
export FACTOTUM_EMBED_TEST_PROVIDER="$provider"
export FACTOTUM_EMBED_TEST_MODEL="$model"
go test -count=1 -run TestRealEmbedding -v ./internal/cli/
