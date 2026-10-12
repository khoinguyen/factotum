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
#   - ALWAYS anchor a pair to an explicit cwd. A new workspace inherits the FOCUSED
#     surface's cwd, so a relative command (e.g. .agents/skills/chief/scripts/
#     loop-agent.sh) can 404 when the focused surface is another repo, leaving a
#     created-but-dead pair workspace. pair() passes --cwd <repo-root> to
#     new-workspace; new-split has no --cwd flag, so the reviewer's command is
#     prefixed with `cd <repo-root> &&` to anchor it the same way.
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

# repo_root is the factotum checkout that holds this script - the directory the
# relative agent commands resolve against. Derive it from the script's own
# location so it never depends on the caller's or the focused surface's cwd.
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"

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

  # Builder runs as the new workspace's initial command (left pane), anchored to
  # the repo root so a relative command resolves regardless of what was focused.
  out="$(cmux new-workspace --name "$task" --cwd "$repo_root" --command "$builder" \
         --window "$window" --group "$group" --group-placement end)"
  ws="$(printf '%s' "$out" | grep -o 'workspace:[0-9]*' | head -1)"
  [ -n "$ws" ] || { echo "cmux-layout: no workspace ref in: $out" >&2; exit 1; }

  surf="$(surfaces_in_workspace "$ws" | head -1)"
  [ -n "$surf" ] || { echo "cmux-layout: no surface in $ws" >&2; exit 1; }

  # Reviewer to the right of the builder. new-split has no --cwd flag, so anchor
  # the reviewer to the repo root in its command; otherwise it could inherit an
  # unrelated cwd the same way the workspace used to.
  out="$(cmux new-split right --workspace "$ws" --surface "$surf" --command "cd '$repo_root' && $reviewer")"
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
