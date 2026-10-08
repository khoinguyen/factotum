#!/usr/bin/env bash
# loop-agent.sh - start one chief-loop agent with the ft msg receiver loaded.
#
# Usage: loop-agent.sh <actor> <project> <task|-> <worktree> <prompt...>
#   actor:    the role to register as, e.g. builder-t-123, reviewer-t-123, chief
#   project:  the ft project id the receiver registers against
#   task:     the task the session works, or - for none (the chief)
#   worktree: the directory to run the agent in
#   prompt:   the kickoff prompt (one quoted argument)
#
# An agent launched this way is a first-class ft msg peer: the receiver is
# staged where OpenCode loads it, the FACTOTUM_* env tells it which actor to
# register as, and the TUI is exec'd. A peer then reaches it with
# `ft msg send actor:<actor>` (or the bare-role sugar `ft msg send <actor>`),
# with no cmux surface target. Set FT_BIN / OPENCODE_BIN to override the tools.
#
# Why this exists: a loop agent is launched directly (`opencode --prompt ...`),
# not through `ft run`, so `ft run`'s per-run receiver staging never happens.
# This wrapper does that staging once (idempotent) and exports the env the
# plugin reads, so every loop agent loads the receiver and is addressable by
# its role. `cmux-msg.sh` stays the fallback until a peer is verified reachable
# over ft msg.
set -euo pipefail

usage() { echo "usage: loop-agent.sh <actor> <project> <task|-> <worktree> <prompt...>" >&2; exit 2; }

[ "$#" -ge 5 ] || usage
actor="$1"; project="$2"; task="$3"; worktree="$4"; shift 4
prompt="$*"

[ -n "$actor" ] || usage
[ -n "$project" ] || usage
[ -d "$worktree" ] || { echo "loop-agent: worktree '$worktree' is not a directory" >&2; exit 1; }

ft_bin="${FT_BIN:-ft}"
opencode_bin="${OPENCODE_BIN:-opencode}"

# Stage the receiver where OpenCode loads plugins. Idempotent; safe to run on
# every launch. Without it, a globally-loaded plugin never exists and the agent
# is unreachable over ft msg.
"$ft_bin" msg install --harness opencode >/dev/null

export FACTOTUM_PROJECT="$project"
export FACTOTUM_ACTOR="$actor"
# The agent's `ft msg send` defaults --from to the configured actor, so a peer's
# shell sends as the role without an explicit --from.
export FACTOTUM_DEFAULT_ACTOR="$actor"
if [ "$task" != "-" ]; then
  export FACTOTUM_TASK_ID="$task"
else
  unset FACTOTUM_TASK_ID || true
fi

cd "$worktree"
exec "$opencode_bin" --prompt "$prompt" --auto
