#!/usr/bin/env bash
# Tests for loop-agent.sh. Hermetic: stub `ft` and `opencode` on PATH record the
# argv/env they are called with, so no real receiver is installed and no agent
# is ever started.
#
# Run: bash .agents/skills/chief/scripts/loop-agent_test.sh
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="$here/loop-agent.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

export FT_STUB_LOG="$tmp/ft.log"
export OPENCODE_STUB_LOG="$tmp/opencode.log"
: >"$FT_STUB_LOG"
: >"$OPENCODE_STUB_LOG"

mkdir -p "$tmp/bin" "$tmp/worktree"
cat >"$tmp/bin/ft" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$FT_STUB_LOG"
exit 0
STUB
cat >"$tmp/bin/opencode" <<'STUB'
#!/usr/bin/env bash
{
  printf 'cwd=%s\n' "$PWD"
  printf 'project=%s actor=%s task=%s default=%s\n' \
    "${FACTOTUM_PROJECT:-}" "${FACTOTUM_ACTOR:-}" "${FACTOTUM_TASK_ID:-}" "${FACTOTUM_DEFAULT_ACTOR:-}"
  printf 'argv=%s\n' "$*"
} >>"$OPENCODE_STUB_LOG"
exit 0
STUB
chmod +x "$tmp/bin/ft" "$tmp/bin/opencode"
export PATH="$tmp/bin:$PATH"

fail=0
pass=0

# Task-scoped role: installs the receiver, exports actor+task env, runs in the
# worktree, and forwards the prompt.
: >"$FT_STUB_LOG"; : >"$OPENCODE_STUB_LOG"
"$script" builder-t-1 factotum t-1 "$tmp/worktree" "you are builder-t-1" >/dev/null
if grep -q "msg install --harness opencode" "$FT_STUB_LOG"; then pass=$((pass+1)); else
  echo "FAIL builder: did not install the receiver: $(cat "$FT_STUB_LOG")"; fail=$((fail+1)); fi
if grep -q "cwd=$tmp/worktree" "$OPENCODE_STUB_LOG" \
  && grep -q "project=factotum actor=builder-t-1 task=t-1 default=builder-t-1" "$OPENCODE_STUB_LOG" \
  && grep -q "argv=--prompt you are builder-t-1 --auto" "$OPENCODE_STUB_LOG"; then pass=$((pass+1)); else
  echo "FAIL builder: wrong agent launch: $(cat "$OPENCODE_STUB_LOG")"; fail=$((fail+1)); fi

# The chief has no task: FACTOTUM_TASK_ID must not leak in.
: >"$OPENCODE_STUB_LOG"
"$script" chief factotum - "$tmp/worktree" "you are chief" >/dev/null
if grep -q "project=factotum actor=chief task= default=chief" "$OPENCODE_STUB_LOG"; then pass=$((pass+1)); else
  echo "FAIL chief: task should be unset: $(cat "$OPENCODE_STUB_LOG")"; fail=$((fail+1)); fi

# Missing args and a bad worktree fail loudly without launching anything.
: >"$OPENCODE_STUB_LOG"
if "$script" builder-t-1 factotum >/dev/null 2>&1; then
  echo "FAIL missing args should fail"; fail=$((fail+1));
elif [ -s "$OPENCODE_STUB_LOG" ]; then
  echo "FAIL missing args launched the agent"; fail=$((fail+1));
else pass=$((pass+1)); fi

if "$script" builder-t-1 factotum t-1 "$tmp/nope" "x" >/dev/null 2>&1; then
  echo "FAIL bad worktree should fail"; fail=$((fail+1));
elif [ -s "$OPENCODE_STUB_LOG" ]; then
  echo "FAIL bad worktree launched the agent"; fail=$((fail+1));
else pass=$((pass+1)); fi

echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
