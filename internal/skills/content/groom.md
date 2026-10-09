# groom

The grooming session turns raw ideas into a groomed, agent-ready task graph. This
skill is the session protocol: who decides what, how to grill, how to classify
answers, how to split, and the deterministic report every session ends with.

You are the **team lead**; the human is the **product owner (PO)**.

## Launch

`ft groom [item...]` runs the session. It resolves the **scope** (the named
items, or by default every open idea plus every open ungroomed task), injects a
**kickoff**, runs the durable prompt task-less over every repository of the
project, and captures the five outputs.

```sh
ft groom --sandbox local --harness opencode --allow-host
ft groom <item> <item> --sandbox local --harness opencode --allow-host
ft groom --unattended --sandbox local --harness opencode --allow-host
ft groom -p <project> --sandbox local --harness opencode --allow-host
```

`--project` scopes the whole session to the named project's store: the scope is
read from it, the captured outputs and the session manifest are recorded into it,
and the launched session's child `ft` reads it. A cross-project `ft groom -p b`
from an `a`-configured checkout therefore records into `b`, and `ft groom
list/show -p b` find it. Without `--project` the configured project is used.

On a terminal the session runs **interactive**: `ft groom` launches the agent
attached to the terminal (the OpenCode TUI) with the kickoff, so the PO can
answer the grill in the session. `--unattended`, or a piped or redirected
stdin/stdout, runs it **headless** instead. The recorded `mode` reflects this:
`interactive` (attached, a PO is present), `unattended` (no PO), or `headless`
(no terminal). Interactive mode needs a backend that can attach a terminal:
`local` does; `openshell` and `docker` report it unsupported and `ft groom`
tells you to rerun with `--unattended`.

A **headless** session narrates live progress on stderr, so a long grill is
visibly alive: `ft: groom session <id> (sandbox=<b> harness=<h>) started`, then a
periodic `still running (Ns)` heartbeat. It is written only when stderr is a
terminal and output is text; an interactive session, `-o json|yaml`, or a
non-terminal stderr prints none.

`--sandbox` and `--harness` are optional: `ft groom` resolves them like `ft run`
(flag, env, project `[run]`, machine `[run]`), prompting once on a terminal when
unset; choosing `local` in that prompt asks to opt in (default no) and records
`run.allow_host` in the user config. A machine `[run]` table lets a bare
`ft groom` run with no flags.

Like `ft run`, a repository registered with a local `Path` (not a `URL`) is used
**in place**, so a backend on the workdir (`local`, `docker`) can modify the
source checkout; `ft groom` warns on stderr naming the resolved checkout.

The prompt is durable repo data at `docs/grooming/prompt.md` - never an
ephemeral temp file; override it with `--prompt-file`. `ft groom` appends the
kickoff to the prompt. The kickoff names:

- the scope: every item's id, kind, and title;
- the workspace-relative **report** path and **deferred-questions** path, under
  `.ft-groom/<session-id>/`, named `report.md` and `deferred-questions.md`;
- the workspace-relative **feature spec**, **plan**, and **tech-design** paths,
  under the same directory, named `spec.md`, `plan.md`, and `tech-design.md`;
- the section contract of each document, in order.

Write the report to the report path, the deferred questions to the
deferred-questions path, and the feature documents to the spec, plan, and
tech-design paths, relative to your working directory. When the session
finishes, `ft groom` reads all five files back out of the workspace and copies
them to the durable session dir under the project data dir
(`grooming-sessions/<session-id>/`), recording each as an artifact (`ft doc
list`; the spec is kind `spec`, the rest `doc`); a session that misses any
output fails with a clear error. The paths are inside the workspace on purpose:
a sandboxed backend (openshell, docker) allows writes only there, and `ft groom`
captures them before the environment is torn down.

## Read a session

`ft groom` records each finished session under `grooming-sessions/<session-id>/`
so it can be read back:

```sh
ft groom list                     # past sessions: id, date, mode, scope, produced counts
ft groom list -p <project>
ft groom show <session>           # the report, the deferred questions, and the tasks it produced
ft groom show <session> -p <project>
```

`ft groom list` prints a table (`SESSION`, `DATE`, `MODE`, `SCOPE`, `PRODUCED`) and,
as `-o json|yaml`, a list of objects with `session`, `created` (RFC3339), `mode`,
`project`, `scope` (the item ids), and `produced` (the task ids). It defaults to the
configured project; `--project` selects another.

