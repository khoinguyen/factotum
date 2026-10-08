# ft up — design

Status: **design for review** (t-n4r7ecaytb). No implementation in this change.

`ft up` is the software-factory daemon: one long-lived process that serves the dashboard and
capture write side, watches the capture path, and drives each captured idea through the factory
pipeline — groom, architecture review, build, QA, rollout — stopping at every human gate and
surfacing it. It is the automation spine for the OSS single-user factory idea (t-mcz23lijcs,
milestone t-dvluzscb5k): today every phase is a manual, invokable step (the `software-factory`
skill); `ft up` supervises the same steps unattended and makes the human the only thing in the
loop.

This document pins the four things the task asks for, each in its own section: the **daemon**
(§3), the **capture trigger** (§4), the **pipeline state machine** (§5), and **where rollout fits**
(§9) — plus how human gates are surfaced (§6) and the store/CLI/dashboard impact (§7, §8).

## 1. Goals and non-goals

Goals.

- A single `ft up` process that embeds `ft serve` (dashboard, capture, msg transport) and adds the
  factory controller.
- A capture stored through the serve write path starts a pipeline automatically, config-gated with
  an opt-out.
- The pipeline runs each factory phase in order, in-process or as a supervised agent run, and
  reflects every transition into the store and the live dashboard.
- Human gates (product questions, a `needs-rework` design, a merge, the release milestone, a
  rollout promotion) are first-class: the daemon parks on them, names them, and offers the exact
  command to clear one.
- Durable, restart-safe pipeline state: a `ft up` restart resumes pipelines where they were.

Non-goals (out of scope for this milestone).

- **The rollout mechanics** (environments, deploy, promote, rollback): that is t-3p53uzgcve. `ft up`
  provides the stage, the gate, and the hook; it does not deploy anything (§9).
- Multi-user, multi-tenant, or cloud auth. `ft up` is single-operator and inherits the serve
  token's single-operator trust boundary.
- A new agent orchestration engine. The phases stay agent skills; the daemon supervises them.
- Auto-merging by default. The merge stays a human gate unless an operator explicitly opts in
  (open question §13).
- Serving all projects in one daemon: `ft up` is project-scoped (one project per process) for the
  first cut, mirroring capture's need for a single target.

## 2. Context: what already exists

- **Capture write path.** `POST /api/capture` authenticates with the shared serve token, derives a
  title from the first line, stores an idea or bug via `app.TicketService.Add`, and returns the
  detail URL (`internal/serve/capture.go`). Nothing else happens: the capture is inert history.
- **Grooming.** `ft groom [item...]` runs a task-less `RunProject` session scoped to the named
  items, or by default to the project's open ideas plus open ungroomed executable tasks (a bug's
  refine verb is `triage`, not groom). It writes a durable session manifest
  (`internal/groom/session.go`) and records the feature's spec/plan/tech-design as artifacts.
  `ft groom review` records the architecture verdict; a `needs-rework` verdict **blocks** the
  produced tasks from build.
- **The DAG loop.** `ft run --goal <task|milestone>` drives `RunLoopService`
  (`pkg/app/runloop.go`): it repeatedly runs the highest-ranked agent-ready, groomed, on-path task
  until the goal resolves, work stalls, or a budget is exhausted. A **milestone is a human gate**:
  the loop completes its prerequisites and stops `no_ready_work` rather than closing it.
- **The build loop.** Today the builder/reviewer/QA loop is the `chief` skill, an agent that
  dispatches fresh `builder-<t>`/`reviewer-<t>`/`qa-<t>` agents per task. It is orchestration, not a
  library call.
- **Events and the live dashboard.** Every mutation appends a `core.Event`; the dashboard polls the
  newest event id and pushes SSE `update`s (`internal/serve/serve.go`, `broker.go`). `ft serve` is
  the read side plus token-gated capture and the `/api/msg/*` transport.
- **Messages.** `ft msg` is a durable mailbox with a register/claim/ack receiver protocol
  (`docs/msg/design.md`), already used by the chief loop to address peers. `ft up` reuses it to
  notify the operator.

The gap `ft up` closes: capture is a dead end and every later phase is a manual command. Nothing
owns "this capture is now a feature in flight, and here is what it is waiting on".

