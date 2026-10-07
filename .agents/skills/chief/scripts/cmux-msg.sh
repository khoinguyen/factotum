#!/usr/bin/env bash
# cmux-msg.sh - reliably message another cmux agent.
#
# Usage: cmux-msg.sh <target> <text...>
#   target: surface:N | workspace:N | pane:N | tab:N | exact tab/workspace title
#           (e.g. chief, builder-t-xxxx, reviewer-t-xxxx)
#   Set CMUX_MSG_FROM to name the sender (e.g. builder-t-xxxx).
#
# Sends through `cmux agent message`, which delivers via the target agent's hooks
# and never types into its terminal, so a message cannot land in a half-typed
# prompt. Use this instead of set-buffer/paste-buffer/send-key for agent-to-agent
# messages; reserve paste-buffer for input that genuinely needs a terminal.
set -euo pipefail

usage() { echo "usage: cmux-msg.sh <target> <text...>" >&2; exit 2; }

target="${1:-}"; shift || true
[ -n "$target" ] || usage
[ "$#" -gt 0 ] || { echo "cmux-msg: empty message" >&2; exit 2; }
text="$*"

case "$target" in
  surface:*|workspace:*|pane:*|tab:*)
    ref="$target" ;;
  *)
    # resolve a tab title to its surface ref
    ref="$(cmux tree --all | awk -v n="$target" '
      /surface:/ && index($0, "\"" n "\"")>0 { match($0,/surface:[0-9]+/); print substr($0,RSTART,RLENGTH); exit }')"
    if [ -z "$ref" ]; then
      # fall back to a workspace title (match the quoted title literally)
      ref="$(cmux tree --all | awk -v n="$target" '
        /workspace / && index($0, "\"" n "\"")>0 { match($0,/workspace:[0-9]+/); print substr($0,RSTART,RLENGTH); exit }')"
    fi
    [ -n "$ref" ] || { echo "cmux-msg: no surface/workspace named '$target'" >&2; exit 1; }
    ;;
esac

if [ -n "${CMUX_MSG_FROM:-}" ]; then
  exec cmux agent message "$ref" --from "$CMUX_MSG_FROM" -- "$text"
else
  exec cmux agent message "$ref" -- "$text"
fi