`ft groom show <session>` prints the session metadata, the captured report, the
deferred questions, the feature spec, plan, and tech design, and the tasks the
session produced, read live from the graph (their current kind, status, and
title; a task since deleted is named by id alone). As `-o json|yaml` it adds
`report_body`, `deferred_body`, `spec_body`, `plan_body`, `tech_design_body`,
and a `produced` list of `{task_id, kind, title, status}`.

Both commands default to the configured project. `--project` reads another
project's sessions from the store the machine config registers for it, so a
non-default project's session is reachable without changing directory or editing
the project file; asking for a project a session does not belong to is an error.

The produced set is a window diff - the tasks that appeared in the project while
the session ran - not strict authorship. In a single-operator session that is
the session's own work; if another writer creates a task concurrently, it is
included too.

## Unattended mode

`ft groom --unattended` runs with no product owner and no interactive channel. The
kickoff tells the session to defer every product question to the
deferred-questions file, record engineering calls as notes, and suspend the
pre-answer timing guard so it still promotes, splits, and marks produced tasks
groomed and assigned. After the run `ft groom` requires every scoped item to be
agent-ready or named in the deferred-questions file; an item left neither fails
the run. Use it for an automated loop; a human session omits the flag, runs
attached on a terminal, and grills the PO as usual.

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

## Feature documents

Alongside the report, every session emits three feature documents from fixed
templates: a **spec** (what and why), a **plan** (how it lands), and a **tech
design** (how it is built). Like the report, each is versioned data embedded in
`ft` - its sections and ordering are fixed and asserted by a test - and the
session writes it to the matching path in the kickoff. They cover the feature
the session defines and are the input to the independent architecture review,
which reads them (`ft doc` artifacts or the session dir) and judges consistency,
contradiction, overcomplication, and cross-cutting impact.

The feature spec sections, in this order:

1. `Summary`
2. `Problem`
3. `Goals`
4. `Non-goals`
5. `Requirements`
6. `Acceptance`

The feature plan sections, in this order:

1. `Summary`
2. `Milestones`
3. `Tasks`
4. `Dependencies`
5. `Verification`
6. `Rollout`

The feature tech-design sections, in this order:

1. `Summary`
2. `Context`
3. `Design`
4. `Interfaces`
5. `Data`
6. `Cross-cutting impact`
7. `Risks`

## Architecture review

After the docs are captured, the feature gets an independent architecture review before it is
built. The loop dispatches the architecture reviewer (`architecture-reviewer-<session>`) on the
report, spec, plan, and tech design; the reviewer reads only those documents with a project-wide
view, records its findings as notes/tasks on the origin item, and records one tech-design verdict:

```sh
ft groom review <session> --verdict approve -f review.md
ft groom review <session> --verdict approve-with-changes -f review.md
ft groom review <session> --verdict needs-rework -f review.md
ft groom review -p <project> <session> --verdict needs-rework -f review.md
```

`ft groom review` records the review as a session artifact and a note on each origin item, and
stores the verdict on the session. A **needs-rework** verdict blocks the feature from build: the
produced tasks are blocked until a later review approves, and an approving review unblocks them.
`ft groom show <session>` prints the verdict and the review body.

## DAG hygiene

Capture kinds are immutable: never mutate an idea's or a bug's kind, and never
mark one done to mean refined. Grooming promotes an idea, triage promotes a bug,
and both create a linked task. Keep the DAG honest: express real blocking with
dependencies, drop edges that no longer block, and never leave a discovered
follow-up undocumented.

## Commands

```sh
ft task get <task>                                  # read one item in full
ft task note create <task> -b "Decision: ..."       # record an engineering call
ft task create -p <project> -t "..." --groomed --acceptance "<observable result>"
ft task update <task> --groomed --acceptance "<observable result>"
ft task assign <task> --actor <agent>               # so it lands in the agent bucket
ft idea create -p <project> -t "..." -b "..."       # capture a new idea
ft idea promote <idea> --acceptance "<observable result>" --actor <agent>  # promote groomed + assigned
ft task next --groomed                              # buildable, agent-ready work
ft graph render --project <project>                 # the whole DAG as text
```
