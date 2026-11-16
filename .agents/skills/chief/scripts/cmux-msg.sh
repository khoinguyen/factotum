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
#
# TITLE RESOLUTION. A restored session can leave duplicate titles behind: the
# stale surfaces sit in an old window while the live pair is in the current one.
# First-match-wins therefore risks pasting into a dead shell. When a title
# matches more than one surface (or workspace, including a same-titled workspace
# whose live agent sits under a differently-titled tab), the candidates are
# ranked by liveness - current window, active window, active workspace, active
# surface - and probed in that order for `recipient_has_agent: true`; a bare
# surface is never pasted into. If no candidate has a live agent the wrapper
# fails loudly and asks for an explicit ref instead of guessing.
set -euo pipefail

usage() { echo "usage: cmux-msg.sh <target> <text...>" >&2; exit 2; }

target="${1:-}"; shift || true
[ -n "$target" ] || usage
[ "$#" -gt 0 ] || { echo "cmux-msg: empty message" >&2; exit 2; }
text="$*"

# resolve_title <title> prints one tab-separated row per matching surface; only
# when no surface matches does it print matching workspaces instead. Columns:
#   score, tree-order, kind, ref, cleanup-surface, display, enclosing-workspace
# cleanup-surface is "-" for a workspace row (it has no surface of its own), so
# no column is empty: bash's `read` collapses runs of whitespace IFS tabs.
# Surfaces outrank workspaces as before (a tab title resolves to the tab). The
# score encodes liveness; tree-order keeps ties deterministic. The enclosing
# workspace lets the caller spot a same-titled workspace that is not just the
# surface's own container (a duplicate copy whose tab has a different title).
resolve_title() {
  cmux tree --all | awk -v n="$1" '
    function title_of(line) {
      if (match(line, /"[^"]*"/)) return substr(line, RSTART + 1, RLENGTH - 2)
      return ""
    }
    function emit(kind, ref, cleanup, score, display, owner) {
      order++
      print score "\t" order "\t" kind "\t" ref "\t" cleanup "\t" display "\t" owner
    }
    /^window window:/ {
      win_ref = ""
      if (match($0, /window:[0-9]+/)) win_ref = substr($0, RSTART, RLENGTH)
      win_base = (index($0, "[current]") ? 16 : 0) + (index($0, "◀ active") ? 8 : 0)
      ws_ref = ""; ws_title = ""; ws_base = 0
      next
    }
    /workspace workspace:/ {
      if (match($0, /workspace:[0-9]+/)) ws_ref = substr($0, RSTART, RLENGTH)
      ws_title = title_of($0)
      ws_base = (index($0, "◀ active") ? 4 : 0)
      if (ws_title == n)
        emit("workspace", ws_ref, "-", win_base + ws_base,
             "workspace \"" n "\" (" win_ref ")", ws_ref)
      next
    }
    /surface surface:/ {
      if (title_of($0) != n) next
      if (match($0, /surface:[0-9]+/)) sref = substr($0, RSTART, RLENGTH)
      score = win_base + ws_base + (index($0, "◀ active") ? 2 : 0)
      emit("surface", sref, sref, score,
           "surface " sref " (workspace \"" ws_title "\", " win_ref ")", ws_ref)
      next
    }
  '
}

# parse_json <json> <key> prints the string/bool value of key, or nothing. The
# capture excludes the closing brace: when the key is last, "false}" is not a
# boolean and would otherwise read as a live agent.
parse_json() {
  printf '%s\n' "$1" | sed -n "s/.*\"$2\"[[:space:]]*:[[:space:]]*\"\{0,1\}\([^\",}]*\)\"\{0,1\}.*/\1/p" | head -1
}

send_json() { # <ref>; prints cmux agent message's JSON
  local ref="$1"
  local -a send=(cmux agent message "$ref")
  if [ -n "${CMUX_MSG_FROM:-}" ]; then
    send+=(--from "$CMUX_MSG_FROM")
  fi
  send+=(--json -- "$text")
  "${send[@]}"
}

# paste_fallback <surface> <json> - deliver to a known bare surface (a single,
# unambiguous match only), then mark the stray queued copy read.
paste_fallback() {
  local surface="$1" json="$2" paste_out
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
}

case "$target" in
  surface:*|workspace:*|tab:*)
    ref="$target"
    json="$(send_json "$ref")"
    has_agent="$(parse_json "$json" recipient_has_agent)"
    if [ "$has_agent" != "false" ]; then
      printf '%s\n' "$json"
      exit 0
    fi
    surface="$(parse_json "$json" recipient_surface_ref)"
    [ -n "$surface" ] || surface="$ref"
    paste_fallback "$surface" "$json"
    exit 0
    ;;
