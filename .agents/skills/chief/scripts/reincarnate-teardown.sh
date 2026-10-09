#!/usr/bin/env bash
# reincarnate-teardown.sh - clean up a retired chief's leftovers.
#
# Usage: reincarnate-teardown.sh <retired-chief-surface-ref>
#   retired-chief-surface-ref: the cmux surface the outgoing chief ran in
#                              (a `surface:N` ref, e.g. surface:37).
#
# A successor chief runs this AFTER killing the outgoing chief's opencode PID.
# Two leftovers otherwise survive that PID kill:
#   1. The retired session's ft msg receiver (`ft msg agent claim --actor chief`)
#      is a CHILD of the dead opencode process; it reparents to PID 1 and keeps
#      long-polling actor:chief, so it steals messages meant for the successor.
#      Restrict the kill to PPID 1 (`-P 1`): the successor's own receiver, whose
#      opencode parent is still alive, is never matched.
#   2. The retired chief's cmux surface (its login shell) lingers. Its workspace
#      is resolved from `cmux tree` so the anchor surface is closed with the
#      required --workspace ref.
#
# The tool shell does NOT inherit CMUX_*, and cmux needs an explicit workspace
# to close an anchor surface, so the workspace is resolved here from the tree.
set -euo pipefail

usage() { echo "usage: reincarnate-teardown.sh <retired-chief-surface-ref>" >&2; exit 2; }

[ "$#" -eq 1 ] || usage
surface="${1:-}"
[ -n "$surface" ] || usage

# 1. Kill the retired session's orphaned chief receiver(s). PPID 1 means it was
#    reparented to init when the old opencode session died; the successor's own
#    receiver (parent alive) is left running.
pkill -9 -P 1 -f 'ft msg agent claim.*--actor chief' || true

# 2. Close the retired chief's surface under its workspace.
ws="$(cmux tree --all | awk -v s="$surface" '
  /workspace workspace:[0-9]+/ { match($0, /workspace:[0-9]+/); ws = substr($0, RSTART, RLENGTH) }
  /surface surface:/ && index($0, "surface " s " ") { print ws; exit }')"
[ -n "$ws" ] || { echo "reincarnate-teardown: no cmux surface $surface in the tree" >&2; exit 1; }

cmux close-surface --workspace "$ws" --surface "$surface"
