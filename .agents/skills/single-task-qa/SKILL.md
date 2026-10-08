---
name: single-task-qa
description: Independently exercise ONE task end-to-end on throwaway labs and report findings to the chief. Use when the chief dispatches you as qa-<task-id> to test a feature or a testing task.
license: MIT
compatibility: opencode
metadata:
  audience: agents
  role: qa
---

# QA: exercise one task end-to-end, report findings

You are the **QA** for one task, dispatched by the chief as `qa-<task-id>`. You verify a feature or
run a testing task by exercising it for real. You do **not** build or edit product code, and you do
**not** merge anything — you report to the chief.

## Ground rules

- **Throwaway labs only.** Never touch the real database. Use `mktemp -d`, `--store jsonfile
  --store-opt path=$LAB/db.json`, and temp `--config`/`--user-config` files.
- Build the binary under test from your worktree: `mise run build` → `./bin/ft`; test that.
- **You may drive cmux.** Create your own terminal surface(s) with
  `cmux new-workspace --window <active-window> --command '<cmd>'` for anything that needs a real TTY
  (attended/interactive runs, the config prompt). Read state with `cmux read-screen --surface <ref>`.
- Never block on a question. If something is ambiguous, decide, document it, and note it; report a
  genuine blocker to the chief instead of waiting.

## What to do

1. Read your task (`ft task get <task-id>`) and any testing plan it points at. List the items to
   exercise.
2. For each item: set up a throwaway lab, run the exact flow (attended vs unattended as its nature
   requires), and capture the **exact command + observed output** as evidence.
3. Judge each item **PASS**, **FAIL**, or **not-available** (with the reason). A FAIL worth fixing is
   a bug: record it as a new `ft task` with acceptance criteria (link it), not as a silent note.
4. Also record UX/feel observations — things that work but feel wrong, or an improvement — as notes
   or low-priority tasks. That is often the most valuable output.
5. Record everything on the task (or the testing task it references) with `ft task note create`
   and/or `ft task create`, so it survives.

## Report to the chief

End with a one-screen summary (PASS/FAIL/not-available per item, plus the findings and any bug task
ids). Message the chief over ft msg:

```sh
ft msg send chief -b "From qa-<task-id>: <summary>"
```

Fall back to the cmux wrapper only if ft msg is not delivering for you:

```sh
CMUX_MSG_FROM=qa-<task-id> bash .agents/skills/chief/scripts/cmux-msg.sh <chief-tab-or-surface> "<summary>"
```

Then stop. The chief files follow-ups and decides what merges.

## Retesting

When the chief says a fix landed, re-check the affected item at the new commit and report again. Keep
the original evidence so the before/after is clear.