esac

rows="$(resolve_title "$target")"
surfaces="$(printf '%s\n' "$rows" | awk -F'\t' 'NF && $3 == "surface"')"
workspaces="$(printf '%s\n' "$rows" | awk -F'\t' 'NF && $3 == "workspace"')"
candidates="$surfaces"

# A lone surface match can still be a stale copy: a duplicate workspace with the
# same title may hold the live agent under a differently-titled tab. Such a
# workspace is a candidate too, so the title is ambiguous, not a paste case.
# A workspace that merely contains the matched surface is not an extra copy.
if [ -n "$surfaces" ] && [ -n "$workspaces" ]; then
  extras="$(printf '%s\n%s\n' "$surfaces" "$workspaces" | awk -F'\t' '
    $3 == "surface" { own[$7] = 1; next }
    $3 == "workspace" && !($4 in own) { print }')"
  if [ -n "$extras" ]; then
    candidates="$(printf '%s\n%s\n' "$surfaces" "$extras")"
  fi
fi
[ -n "$candidates" ] || candidates="$workspaces"

if [ -z "$candidates" ]; then
  echo "cmux-msg: no surface/workspace named '$target'" >&2
  exit 1
fi

count="$(printf '%s\n' "$candidates" | wc -l | tr -d '[:space:]')"
ordered="$(printf '%s\n' "$candidates" | sort -t "$(printf '\t')" -k1,1nr -k2,2n)"
first="$(printf '%s\n' "$ordered" | head -1)"
ref="$(printf '%s' "$first" | cut -f4)"

if [ "$count" -eq 1 ]; then
  json="$(send_json "$ref")"
  has_agent="$(parse_json "$json" recipient_has_agent)"
  if [ "$has_agent" != "false" ]; then
    printf '%s\n' "$json"
    exit 0
  fi
  surface="$(parse_json "$json" recipient_surface_ref)"
  if [ -z "$surface" ]; then
    surface="$(printf '%s' "$first" | cut -f5)"
    [ "$surface" = "-" ] && surface=""
  fi
  if [ -z "$surface" ]; then
    printf 'cmux-msg: %s has no agent and no surface to paste into (pass an explicit surface ref)\n' "$ref" >&2
    exit 1
  fi
  paste_fallback "$surface" "$json"
  exit 0
fi

# The title is ambiguous. Deliver only to a candidate that reports a live agent;
# probing a bare surface only queues a copy there, which is marked read so a
# later attach does not replay it. Never paste when the title was ambiguous.
tried=""
while IFS=$'\t' read -r _score _order _kind cand cleanup display _owner; do
  [ -n "$cand" ] || continue
  [ "$cleanup" = "-" ] && cleanup=""
  if ! json="$(send_json "$cand")"; then
    printf 'cmux-msg: cannot message %s: ambiguous target %s\n' "$cand" "'$target'" >&2
    exit 1
  fi
  has_agent="$(parse_json "$json" recipient_has_agent)"
  if [ "$has_agent" != "false" ]; then
    printf '%s\n' "$json"
    exit 0
  fi
  surface="$(parse_json "$json" recipient_surface_ref)"
  [ -n "$surface" ] || surface="$cleanup"
  [ -n "$surface" ] && cmux agent inbox --surface "$surface" --state queued --mark-read >/dev/null 2>&1 || true
  tried="${tried}  ${display}"$'\n'
done <<<"$ordered"

printf "cmux-msg: ambiguous target '%s': %s matched and none has a live agent\n" "$target" "$count" >&2
printf '%s' "$tried" >&2
printf 'pass an explicit ref instead (e.g. cmux-msg.sh surface:<n> ...)\n' >&2
exit 1
