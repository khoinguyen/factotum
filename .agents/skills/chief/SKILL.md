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

## cmux mechanics (read this first)

The shell your commands run in **does not inherit `CMUX_*`**, so any cmux command that defaults to
"the current surface/pane/workspace" fails with `not_found`. Never rely on the caller context:
resolve your own refs once and pass them explicitly.

- Resolve your refs: `cmux identify --id-format both` → window / workspace / pane / surface. Keep them.
- Pass them to every command: `--workspace <ws>`, `--surface <ref>`, and `--pane <ref>` where it takes one.
- Enumerate with `cmux tree --all`, `cmux list-pane-surfaces`, `cmux list-workspaces`. There is **no**
  `cmux list-surfaces`.
- Deliver a message: `cmux set-buffer --name <n> "<one line>"`, then
  `cmux paste-buffer --name <n> --surface <ref>`, then `cmux send-key --surface <ref> enter`.
  **Flatten the text to a single line first** — an embedded newline submits early, so a multi-line
  paste arrives as several messages. Longer content goes in a temp file whose path you send.
- Waking: pasting text into a surface and sending Enter delivers it as input, which starts a new turn
  in that agent's session. So a subagent "reporting to the chief" is simply it pasting into your
  `chief` surface; you wake on your next turn. You wake them the same way.

## The loop

1. **Pick the next task.** `ft task next` ranks ready work (or follow Khoi's named task). Skip any
   task that carries an open human decision — surface it to Khoi instead of building it.
2. **Set up the builder's worktree.** The chief stays in the repo on `main`; **each subagent gets its
   own directory** so branch switches never collide. Create the builder's worktree + branch before
   spawning:
   `git worktree add /tmp/ft-<t> -b ft/<t>-<short-brief> origin/main`
   Pass that path to the builder; it commits there and never creates or switches a branch itself.
3. **Lay out the workspace.** One workspace: chief left (full height), builder top-right, reviewer
   bottom-right. Name your own pane first so your subagents can find you:
   `cmux rename-tab --surface <chief-surface> chief`. Then, passing refs and starting each agent **in
   its own directory**:
   - `cmux new-split right --workspace <ws> --surface <chief-surface> --command 'cd /tmp/ft-<t> && opencode --prompt "load the single-task-builder skill; you are builder-<t>; work in /tmp/ft-<t> on branch ft/<t>-<short-brief>" --auto'`
     — builder in the new right pane, already in its worktree.
   - `cmux new-split down --workspace <ws> --surface <builder-ref> --command 'cd <repo> && opencode --prompt "load the single-task-reviewer skill; you are reviewer-<t>" --auto'`
     — reviewer stacked below the builder; it creates its own detached review worktree once the
     branch is pushed (see the reviewer skill).
   - Name them: `cmux rename-tab --surface <builder-ref> builder-<t>` and the same for the reviewer.
   - Agent: **opencode**. Its positional arg is a project path, not a prompt, so pass the kickoff via
     `--prompt`; `--auto` runs it unattended. Start it with `cd <worktree> && opencode …` so the agent
     works in the right directory.
4. **Wire them**, each message a single line, with explicit refs. Tell each the other's name, the task,
   and how to reach the chief. Your pane is named `chief`; give them that name and your surface ref.
   - to the builder: the task id, the branch `ft/<t>-<short-brief>`, that `reviewer-<t>` will review
     the PR, and that the chief is `chief` (find it with `cmux find-window --content chief`, or use the
     ref you give them).
   - to the reviewer: the task id, that `builder-<t>` will send the hand-off, and the same chief note.
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
8. **Clean up and repeat.** Close the builder/reviewer surfaces, then back to step 1.

## Keep your own context small

- Read **reports**, not diffs. Ask a subagent for a one-screen summary if the report is long.
- Never re-do a subagent's work. Your value is picking well, wiring correctly, and unblocking.
- One task in flight per pair; you may run several pairs in parallel if they touch different areas.

## Escalate to Khoi

- A task with an open product decision.
- A builder/reviewer disagreement that survives three rounds.
- Anything the follow-up tasks cannot absorb. Record it and raise it when Khoi is present.
