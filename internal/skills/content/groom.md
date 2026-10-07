# groom

The grooming session turns raw ideas into a groomed, agent-ready task graph. This
skill is the session protocol: who decides what, how to grill, how to classify
answers, how to split, and the deterministic report every session ends with.

You are the **team lead**; the human is the **product owner (PO)**.

## Launch

`ft groom [item...]` runs the session. It resolves the **scope** (the named
items, or by default every open idea plus every open ungroomed task), injects a
**kickoff**, runs the durable prompt task-less over every repository of the
project, and captures the two outputs.

```sh
ft groom --sandbox local --harness opencode --allow-host
ft groom t-abc t-def --sandbox local --harness opencode --allow-host
ft groom --unattended --sandbox local --harness opencode --allow-host
```

`--sandbox` and `--harness` are optional: `ft groom` resolves them like `ft run`
(flag, env, project `[run]`, machine `[run]`), prompting once on a terminal when
unset; a machine `[run]` table lets a bare `ft groom` run with no flags.

The prompt is durable repo data at `docs/grooming/prompt.md` - never an
ephemeral temp file; override it with `--prompt-file`. `ft groom` appends the
kickoff to the prompt. The kickoff names:

- the scope: every item's id, kind, and title;
- the absolute **report** path and **deferred-questions** path, both under the
  project data dir at `grooming-sessions/<session-id>/`, named `report.md` and
  `deferred-questions.md`;
- the report and deferred-questions section contracts, in order.

Write the report to the report path and the deferred questions to the
deferred-questions path. When the session finishes, `ft groom` reads both files
and records them as `doc` artifacts (`ft doc list`); a session that writes
neither fails with a clear error. The files are written on the host under the
project data dir, so the selected backend must give the session write access
there; the isolating backends mount only the resolved workspace today.

## Unattended mode

`ft groom --unattended` runs with no product owner and no interactive channel. The
kickoff tells the session to defer every product question to the
deferred-questions file, record engineering calls as notes, and suspend the
pre-answer timing guard so it still promotes, splits, and marks produced tasks
groomed and assigned. After the run `ft groom` requires every scoped item to be
agent-ready or named in the deferred-questions file; an item left neither fails
the run. Use it for an automated loop; a human session omits the flag and grills
the PO as usual.

## Who decides what

- The **PO** owns product: what to build, why, priority, user-visible scope and
  behavior, and product trade-offs (security posture, cost). Ask the PO only these.
- **You** own engineering: decomposition, phasing, rollout, sequencing, technique,
  and verification gates. Decide these yourself, record the call
  (`ft task note create`), and move on. Do not ask the PO about internal mechanics.

## Run the session

1. Read the target ideas/tasks and their origins; read the repo, specs, and docs
   (`ft doc search`, `ft memory search`).
2. Cross-check each item against the code for inconsistency, ambiguity, and
   technical trade-offs.
3. Split decisions into product questions (grill the PO) and engineering calls
   (you decide).
4. Grill: product questions only, each with concrete options plus a `defer`.
   Append every deferred question to the deferred-questions file for stakeholders.
5. Decompose into small, focused, independently-reviewable tasks: **1 task = 1 PR**.
   Add dependencies that express real blocking, and a verification gate per task.
6. Apply agent-owned changes: notes, decomposition, directly implied acceptance.
7. After the PO answers, apply the outcomes, build the DAG, and run hygiene.

## Grill the PO

- Ask product questions only. Each question offers concrete options and a `defer`
  the PO can choose when the decision is not theirs to make now.
- Deferred questions are written to the deferred-questions file (path in the
  kickoff) so stakeholders can answer them later. Never leave a defer only in chat.
- Keep the grill short and high-leverage; prefer a handful of decisions that change
  the shape of the work.

## Classify each answer

Every PO answer resolves to exactly one of:

- a **new requirement** - a product constraint that applies to existing work;
- an **acceptance criterion** - an observable condition that defines done, folded
  into the item's body;
- a **new idea** - a future possibility captured with `ft idea create`, left
  immutable as history;
- a **new task** - decomposable, buildable work created with `ft task create`.

## Completion

A completed session leaves **agent-ready** tasks: every task it produces is
`groomed` (it carries at least one acceptance criterion) **and** assigned to an
agent, so it lands in the agent bucket (`ft task next --for <agent>`). Promoting
an idea creates a linked task that is groomed and assigned in the same step.
Work that is groomed but unassigned, or assigned but ungroomed, still counts as
human-next. Genuinely human items (product decisions, human testing and review)
stay human on purpose and are named in the report.

## Deterministic report

End every session with a report whose top-level sections are, in this order:

1. `Summary`
2. `Per item`
3. `Product questions (grill)`
4. `Deferred (for stakeholders)`
5. `DAG changes`

The report template is versioned data (embedded in `ft`), not prose you invent; its
section set and ordering are fixed and asserted by a test.

## Deferred-questions file

The deferred-questions file has two sections, in this order:

1. `Questions` - one entry per deferred product question, with its item, options,
   owner, and the date it was raised.
2. `Resolved` - questions stakeholders have since answered.

## DAG hygiene

Ideas are immutable capture: never mutate an idea's kind, and never mark it done to
mean promoted. Promotion creates a linked task. Keep the DAG honest: express real
blocking with dependencies, drop edges that no longer block, and never leave a
discovered follow-up undocumented.

## Commands

```sh
ft task get <task>                                  # read one item in full
ft task note create <task> -b "Decision: ..."       # record an engineering call
ft task create -p <project> -t "..." --groomed --acceptance "<observable result>"
ft task update <task> --groomed --acceptance "<observable result>"
ft task assign <task> --actor <agent>               # so it lands in the agent bucket
ft idea create -p <project> -t "..." -b "..."       # capture a new idea
ft idea promote <idea>                              # create the linked task
ft task next --groomed                              # buildable, agent-ready work
ft graph render --project <project>                 # the whole DAG as text
```
