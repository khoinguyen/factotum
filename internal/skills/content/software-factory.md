# software-factory

The end-to-end recipe for running a project as a **software factory**: turn raw captures into a
groomed task graph, design and review the feature, break it into tasks, build each task with the
chief's builder/reviewer loop, verify with QA, and roll out.

This is the manual, invokable recipe — point a fresh agent at it and it drives the loop. It ties the
individual role skills together rather than restating them; each role's detailed charter lives in
its own skill (see **Roles**). It is **project-agnostic**: every command takes a project id
(the `-p <project>` placeholder) or falls back to the configured default project, so the same
recipe works for any project. Run `ft skill get <name>` to load a role skill.

## Roles

| Phase | Skill | Who runs it |
| --- | --- | --- |
| Groom | `groom` | the team lead, with the product owner |
| Architecture review | `architecture-reviewer` | a fresh `architecture-reviewer-<feature>` |
| Build | `chief` | the chief orchestrator |
| Build one task | `single-task-builder` | a fresh `builder-<task>` |
| Review a PR | `single-task-reviewer` | a fresh `reviewer-<task>` |
| QA | `single-task-qa` | a fresh `qa-<task>` |

## The loop

1. **Capture** — record every idea or defect the moment it surfaces, as a non-executable item.
2. **Groom** — grill the product owner, split the work, and emit the feature spec, plan, and tech design.
3. **Architecture review** — review the design independently; a failing verdict blocks the build.
4. **Breakdown** — make the DAG honest: dependencies, one verification gate per task, a release milestone.
5. **Build** — the chief's loop: a fresh builder and reviewer per task, each in its own worktree.
6. **Review** — the reviewer builds the branch, runs CI, exercises it, and probes the edges.
7. **QA** — a fresh QA agent exercises the feature end-to-end on throwaway labs.
8. **Rollout** — close milestones, verify the graph, and store the durable learnings.

## Phase details

### 1. Capture

Write the thought or defect down immediately; never hold it in chat. Captures are immutable history:

```sh
ft idea create -p <project> -t "A half-formed thought" -b "context"
ft bug create -p <project> -t "It crashes on save" -b "steps to reproduce"
```

`ft idea` and `ft bug` never enter the ready set; grooming or triage turns them into a linked,
executable task.

### 2. Groom

Run the grooming session over the captures (or the whole open backlog). The `groom` skill is the
session protocol: it grills the product owner on product questions only, records engineering calls as
notes, splits the work into 1 task = 1 PR, and writes three feature documents — a spec, a plan, and a
tech design — alongside a deterministic report.

```sh
ft groom -p <project> --sandbox local --harness opencode --allow-host
ft groom <item> <item> --unattended --sandbox local --harness opencode --allow-host
```

A completed session leaves every produced task **groomed** (it carries at least one acceptance
criterion) and assigned, so it lands in the agent bucket. Read a finished session back, and inspect
the agent-ready queue:

```sh
ft groom list -p <project>
ft groom show <session>
ft task next -p <project> --groomed
```

### 3. Architecture review

Before any of the feature is built, review the design independently. Dispatch the
`architecture-reviewer` skill as `architecture-reviewer-<feature>` on the session's spec, plan, and
tech design. It records its findings on the origin item and exactly one tech-design verdict:

```sh
ft groom review <session> --verdict approve -f review.md
ft groom review <session> --verdict approve-with-changes -f review.md
ft groom review <session> --verdict needs-rework -f review.md
```

A `needs-rework` verdict blocks the feature from build: `ft groom review` blocks the tasks the
session produced. Fix the findings and re-review; `approve` or `approve-with-changes` unblocks the
build. Never skip this to move faster.

### 4. Breakdown

Make the DAG honest before building: real blocking as dependencies, one verification gate per task,
a milestone as the release gate. Split a task too large for one reviewable PR, and correct
acceptance criteria as the design firms up:

```sh
ft graph render -p <project> -f agent
ft task list -p <project> --groomed
ft task create -p <project> -t "Short imperative title" --groomed --acceptance "observable result" --dep <blocking-task>
ft task update <task> --groomed --acceptance "observable result"
```

### 5. Build

The `chief` skill drives the loop without filling one context: it picks the highest-ranked ready
task and dispatches a fresh `single-task-builder` and a fresh `single-task-reviewer` per task, each
in its own worktree, wired to each other over cmux.

```sh
ft task next -p <project> --groomed --for <agent>
ft task get <task>
```

The builder runs one task through its lifecycle test-first (RED, implement, GREEN, `mise run ci`):

```sh
ft task start <task>
ft task review <task>
ft task done <task>
```

Capture every follow-up the moment it is found, so the graph never goes stale:

```sh
ft task note create <task> -b "Decision: ..." --link issue=https://...
ft task create -p <project> -t "Short imperative title" --dep <blocking-task>
```

### 6. Review

The builder hands the PR to the `single-task-reviewer` agent. The reviewer builds the branch, runs
CI, exercises the change, probes the edge cases, and checks it against the PR body; builder and
reviewer converge (fix or justified pushback). The chief merges only an approved PR with green CI,
and never merges on fakes alone. A review that flags a real, unverified path is escalated.

### 7. QA

For end-to-end, integration, or UX verification, the chief dispatches a fresh `single-task-qa`
agent — never a build agent. QA exercises the feature for real on throwaway labs and reports
PASS/FAIL/not-available per item; a FAIL becomes a new task with acceptance criteria. Track the
testing work like any other:

```sh
ft task create -p <project> -t "QA: <feature> end-to-end" --groomed --acceptance "observable result"
```

### 8. Rollout

Close the loop: mark the release milestones done to unblock dependents, verify the whole graph, and
store the durable learnings a later session would otherwise rediscover.

```sh
ft milestone create -p <project> -t "MVP"
ft milestone done <milestone>
ft graph render -p <project> -f summary
ft memory create -p <project> -t "Title" --brief "when this applies" -b "what to remember"
```

The chief owns the release decision; a subagent reports, it never merges on its own.

## Operate and observe

```sh
ft serve
ft doc search "<query>"
ft memory search "<query>"
ft task context <task>
ft skill list
ft skill get <name>
```

## Commands

The whole recipe in one block:

```sh
ft idea create -p <project> -t "..." -b "..."                     # capture an idea
ft bug create -p <project> -t "..." -b "..."                      # capture a defect
ft groom -p <project> --unattended --sandbox local --harness opencode --allow-host
ft groom show <session>                                           # report, docs, produced tasks
ft groom review <session> --verdict approve -f review.md          # needs-rework blocks the build
ft graph render -p <project> -f agent                             # the DAG as text
ft task next -p <project> --groomed --for <agent>                 # the buildable queue
ft task create -p <project> -t "..." --groomed --acceptance "..." --dep <blocking-task>
ft task note create <task> -b "Decision: ..."                     # record what you learned
ft task start <task>                                              # build; then review, then done
ft milestone done <milestone>                                     # unblock the rollout
ft memory create -p <project> -t "..." --brief "..." -b "..."     # keep durable knowledge
```

## Ground rules

- One task = one PR; one reviewable, independently verifiable unit.
- The body is the spec; notes are history. Fold decisions into the body.
- Never point a branch build at the shared database; use a throwaway store.
- Never merge on fakes: a dependency, service, or model exercised only through fakes is escalated.
- Report, do not file — the chief owns follow-up tasks and the merge.
