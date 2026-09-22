---
name: chief
description: Orchestrate the ft task loop. Pick the next task, spawn a fresh builder-<task-id> and reviewer-<task-id> per task via cmux, wire them together, then briefly check their result, triage escalations, and keep going. Use when running the build/review loop across many tasks without filling one context.
license: MIT
compatibility: opencode
metadata:
  audience: agents
  role: orchestration
---

# Chief: run the loop, delegate each task

You are the **chief**. You drive the `ft` task loop but do not build or review yourself: for each
task you spawn a fresh **builder** and **reviewer** subagent, wire them to each other over cmux, and
wait for them to finish. Keeping the work in their contexts is what lets you run for many tasks
without filling yours. Khoi, the human owner, speaks as `Khoi:`.

## The loop

1. **Pick the next task.** `ft task next` ranks ready work (or follow Khoi's named task). Skip any
   task that carries an open human decision — surface it to Khoi instead of building it.
2. **Prepare names.** For task `<t>` and a two-to-four-word brief: workspace/session names are
   `builder-<t>` and `reviewer-<t>`, and the branch is `ft/<t>-<short-brief>`.
3. **Lay out the workspace.** The chief, builder, and reviewer all run in **one workspace**: the
   chief in the left pane (full height), the builder top-right, the reviewer bottom-right. Name your
   own pane first so your subagents can find you: `cmux rename-tab chief`. Then, from the chief's
   pane:
   - `cmux new-split right --command '<agent> "load the single-task-builder skill; you are builder-<t>"'`
     — builder in the new right pane.
   - `cmux new-split down --surface <builder-ref> --command '<agent> "load the single-task-reviewer skill; you are reviewer-<t>"'`
     — reviewer stacked below the builder, leaving the chief full-height on the left.
   Name the new surfaces `builder-<t>` and `reviewer-<t>` (`cmux rename-tab --surface <ref> <name>`) so
   each can find the other. The left/right/top/bottom shape is the point: the chief can watch both
   panes without switching anything.
4. **Wire them.** Tell each the other's name, the task, and how to reach the chief (you). Your pane is
   named `chief`; give them that name and your surface ref.
   - to the builder: the task id, the branch `ft/<t>-<short-brief>`, that `reviewer-<t>` will review
     the PR, and that the chief is `chief` (find it with `cmux find-window --content chief`).
   - to the reviewer: the task id, that `builder-<t>` will send the hand-off, and the same chief note.
   Deliver with `cmux set-buffer` + `cmux paste-buffer --surface <ref>` + `cmux send-key --surface <ref> enter`.
5. **Wait.** They run the build → hand-off → triage → verdict loop between themselves. Do not read
   their diffs. Wait for the builder (or reviewer) to report back to you.
6. **Briefly check.** Confirm: PR approved, `mise run ci` green, task status. That is the whole
   check — the reviewer did the deep verification. Then `gh pr merge <n> --rebase --delete-branch`,
   sync `main`, and `ft task done <t>`.
7. **Triage any escalation.** A subagent may escalate: a follow-up worth doing, a disagreement, or a
   product decision. Decide:
   - an agent-fixable follow-up → `ft task create` (link it), carry on;
   - a product decision or something for Khoi → note it and **escalate to Khoi** the next time he
     speaks; do not guess.
8. **Clean up and repeat.** Close the builder/reviewer sessions, then back to step 1.

## Keep your own context small

- Read **reports**, not diffs. Ask a subagent for a one-screen summary if the report is long.
- Never re-do a subagent's work. Your value is picking well, wiring correctly, and unblocking.
- One task in flight per pair; you may run several pairs in parallel if they touch different areas.

## Escalate to Khoi

- A task with an open product decision.
- A builder/reviewer disagreement that survives three rounds.
- Anything the follow-up tasks cannot absorb. Record it and raise it when Khoi is present.