## 3. The daemon

`ft up` is one process with three parts:

1. **The serve server**, started in-process from `serve.New` exactly as `ft serve` does — the
   dashboard, the token-gated capture write side, and the `/api/msg/*` transport.
2. **The controller** — the pipeline state machine (§5): it accepts captures (§4), owns each
   pipeline's phase, runs stages, and parks on gates.
3. **The stage runners** — the ports that execute one phase (§8): a groom session, an
   architecture-reviewer run, a chief build loop, a QA run, a rollout step.

`ft up` is **`ft serve` plus the controller**, not a second program to run beside it. We keep
`ft serve` as it is (read + write, no controller) and give `ft up` its own command that embeds the
server, so the dashboard-only user never pays for the controller and the factory user has one
process to run and one to stop. This settles the "who owns serve" question: the daemon does, in
its own command.

Process model.

- One controller worker per project, concurrency 1 by default (a configurable cap, §12), so two
  captures cannot run two grooms over the same graph at once.
- Graceful shutdown on SIGINT/SIGTERM: stop accepting captures, let the running stage finish or
  record it as interrupted, persist every pipeline, then exit. Restart reconciles new captures (§4)
  and re-owns every non-terminal pipeline (§5, "Restart and resume").
- The daemon never blocks on an interactive prompt: a stage that needs a human becomes a gate
  (§6). When `ft up` runs attached to a terminal it may run the groom session interactively (the
  PO is present); headless it runs unattended.

## 4. The capture trigger

The trigger is the **capture write path**: a capture stored through `/api/capture` enqueues a
pipeline. Two mechanisms, primary and backstop, mirroring the msg design's "poll is the contract,
the fast path is an optimization".

- **Primary — in-process hook.** After `handleCaptureSubmit` stores the idea/bug successfully, it
  calls `controller.Submit(capture)`. This is exactly-once, immediate, and needs no polling: the
  daemon already owns the write path, so it knows the moment a capture lands. The controller
  creates a `Pipeline` in state `queued` keyed by the capture's ticket id.
- **Backstop — reconciliation.** On startup, and on a slow tick, the controller scans the store for
  **open captures (ideas and bugs) with no pipeline and no linked origin task** and enqueues them.
  This catches captures stored while the daemon was down, and captures stored by another process (a
  CLI `ft idea create`, a second `ft serve`). It is idempotent: a capture already in a pipeline is
  skipped, so a restart never duplicates work.

