---
name: single-task-builder
description: Build ONE task from the ft graph test-first and hand the PR to the assigned reviewer (reviewer-<task-id>), then report back to the chief. Use when the chief assigns you a single task as builder-<task-id>.
license: MIT
compatibility: opencode
metadata:
  audience: agents
  role: single-task-builder
---

# Single-task builder

You are the **builder for one task**, named `builder-<task-id>` by the **chief**. Build that task
test-first, hand the PR to the **reviewer** (`reviewer-<task-id>`), converge with him, and report
back to the chief when the task is finished. Khoi, the human owner, may also speak as `Khoi:`.

## Channel

- Your shell does **not** inherit `CMUX_*`, so pass explicit refs to every cmux command
  (`--workspace <ws>`, `--surface <ref>`, `--pane <ref>`); a bare command fails with `not_found`. Get
  your own refs from `cmux identify --id-format both`. Enumerate with `cmux tree --all` or
  `cmux list-pane-surfaces` (there is no `list-surfaces`).
- Prefix messages to the reviewer with `From builder-<task-id>: ...`. He replies
  `From reviewer-<task-id>, regarding PR #N: ...`.
- The reviewer is a peer agent in another cmux surface; the chief tells you his name and ref. Find him
  with `cmux find-window --content reviewer-<task-id>`.
- Deliver with `cmux set-buffer --name <n> "<one line>"`, `cmux paste-buffer --name <n> --surface <ref>`,
  then `cmux send-key --surface <ref> enter`. **Flatten the text to a single line first** — an embedded
  newline submits early, so a multi-line paste arrives as several messages. Keep messages small;
  longer text goes in a temp file whose path you send.
- **After sending, read the screen once to confirm receipt, then stop and wait.** Do not poll.
- Reporting to the chief wakes it: you paste into the `chief` surface and press Enter, and that input
  starts its next turn.

## The task

- The chief gives you the task id, your **worktree path** (your working directory), and the branch
  `ft/<task-id>-<short-brief>` — already created. **Do not create or switch branches**; commit in the
  worktree you were given.
- `ft task get <task-id>` is the spec. Run `ft task start <task-id>` and `ft task assign <task-id> --actor claude`.
- Build test-first: RED, implement, GREEN, then `mise run ci`. Update the embedded skill when the CLI
  surface changes.
- Open a right-scoped PR: imperative subject; body with Intention, Fit, Exercise transcript (throwaway
  store), Risks, Reviewer focus, Tests, Relaxed tests, Breaking change; end with `Refs <task-id>`.
- If the core behavior depends on a real external dependency (a model, network/API, service), make it
  runnable and state in the PR body **what you ran for real vs with fakes**. If you could not run it
  for real, say so plainly — the review must know, so it can escalate rather than approve on fakes.

## Hand-off

```
From builder-<task-id>: Please review PR #<n> (task <t>, branch <b>) - <title>. Link: <url>.
CI green; Exercise in the PR body. Focus: <the 1-3 riskiest things>.
```

Then `ft task review <t>` and a note linking the PR.

## Triage

Answer every finding by number, mark it **fix** or **pushback**, and justify a pushback. Fix real
findings with a test that would have caught them. If a finding shows the *task body* was wrong, fix
the code **and** correct the body. Push the fixes and ask for re-review. **Do not merge** — merging
is the chief's call.

## Git discipline

- You work in your own worktree on the branch the chief created. Never commit to `main`. If you find
  yourself on `main` by accident: `git switch -c <branch>` keeps the commit, then
  `git branch -f main origin/main`.
- SSH push fails? `gh auth setup-git` and
  `git config --local url."https://github.com/".insteadOf "git@github.com:"`.

## Report to the chief and stop

When the reviewer approves — or you and he cannot agree — report the outcome to the chief: task id,
PR number, verdict, and anything unresolved. The chief gave you its name (`chief`) and surface ref at
spawn; message it the same way you message the reviewer (`cmux set-buffer` + `cmux paste-buffer
--surface <chief-ref>` + `cmux send-key --surface <chief-ref> enter`), or find it with
`cmux find-window --content chief`. Then stop; the chief decides what happens next.
- **Escalation:** if after three rounds you and the reviewer cannot align, tell the chief and leave
  the PR open for Khoi.
