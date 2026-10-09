#!/usr/bin/env bash
# cmux-layout.sh - lay out the chief loop in cmux.
#
# Convention (Khoi 2026-10-07): the chief workspace holds the chief on the LEFT
# and the Factotum Dashboard (ft serve, opened as a browser pane) on the RIGHT.
# Each builder/reviewer pair runs in its OWN workspace - named after the task id
# and placed in the same workspace group - with the builder on the LEFT and the
# reviewer on the RIGHT.
#
# Usage:
#   cmux-layout.sh dashboard <chief-workspace-ref> <url>
#   cmux-layout.sh pair <task-id> <group-ref> <window-ref> <builder-cmd> <reviewer-cmd>
#
# The tool shell does NOT inherit CMUX_*, so every caller passes explicit refs:
# the group (from `cmux workspace-group list --json`) and the active window (from
# `cmux identify --id-format both`). NEVER let new-workspace default its window:
# from an env-less shell it can land in the wrong (e.g. stale) window.
#
# Gotchas learned the hard way (do not rediscover):
#   - NEVER paste into a fresh workspace's anchor surface: paste-buffer fails with
#     "invalid_params: Surface is not a terminal". Create the builder with
#     `new-workspace --command` (and the reviewer with `new-split --command`).
#   - A workspace anchor surface can't be closed by a bare ref ("Surface ref not
#     found"); target the workspace: `cmux close-surface --workspace <ws> --surface <ref>`.
#   - `rename-tab` for a surface in a NON-caller workspace needs `--workspace <ws>`,
#     else it fails with "not_found: Tab not found".
#   - Do NOT create a second dashboard browser pane if one already exists (this
#     script checks the tree and skips).
set -euo pipefail

usage() {
  echo "usage: cmux-layout.sh dashboard <chief-workspace-ref> <url>" >&2
  echo "       cmux-layout.sh pair <task-id> <group-ref> <window-ref> <builder-cmd> <reviewer-cmd>" >&2
  exit 2
}

# surfaces_in_workspace <workspace-ref> prints each surface ref in that workspace.
# A workspace created by `new-workspace` may not have its surface registered in
# `cmux tree --all` for a beat, so poll until one appears instead of returning an
# empty list and aborting the layout. `CMUX_LAYOUT_SURFACE_TRIES` (default 50) and
# `CMUX_LAYOUT_SURFACE_INTERVAL` (default 0.1s) bound the wait; the test sets the
# interval to 0.
surfaces_in_workspace() {
  local ws="$1" tries="${CMUX_LAYOUT_SURFACE_TRIES:-50}" interval="${CMUX_LAYOUT_SURFACE_INTERVAL:-0.1}"
  local i=0 surfaces=""
  while :; do
    surfaces="$(cmux tree --all | awk -v w="$ws" '
      $0 ~ "workspace " w "([ ]|$)" { inws = 1; next }
      inws && /workspace / { inws = 0 }
      inws && /surface:/ { match($0, /surface:[0-9]+/); print substr($0, RSTART, RLENGTH) }')"
    [ -n "$surfaces" ] && break
    i=$((i + 1))
    [ "$i" -ge "$tries" ] && break
    sleep "$interval"
  done
  printf '%s\n' "$surfaces"
}

dashboard() { # <chief-ws> <url>
  local ws="${1:-}" url="${2:-}"
  [ -n "$ws" ] && [ -n "$url" ] || usage
  if cmux tree --all | awk -v w="$ws" '
      $0 ~ "workspace " w "([ ]|$)" { inws = 1; next }
      inws && /workspace / { inws = 0 }
      inws && /\[browser\]/ { found = 1 }
      END { exit found ? 0 : 1 }'; then
    echo "cmux-layout: dashboard browser pane already present in $ws; skipping" >&2
    return 0
  fi
  cmux new-pane --type browser --direction right --workspace "$ws" --url "$url"
}

pair() { # <task-id> <group-ref> <window-ref> <builder-cmd> <reviewer-cmd>
  local task="${1:-}" group="${2:-}" window="${3:-}" builder="${4:-}" reviewer="${5:-}" out ws surf rev
  [ -n "$task" ] && [ -n "$group" ] && [ -n "$window" ] && [ -n "$builder" ] && [ -n "$reviewer" ] || usage

  # Builder runs as the new workspace's initial command (left pane).
  out="$(cmux new-workspace --name "$task" --command "$builder" \
         --window "$window" --group "$group" --group-placement end)"
  ws="$(printf '%s' "$out" | grep -o 'workspace:[0-9]*' | head -1)"
  [ -n "$ws" ] || { echo "cmux-layout: no workspace ref in: $out" >&2; exit 1; }

  surf="$(surfaces_in_workspace "$ws" | head -1)"
  [ -n "$surf" ] || { echo "cmux-layout: no surface in $ws" >&2; exit 1; }

  # Reviewer to the right of the builder.
  out="$(cmux new-split right --workspace "$ws" --surface "$surf" --command "$reviewer")"
  rev="$(printf '%s' "$out" | grep -o 'surface:[0-9]*' | head -1)"
  [ -n "$rev" ] || { echo "cmux-layout: no surface ref in: $out" >&2; exit 1; }

  cmux rename-tab --workspace "$ws" --surface "$surf" "builder-$task"
  cmux rename-tab --workspace "$ws" --surface "$rev"  "reviewer-$task"
  printf 'pair workspace: %s (builder %s left, reviewer %s right)\n' "$ws" "$surf" "$rev"
}

cmd="${1:-}"; shift || true
case "$cmd" in
  dashboard) dashboard "$@" ;;
  pair) pair "$@" ;;
  *) usage ;;
esac