Config and opt-out (t-ywh4btshch's acceptance).

- `[up] auto_groom` (default **true** under `ft up`) enables the trigger; `--no-auto-groom` or
  `auto_groom = false` disables it, so capture stays a pure store write and the pipeline is started
  by hand (`ft up start <capture>`).
- A capture is skipped when it is already promoted (a linked executable task exists), already in a
  pipeline, or carries the `no-auto` label. The daemon records why it skipped.

The pipeline is keyed by **capture id**, so the trigger, the backstop, and a manual start all
converge on one pipeline per capture. Captures are immutable history (an idea never becomes a
task in place); the pipeline is the mutable, in-flight record beside it.

## 5. The pipeline state machine

A `Pipeline` is a new pure entity in `pkg/core` (style of `Message`/`Run`), one per captured
idea/bug. It holds the phase, the gate, and links to what the phases produced.

```go
type PipelineID string

type PipelineState string
const (
    PipelineQueued    PipelineState = "queued"    // enqueued, not started
    PipelineGrooming  PipelineState = "grooming"  // groom session running
    PipelineReview    PipelineState = "review"    // architecture review running
    PipelineBuilding  PipelineState = "building"  // build loop running
    PipelineQA        PipelineState = "qa"        // QA running
    PipelineRollout   PipelineState = "rollout"   // release/rollout running
    PipelineDone      PipelineState = "done"      // terminal success (immutable)
    PipelineCancelled PipelineState = "cancelled" // terminal, operator-cancelled (immutable)
    PipelineFailed    PipelineState = "failed"    // parked after a stage failure; retryable
    PipelinePaused    PipelineState = "paused"    // operator hold; resumable
)

// Gate is the human action, if any, the pipeline is waiting on. It is
// orthogonal to State: a gated pipeline stays in its phase (or pauses) and
// resumes when the gate is cleared.
type GateKind string
const (
    GateNone           GateKind = ""                 // no gate
    GateGroomQuestions GateKind = "groom_questions"  // deferred PO questions (advisory)
    GateDesignRework   GateKind = "design_rework"    // needs-rework verdict (blocks build)
    GateMergeApproval  GateKind = "merge_approval"   // PR approved, awaiting merge
    GateRelease        GateKind = "release"          // release milestone to close
    GateRollout        GateKind = "rollout"          // deploy/promote approval
)

type Pipeline struct {
    ID         PipelineID
    ProjectID  ProjectID
    CaptureID  TicketID    // the origin idea/bug
    Milestone  *TicketID   // the release gate this pipeline drives toward
    State      PipelineState
    Gate       GateKind
    Session    string      // groom session id, once grooming has run
    Produced   []TicketID  // tasks the groom session created
    Error      string      // last failure reason
    Attempts   int
    CreatedAt  time.Time
    UpdatedAt  time.Time
}
```

### Transitions

```
  queued ─▶ grooming ─▶ review ─▶ building ─▶ qa ─▶ rollout ─▶ done
                │           │          │            │
                │           │          │            └─ gate: release (milestone closed), then rollout
                │           │          └─ gate: merge_approval (blocking)
                │           └─ gate: design_rework (blocking; needs-rework)
                └─ gate: groom_questions (advisory; does not block)

  A gate holds its phase; clearing the gate resumes the same phase.
  pause/resume at any phase. A stage that fails after its retry budget → failed
  (parked; `ft up retry` returns to the phase, or `ft up cancel`).
```

Rules.

- `queued → grooming`: the controller starts the groom stage. Grooming produces the session and the
  feature tasks; on success `grooming → review`.
- **Gates are orthogonal to phases.** A pipeline in any phase may set `Gate` and stop advancing.
  The phase is what it was doing; the gate is what it is waiting for. Clearing the gate resumes the
  same phase.
- **Blocking vs advisory gates.** `GateDesignRework`, `GateMergeApproval`, `GateRelease`, and
  `GateRollout` are blocking: the pipeline does not advance past them until a human clears them.
  `GateGroomQuestions` is advisory: an unattended groom still finishes agent-ready, so the pipeline
  proceeds to `review`/`building` while the questions are surfaced for the human to answer.
- `review → building` only on `approve`/`approve-with-changes`; `needs-rework` sets
  `GateDesignRework` and does **not** advance — reusing `ft groom review`'s existing block, not
  reinventing it.
- `building → qa` when every produced task is done (or resolved under the policy). `qa` is
  dispatched per t-zdohwjheha.
- `qa → rollout` on pass; a QA failure returns to `building` with a new task, or parks `failed`
  after the retry budget.
- `rollout → done` after the milestone is closed and the rollout stage (if configured) completes.
- A stage that fails after its retry budget sets `failed` and parks; the operator retries
  (`ft up retry`, which returns it to the phase it failed in) or cancels it (`cancelled`).
- `paused` is an operator hold; `resume` returns to the phase it held. `failed` and `paused` are
  non-terminal and reversible; `done` and `cancelled` are the only immutable terminal states.

The state machine is durable: every transition writes the `Pipeline` and appends an event (§7), so
the dashboard and `ft up status` show exactly where each feature is, and a restart resumes.

### Restart and resume

Reconciliation (§4) only enqueues captures that have no pipeline; the controller separately
**re-owns every non-terminal pipeline** on startup, so a process that died mid-pipeline does not
leave it parked forever:

- On startup, list every pipeline whose state is not `done`/`cancelled` and atomically claim it for
  its project (the claim is what makes exactly one controller own it).
- `queued` resumes by starting its first stage. A pipeline in an in-flight phase (`grooming`,
  `review`, `building`, `qa`, `rollout`) resumes by **re-running its current stage**.
- Stages are written to be **idempotent and resumable**: each checks the durable artifacts it would
  produce — the groom session manifest, the recorded review verdict, the produced tasks and their
  PRs, the QA result — and continues from the furthest point reached instead of redoing side
  effects. A stage whose externally dispatched agent run is still live is re-attached or awaited;
  one whose run is gone is re-run.
- `failed` and `paused` are **not** auto-resumed: they stay parked until the operator runs
  `ft up retry`/`resume`. A restart never silently restarts work the operator deliberately held.

This is the same recovery shape as `ft msg`'s lease/requeue sweep (`docs/msg/design.md` §6): the
durable claim, not the live process, is the source of truth for ownership.

## 6. Human gates

The daemon's whole point is to do everything that does not need a person and to stop exactly where
one is needed. A gate is surfaced three ways, all reading the same durable `Pipeline`:

- **The dashboard.** A `/factory` view lists pipelines with `state`, `gate`, the origin capture,
  the produced tasks, and the **exact command to clear the gate** (for example
  `ft up answer <pipeline> -f answers.md`). It is SSE-live like the rest of the dashboard.
- **The CLI.** `ft up status` prints the same, and `ft up list` is the table. The `Next:` block on a
  gated pipeline names its clearing command, matching the repo's hint convention.
- **Notification (opt-in).** Opening a **blocking** gate sends one `ft msg` to the configured
  operator actor (reusing the msg hub), and optionally an ntfy/desktop ping. Advisory gates do not
  notify by default; the dashboard shows them.

Clearing a gate is an explicit operator action, so it is auditable:

```
ft up answer <pipeline> -f answers.md     # answer deferred groom questions, resume grooming
ft up approve <pipeline>                  # accept a design after a rework cycle (re-records review)
ft up merge <pipeline>                    # clear merge_approval after merging the PRs
ft up release <pipeline>                  # close the release milestone (or ft milestone done)
ft up promote <pipeline>                  # approve the rollout promotion
```

Each clears its gate and resumes the phase; the daemon never clears a blocking gate on its own.

## 7. Store, events, and the dashboard

### Entity and port

`Pipeline` is a first-class entity with a `PipelineRepo` on `Backend`, exactly like `Message`/`Run`
in `docs/msg/design.md`: `Create`/`Get`/`List`/`Update`, plus a **claim** primitive so the single
controller worker takes a queued pipeline atomically (memory mutex, sqlite transaction, jsonfile
write lock, jsondir lock file). `pkg/store/conformance` grows a `Pipeline` case set: round-trip,
`ErrAlreadyExists`/`ErrNotFound`, `Validate` (empty id/capture/project, unknown state, unknown
gate), list filters (project, state, gate), claim concurrency (one winner), and terminal-state
immutability for `done`/`cancelled` only (`failed`/`paused` stay mutable so `retry`/`resume` can
move them).

Additive schema only. sqlite gets a forward migration; memory a map; jsonfile a key; jsondir a
file. No existing entity changes.

### Events

New event kinds, appended through the existing path so the broker/SSE lights up for free:
`pipeline.queued`, `pipeline.stage_started`, `pipeline.stage_finished`, `pipeline.gate_opened`,
`pipeline.gate_cleared`, `pipeline.done`, `pipeline.failed`. `Event.TicketID` is set to the origin
capture, so a pipeline also surfaces on the capture's detail page.

### Dashboard snapshot

The snapshot gains a `pipelines` list (project-scoped), and the app a `/factory` route. A pipeline
row shows the capture, phase, gate, produced tasks, and the clearing command; the app refetches on
every `/events` update, so a capture appears as a pipeline immediately after it is stored.

## 8. Stages and the stage-runner port

A stage is the unit of work the controller advances. The controller depends on a small port, not on
the skills or commands directly, so the machine is testable with fakes and the phases stay
pluggable:

```go
// StageResult reports what one stage did; Gate opens a human gate (empty = none).
type StageResult struct {
    Done   bool
    Gate   core.GateKind
    Detail string
}

// StageRunner runs one phase of one pipeline. Implementations dispatch a
// harness/agent run or an ft command; the controller only sequences them.
type StageRunner interface {
    Run(ctx context.Context, p *core.Pipeline) (StageResult, error)
}
```

Production runners, one per phase:

- **groom** — runs the `ft groom` session (`internal/groom` + `RunService.RunProject`) **scoped to
  the pipeline's capture id** (`ft groom <capture>`, not a bare `ft groom`, which would scope to
  the whole backlog and excludes bugs), records the session, and returns the produced tasks.
  Interactive when a terminal PO is attached, else unattended (which defers product questions →
  `GateGroomQuestions`).
