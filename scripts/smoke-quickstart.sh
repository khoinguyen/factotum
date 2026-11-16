#!/usr/bin/env bash
#
# smoke-quickstart: the README's from-zero quickstart, run for real.
#
# The README Quickstart is a single fenced block delimited by
# `<!-- quickstart:begin -->` / `<!-- quickstart:end -->`. This script extracts
# that block verbatim and runs it in a throwaway HOME + working directory, so a
# new user's `ft init` writes to a temp config and a temp sqlite database rather
# than the machine's real ones. The block is the contract: if it stops working,
# the onboarding path is broken and this fails.
#
# It proves the documented install (a built `ft`), init, create, list, next, and
# skill commands all work end to end. `ft run` and `ft groom` need a real harness
# and sandbox and are documented but not exercised here (see the PR body).
set -euo pipefail

# Hermetic against the caller's shell: a builder or reviewer session exports
# FACTOTUM_* (PROJECT, TASK_ID, ACTOR, STORE, USER_CONFIG, ...), and any of them
# overrides the throwaway project, store, and HOME this smoke sets up - the
# quickstart then resolves the real project instead of the temp one. Clear the
# whole namespace before the built ft runs.
for name in $(compgen -v FACTOTUM_ || true); do
  unset "$name"
done

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

readme="$root/README.md"
bin="$root/bin/ft"
if [ ! -x "$bin" ]; then
  echo "smoke-quickstart: $bin is missing; run 'mise run build' first" >&2
  exit 1
fi

tmp="$(mktemp -d)"
block_pid=""
# Job control, so the quickstart block below runs in a process group of its own:
# an interrupt can then stop the block and everything it started, not just the
# shell waiting on it.
set -m
cleanup() {
  if [ -n "$block_pid" ]; then
    kill -TERM -- "-$block_pid" 2>/dev/null || true
    wait "$block_pid" 2>/dev/null || true
    block_pid=""
  fi
  rm -rf "$tmp"
}
# An interrupt must clean up and *stop*. `trap cleanup EXIT INT TERM` ran the
# handler and then resumed whatever it interrupted, so a Ctrl-C or a CI TERM
# removed the throwaway HOME and store and carried straight on against them,
# failing with a misleading error instead of reporting the interruption. Exit
# with the shell's conventional 128+signal status, and drop the EXIT trap so the
# one cleanup already done is not repeated.
on_signal() {
  echo "smoke-quickstart: interrupted by $1, cleaning up" >&2
  cleanup
  trap - EXIT
  exit "$2"
}
trap cleanup EXIT
trap 'on_signal SIGINT 130' INT
trap 'on_signal SIGTERM 143' TERM

# "Install": the binary runs. The quickstart below assumes `ft` is on PATH.
if ! "$bin" version >/dev/null; then
  echo "smoke-quickstart: ft version failed" >&2
  exit 1
fi

block="$(awk '/<!-- quickstart:begin -->/{f=1;next} /<!-- quickstart:end -->/{f=0} f && !/^```/' "$readme")"
if [ -z "${block//[[:space:]]/}" ]; then
  echo "smoke-quickstart: no quickstart block found in README.md" >&2
  echo "  expected markers: <!-- quickstart:begin --> ... <!-- quickstart:end -->" >&2
  exit 1
fi

export HOME="$tmp/home"
work="$tmp/work"
mkdir -p "$HOME" "$work"
export PATH="$root/bin:$PATH"

log="$tmp/quickstart.log"
# Run the documented block as a user would, with `ft` from the built binary on
# PATH. Redirecting to a file keeps `ft init` non-interactive (it prompts only
# when stdout is a terminal). The block runs in the background and is waited on
# so a trap fires while it is still running: bash defers a trap until the
# foreground command it interrupted finishes.
( cd "$work" && bash -euo pipefail -c "$block" ) >"$log" 2>&1 &
block_pid=$!
block_rc=0
wait "$block_pid" || block_rc=$?
block_pid=""
if [ "$block_rc" -ne 0 ]; then
  echo "smoke-quickstart: the README quickstart failed:" >&2
  cat "$log" >&2
  exit 1
fi

# The documented commands must have left a real, queryable task behind.
todo="$(cd "$work" && ft task list --status todo)"
if ! printf '%s' "$todo" | grep -Fq 'Try Factotum'; then
  echo "smoke-quickstart: 'ft task list' does not show the created task:" >&2
  printf '%s\n' "$todo" >&2
  exit 1
fi
id="$(printf '%s\n' "$todo" | awk 'NR==2 {print $1}')"
next="$(cd "$work" && ft task next)"
if ! printf '%s' "$next" | grep -Fq "$id"; then
  echo "smoke-quickstart: 'ft task next' does not rank the ready task $id:" >&2
  printf '%s\n' "$next" >&2
  exit 1
fi

# The README's "Teach your agent" block enumerates the embedded skills between
# `<!-- skills:begin -->` / `<!-- skills:end -->`, each as an `ft skill get <name>`
# line. Assert that documented set matches `ft skill list` exactly, then exercise
# every documented command.
skills="$(cd "$work" && ft skill list)"
listed="$(printf '%s\n' "$skills" | awk 'NR>1 && NF {print $1}' | sort -u)"
readme_skills="$(awk '/<!-- skills:begin -->/{f=1;next} /<!-- skills:end -->/{f=0} f' "$readme" \
  | sed -n 's/^ft skill get \([a-z][a-z-]*\).*/\1/p' | sort -u)"
if [ -z "$readme_skills" ]; then
  echo "smoke-quickstart: no skills block found in README.md" >&2
  echo "  expected markers: <!-- skills:begin --> ... <!-- skills:end -->" >&2
  exit 1
fi
if [ "$readme_skills" != "$listed" ]; then
  echo "smoke-quickstart: README skill list does not match 'ft skill list':" >&2
  echo "  README: $(printf '%s ' $readme_skills)" >&2
  echo "  ft:     $(printf '%s ' $listed)" >&2
  exit 1
fi
while read -r name; do
  if ! (cd "$work" && ft skill get "$name") | grep -Fq "# $name"; then
    echo "smoke-quickstart: 'ft skill get $name' did not print the $name skill" >&2
    exit 1
  fi
done <<< "$readme_skills"

echo "smoke-quickstart: ok - the README quickstart (init, create, list, next, skill) ran against a throwaway store"
