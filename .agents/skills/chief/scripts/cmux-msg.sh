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
#
# If the target has no hook-capable agent (e.g. the opencode plugin failed to
# load, so `recipient_has_agent` is false), the message would sit queued forever.
# In that case deliver it with `cmux paste --submit` into the resolved agent
# surface and mark the stray queued message read.
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

send=(cmux agent message "$ref")
if [ -n "${CMUX_MSG_FROM:-}" ]; then
  send+=(--from "$CMUX_MSG_FROM")
fi
send+=(--json -- "$text")

json="$("${send[@]}")"

has_agent="$(printf '%s\n' "$json" | sed -n 's/.*"recipient_has_agent"[[:space:]]*:[[:space:]]*\([a-z]*\).*/\1/p' | head -1)"

if [ "$has_agent" != "false" ]; then
  printf '%s\n' "$json"
  exit 0
fi

surface="$(printf '%s\n' "$json" | sed -n 's/.*"recipient_surface_ref"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
[ -n "$surface" ] || surface="$ref"

cmux paste --surface "$surface" --submit -- "$text"
cmux agent inbox --surface "$surface" --state queued --mark-read >/dev/null 2>&1 || true
printf '%s\n' "$json"