- **review** — dispatches an `architecture-reviewer` agent run over the session's docs and records
  `ft groom review --verdict`; `needs-rework` → `GateDesignRework`.
- **build** — dispatches the `chief` agent run, which drives the builder/reviewer loop and files
  follow-ups. This preserves the merge gate and the "report, do not file" behavior; the simpler
  alternative is `ft run --goal <milestone>`, noted in §14.
- **qa** — dispatches a `single-task-qa` agent run when all produced tasks are done
  (t-zdohwjheha).
- **rollout** — the rollout stage (§9).

The controller owns ordering, retries, budgets, and the gate; a runner owns only one phase. This is
the same split as `LoopRunner` in `pkg/app/runloop.go`: the loop depends on the behavior, the
adapter is a fake in tests.

## 9. Where rollout fits

Rollout is the **last pipeline stage**, and its human gate already exists in the graph: the release
**milestone**. `ft up` does not invent a release mechanism; it reuses the milestone gate the DAG
loop already honors (`pkg/app/runloop.go` stops rather than closing a milestone).

1. When a pipeline starts, `ft up` attaches (or creates) a **release milestone** for the feature
   and records it as `Pipeline.Milestone`. The milestone **depends on** the groom-produced tasks
   (the tasks are its prerequisites, the edge the DAG loop expects), so the loop drives the tasks
   and the milestone is the single release gate. Rollout work depends on the milestone, so it is
   blocked until the gate is closed.
