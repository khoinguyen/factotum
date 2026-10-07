#!/usr/bin/env bash
# cmux-msg.sh - reliably message another cmux agent.
#
# Usage: cmux-msg.sh <target> <text...>
#   target: surface:N | workspace:N | tab:N | exact tab/workspace title
#           (e.g. chief, builder-t-xxxx, reviewer-t-xxxx)
#   Set CMUX_MSG_FROM to name the sender (e.g. builder-t-xxxx).
#
# Sends through `cmux agent message`, which delivers via the recipient's hooks
# when it has a hook-capable agent. If it does not (e.g. the opencode plugin
# failed to load, so `recipient_has_agent` is false), the message would sit
# queued forever: the wrapper then pastes the text once into the recipient's
# agent surface with `cmux paste --submit` and marks the stray queued message
# read. Use this instead of set-buffer/paste-buffer/send-key for agent-to-agent
# messages; reserve paste-buffer for input that genuinely needs a terminal. Note
# the fallback does type into the terminal (once, submitted), unlike the hook
# path.
set -euo pipefail

usage() { echo "usage: cmux-msg.sh <target> <text...>" >&2; exit 2; }

target="${1:-}"; shift || true
[ -n "$target" ] || usage
[ "$#" -gt 0 ] || { echo "cmux-msg: empty message" >&2; exit 2; }
text="$*"

case "$target" in
  surface:*|workspace:*|tab:*)
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

# Keep paste's confirmation out of stdout and fail loudly if the submit key was
# not sent (cmux pastes the text but exits 0 with a warning). Only mark the
# queued copy read once the text actually submitted.
paste_out="$(cmux paste --surface "$surface" --submit -- "$text" 2>&1)" || {
  printf 'cmux-msg: paste failed for %s: %s\n' "$surface" "$paste_out" >&2
  exit 1
}
case "$paste_out" in
  *"submit key was not sent"*)
    printf 'cmux-msg: %s\n' "$paste_out" >&2
    exit 1 ;;
esac
cmux agent inbox --surface "$surface" --state queued --mark-read >/dev/null 2>&1 || true
printf '%s\n' "$json"
