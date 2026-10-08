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
# It proves the documented install (a built `ft`), init, create, list, and next
# commands all work end to end. `ft run` and `ft groom` need a real harness and
# sandbox and are documented but not exercised here (see the PR body).
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

readme="$root/README.md"
bin="$root/bin/ft"
if [ ! -x "$bin" ]; then
  echo "smoke-quickstart: $bin is missing; run 'mise run build' first" >&2
  exit 1
fi

tmp="$(mktemp -d)"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT INT TERM

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
# when stdout is a terminal).
if ! ( cd "$work" && bash -euo pipefail -c "$block" ) >"$log" 2>&1; then
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

echo "smoke-quickstart: ok - the README quickstart (init, create, list, next) ran against a throwaway store"