2. The build and QA stages drive the produced tasks to done. Once the tasks resolve, the milestone's
   readiness opens; the pipeline enters `rollout` with `GateRelease`: the milestone is the human
   gate, and closing it (`ft milestone done` / `ft up release`) is the release decision.
3. On close, the **rollout stage** runs: the environments/deploy/promote/rollback work of
   t-3p53uzgcve. That task owns the mechanics; `ft up` provides the stage, the `GateRollout`
   promotion gate, and the config (`[up] rollout`). With no rollout configured, the stage is a
   no-op and the pipeline goes straight to `done`.
4. `done` stores the durable learnings as `ft memory` (the software-factory skill's step 8) so the
   next feature starts from them.

The edge direction matters: the milestone is **downstream** of its prerequisites (it depends on
them), matching `pkg/app/runloop.go` — `agentReadyOnPath` follows a goal's deps, so a milestone that
depended on nothing would be `goal_reached` immediately and its tasks would never run. The reverse
edge ("tasks depend on the milestone") would gate the build behind a gate that can be closed before
anything is built.

So rollout fits as: **a stage gated by the release milestone, implemented elsewhere, orchestrated
here.** The daemon never deploys on its own; `GateRelease` and `GateRollout` keep the release
decision human.

## 10. CLI surface

```
ft up [--bind host:port] [-p project] [--no-auto-groom] [--unattended] [--max-parallel N]
ft up status [--pipeline <id>] [-o json|yaml]     # phases and open gates
ft up list                                        # pipelines table
ft up start <capture>                             # enqueue a pipeline by hand
ft up answer <pipeline> -f <file>                 # answer deferred groom questions; resume
ft up approve <pipeline>                          # accept the design after a rework cycle
ft up merge <pipeline>                            # clear merge_approval
ft up release <pipeline>                          # close the release milestone
ft up promote <pipeline>                          # clear rollout_approval
ft up pause|resume|retry|cancel <pipeline>        # operator control
```

- Output follows the repo shapes: single results print `key: value` lines ending `project`, `repo`;
  lists print a table; `-o json|yaml` are the machine interfaces.
- `ft up` (the daemon) starts the server and controller; the verbs are operator control over the
  durable state, so they work while the daemon runs or against a stopped store.
- `ft serve` is unchanged; `ft up` is additive.

## 11. Config

A machine-scoped `[up]` table (host settings, like `[serve]`/`[run]`), merged `env > project file >
user file > defaults`:

```toml
[up]
auto_groom = true          # capture write starts a pipeline (the opt-out)
unattended = true          # groom/agents headless; false = attach the PO when on a terminal
operator   = "act-..."     # human actor to notify at a blocking gate
notify     = "msg"         # none | msg | ntfy
max_parallel = 1           # pipelines in flight
budget     = 25            # max agent runs per pipeline before parking failed
# rollout config is owned by t-3p53uzgcve
```

Only `auto_groom`/`unattended` are read from a committed project file (project intent); the rest
are machine-scoped, so a committed file never carries an operator id, a notification target, or a
path.

## 12. Cross-cutting impact

- **`pkg/core`** — new `Pipeline` entity + enums (pure; no infra imports).
- **`pkg/store`** — `PipelineRepo` on `Backend`, implemented by all four adapters and covered by
  the conformance suite. Additive schema + sqlite forward migration.
- **`internal/serve`** — the capture handler gains one call to `controller.Submit`; the snapshot
  gains `pipelines`; a `/factory` view is added. `serve.New` gains an optional controller hook
  (nil for plain `ft serve`).
- **`internal/cli`** — a new `up` command tree; `serve` unchanged.
- **`internal/groom` / `pkg/app/runloop`** — reused as-is by the stage runners; `ft groom review`'s
  block and the milestone gate are honored, not duplicated.
- **Events** — the new pipeline kinds join `core.EventKind.Valid`; the dashboard lights up through
  the existing broker.
- **Skills** — `internal/skills/content/software-factory.md` gains the daemon path; whether a
  dedicated `up`/operator skill is needed is a follow-up (a CLI-surface change requires the
  embedded-skill update per AGENTS.md).
- **Config** — the `[up]` table above.

## 13. Open questions for Khoi (product calls)

1. **Merge gate.** Default to `GateMergeApproval` (human merges), or let a single operator opt into
   auto-merging approved PRs with green CI? This design defaults to the human gate.
2. **Groom interactivity.** When `ft up` runs on a terminal with a PO present, should groom attach
   the PO by default, or always run unattended and only surface deferred questions?
3. **Notification channel.** `ft msg` to the operator actor, ntfy, desktop, or dashboard only?
4. **Concurrency and budget.** `max_parallel = 1` and a per-pipeline agent-run budget of 25 —
   accept, or different numbers?

## 14. Alternatives considered and rejected

- **Poll-only trigger.** Rejected as the primary: the daemon already owns the capture write path, so
  an in-process hook is exact and immediate. Poll remains the durable backstop (reconciliation),
  same as msg's poll-is-the-contract.
- **A separate daemon beside `ft serve`.** Rejected: two processes to start, two views of one
  store, and a race over the capture path. `ft up` embeds serve.
- **Reusing the event log as the pipeline state.** Rejected: events are append-only summaries;
  the pipeline needs mutable state (phase, gate, attempts) and an atomic claim for the single
  worker, exactly why messages got their own entity.
- **Encoding pipeline state as a doc/artifact.** Rejected for the same reason: no atomic claim, no
  compare-and-swap, no cheap restart recovery.
- **Building the pipeline from the `chief` skill only (no daemon).** Rejected: the skill needs an
  agent context and a human to start it; the capture→pipeline trigger must be a process.
- **`ft run --goal <milestone>` as the build stage.** Considered and kept as a simpler option, but
  the chief skill's builder/reviewer pair, follow-up filing, and merge gate are the factory we want
  to supervise; the runner can start with `--goal` and grow into the chief dispatch.
- **Implementing rollout here.** Rejected: rollout is t-3p53uzgcve; this design only provides the
  stage, gate, and hook.

## 15. Task sequencing

This design informs, in build order:

1. **Pipeline entity + `PipelineRepo` + conformance** across the four backends (new task).
2. **Capture hook + reconciliation** — the trigger and opt-out (t-ywh4btshch).
3. **Groom stage + controller + `ft up` command** — the first end-to-end pipeline.
4. **Review/build/QA stages** — architecture-reviewer, chief dispatch, auto-QA (t-zdohwjheha).
5. **Gates + dashboard `/factory` + `ft up status`** — surfacing and clearing.
6. **Rollout stage** (t-3p53uzgcve), gated by the release milestone.

Milestone: t-dvluzscb5k (OSS single-user factory). Origin idea: t-mcz23lijcs.
