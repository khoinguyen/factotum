# Factotum

`ft` manages agentic coding work: **projects** (which may span many repositories), the **task graph**
of mixed human and agent work, and the specs, docs, and memory around them. It is agent-first — it
ranks the next tasks to do, marks which ones an agent may start, and records every mutation.

> Status: early, but working end to end. Storage backends, next-task rankers, output renderers,
> agent harnesses, and isolation backends are all pluggable.

## Why

A coding project rarely maps to one repository. It may span a backend, a frontend, a data repo, and
a devops repo — plus the humans and agents working across all of them. Factotum is the generalist
that keeps that work ordered: what exists, what depends on what, what is ready next, and who is
waiting on whom.

## Install

Prerequisites: [mise](https://mise.jdx.dev) (it installs the pinned Go toolchain, linter, Node, and
pnpm). From a clone:

```sh
git clone <this repo> factotum && cd factotum
mise install       # pinned Go, golangci-lint, Node, and pnpm
mise run build     # -> ./bin/ft (also builds the embedded web UI)
mise run install   # -> ~/.local/bin/ft
```

`mise run build` writes `./bin/ft` only; `mise run install` is the separate step that updates the
installed CLI. Both stamp the git SHA into `ft version`, so a binary is identifiable. Use `./bin/ft`
(or put `./bin` on your `PATH`) if you did not install.

## Quickstart

This registers a project in a per-project store and creates your first task. It uses the installed
`ft`; the same commands work through `./bin/ft`.

<!-- quickstart:begin -->
```sh
# Register this directory as a project: writes ~/.factotum/config.toml and
# pins the project id in ./.factotum/config.toml.
ft init -p acme

# Create your first task and capture its id.
task=$(ft task create -t "Try Factotum" -b "My first task." | sed -n 's/^task_id: //p')

# See it, read it back, and rank what to start next.
ft task list
ft task get "$task"
ft task next

# ft also ships the agent skill that teaches a harness the ft loop.
ft skill list
```
<!-- quickstart:end -->

`ft init -p <name>` slugs the name into a project id, or derives one from the git remote (else the
directory name) when no name is given. Registration makes the project the default, so later commands
need no `-p`. See [Projects and repositories](#projects-and-repositories) for what it writes.

## The core loop

A task moves `todo → in_progress → ready_for_review → done`. The status verbs are the whole
interface; run `--help` on any of them, or follow the `Next:` block each command prints.

```sh
ft task start <task>    # todo -> in_progress
ft task review <task>   # in_progress -> ready_for_review (unblocks dependents)
ft task done <task>     # accepted and finished
ft task reopen <task>   # back to todo, e.g. after a failed review
```

The verbs also work at the top level: `ft done <task>` == `ft task done <task>` ==
`ft task set <task> status=done`. Other statuses: `ft task block` / `cancel`, and `snooze` to park
work out of ranking (`--until +7d`, `--until-task <task>`, or `--indefinite`).

Everything else you need while working:

- **Dependencies** gate readiness: `--dep <blocking-task>` on create, or `ft task dep create`. A
  task is ready only once its dependencies are *resolved* (`ready_for_review`, `done`, or
  `cancelled`); a milestone unblocks only when explicitly `done`. Cycles are rejected.
- **Claim** the highest-ranked ready task instead of copying an id: `ft task claim --for <actor>
  [--start]`.
- **Notes** are history attached to a task: `ft task note create <task> -b "..." [--link pr=<url>]`.
  A note never changes a grooming verdict — fold a decision into the task **body** to make it count.
- **Groomed** is what an agent may start: a task with at least one acceptance criterion, marked
  explicitly. Only groomed work appears in `ft task next --for <agent> --groomed`.

```sh
ft task create -p acme -t "Add retry to the uploader" \
  --groomed --acceptance "a flaky upload retries then succeeds (test)" \
  --body "Context and acceptance criteria." --dep <blocking-task>
ft task next --for claude --groomed   # the buildable shortlist for an agent
```

Suggestions are context-aware and go to **stderr**, so pipes stay clean. Suppress them with
`--no-hints`, `FACTOTUM_NO_HINTS=1`, or `no_hints = true` in config.

## Projects and repositories

Configuration has two scopes, merged `env > project file > user file > defaults`:

- `~/.factotum/config.toml` — machine-scoped, not committed: `default_project`, a
  `[projects.<id>]` registry mapping each project to its `db_path`, and machine-only settings.
- `./.factotum/config.toml` — project-scoped, committed: `project = "<id>"` plus optional overrides.
  Machine-local paths must never appear here.

`ft init` wires both up and registers the project. Add the repositories it spans with
`ft project repo` (a project created outside a git checkout uses `ft project create <name> --repo
"name=<repo>,url=<url>,brief=<brief>"` instead):

```sh
ft project repo create acme backend --url git@github.com:acme/backend.git --brief "Go API service"
ft project repo create acme web --path repos/web --brief "Next.js frontend"
ft project repo list acme
ft project list
```

A **project** spans repositories; each **repository** carries a name, optional URL and local path,
and a **brief**. Tasks may target one repo with `-r/--repo <name>`. Any command that takes
`-p/--project` falls back to the configured default; mutating commands error when none is set, while
list/filter commands fall back to all.

## Capture ideas and bugs

Ideas and bugs are non-executable captures over the same storage as tasks. They are never in
`task next` or the ready set, and are not assignable. Refining one creates a linked executable task
and keeps the capture as history.

```sh
ft idea create -p acme -t "A half-formed thought" -b "what if?"
ft idea promote <idea>          # groom into an executable task linked to the idea
ft bug create -p acme -t "It crashes on save" -b "steps to reproduce"
ft bug triage <bug>             # triage into an executable task linked to the bug
ft idea list && ft bug list
```

`ft task promote` is the peer of `ft idea promote` / `ft bug triage` for a task of kind `idea` or
`bug`. Both surfaces also have `list`, `search`, and `get`.

## Run agents

`ft run <task>` resolves the task's repositories into a workspace, prepares the selected **isolation
backend**, runs the selected **harness**, captures the output, and reflects progress into the store.
A successful run moves the task to `ready_for_review`.

```sh
ft run <task> --sandbox local --harness opencode --allow-host   # dev-only, unsandboxed
ft run --goal <task|milestone> --sandbox openshell --harness opencode --max-tasks 5
```

- `--sandbox` and `--harness` resolve flag > `FACTOTUM_RUN_*` > committed project `[run]` > machine
  `[run]`. On a terminal an unset one prompts once and saves the answer; non-interactively, an
  unset one is an error. The `local` backend is unsandboxed and must be opted in explicitly
  (`--allow-host` or `run.allow_host`).
- Sandboxes: `local` (dev only), `openshell`, and `docker`. Harness: `opencode`. An isolating
  backend runs the harness's image as a non-root user under its policy; a configured credential is
  attached as a provider placeholder, never placed in the sandbox.
- `--goal` repeatedly runs the highest-ranked **agent-ready, groomed, on-path** task until the goal
  is reached, work stalls, or `--max-tasks` is exhausted. A task goal resolves on its own; a
  milestone is a human gate, so the loop stops `no_ready_work` until a human closes it.
- With `--prompt-file`/`--prompt-artifact` and no task id, `ft run` runs the prompt once over every
  repository and writes nothing to the graph.

The machine-scoped `[run]` table holds the launcher settings (`sandbox`, `harness`, `workspace`,
`model`, `args`, `refresh`, `allow_host`, `provider`, `credential_env`):

```toml
# ~/.factotum/config.toml — machine-scoped.
[run]
sandbox = "openshell"       # or docker; local is unsandboxed and needs allow_host
harness = "opencode"
provider = "openrouter"
credential_env = "OPENROUTER_API_KEY"
```

Only `sandbox` and `harness` are read from a committed project `[run]` (project intent); host-scoped
settings (`workspace`, `allow_host`, credentials) are read from the machine file only, so a committed
file never carries a host path or opts into the unsandboxed backend.

## Teach your agent to use ft

`ft` ships the usage skill it expects an agent to follow, embedded in the binary (source:
[`internal/skills/content`](internal/skills/content)). Load it into a harness and the harness knows
the ft loop — rank, start, review, done — and the conventions around it.

```sh
ft skill list          # NAME/DESCRIPTION for the embedded skills
ft skill get           # ft: the core loop, commands, and conventions (the default skill)
ft skill get ft        # the same skill, named explicitly
ft skill get groom     # the grooming session protocol
```

The two skills are **ft** (how to use `ft`) and **groom** (how to run a grooming session).
`ft skill get` prints the skill's markdown to stdout; put it where your harness reads its
instructions. OpenCode reads an `AGENTS.md` in the project, so append the skill there, or write it
to a file and list that file under `instructions` in `opencode.json`:

```sh
ft skill get ft >> AGENTS.md          # straight into the agent's rules
ft skill get ft > docs/ft-skill.md    # or a separate file, referenced by the harness:
# opencode.json: { "instructions": ["docs/ft-skill.md"] }
```

Regenerate the copy after upgrading `ft`. `ft skill lint` checks the embedded skills against the
live CLI, so a skill never names a command or flag that no longer exists (`ft skill lint
--semantic` also judges the prose, and needs a TypeSafe key).

## Groom a project

`ft groom` runs a task-less session over the project's open ideas and ungroomed tasks, using the
durable prompt at `docs/grooming/prompt.md`, and records its report and deferred questions as doc
artifacts. It resolves sandbox and harness exactly like `ft run`.

```sh
ft groom                 # every open idea + ungroomed task
ft groom <idea|task>...  # a specific scope
ft groom --unattended    # no product owner: defer product questions, still finish agent-ready
ft groom list            # past sessions
ft groom show <session>  # a session's report, deferred questions, and produced tasks
```

## Serve the dashboard

`ft serve` is a live, idea-centric dashboard over the graph (reads are open and SSE-live; the read
pages have no mutating endpoints). Its `/capture` page turns a sentence into a stored idea or bug,
gated by a shared token.

```sh
ft serve                          # 127.0.0.1:8484, configured project
ft serve --all                    # every registered project
ft serve --bind 0.0.0.0:8484      # reachable from another device
```

Capture is enabled by the machine-scoped `serve.token` (or `FACTOTUM_SERVE_TOKEN`); with no token it
is disabled and the read side stays open. The app is embedded at `/` — `mise run build-web` rebuilds
it into `web/dist`.

## Commands (reference)

| Area | Commands |
| --- | --- |
| Setup | `init [-u\|-p [name]]`, `doctor`, `feedback create`, `version`, `skill list\|get\|lint` |
| Project | `project create\|list\|get\|delete`, `project repo create\|list\|update\|delete` |
| Actor | `actor create --kind human\|agent`, `actor list` |
| Task | `task create`, `task list`, `task get`, `task update`, `task set field=value...`, `task search`, `task context`, `task apply`, `task edit`, `task delete` |
| Lifecycle | `task start\|review\|done\|reopen\|block\|cancel`, `task claim`, `task assign`, `task snooze\|unsnooze`, `task wait` |
| Graph | `task dep create\|delete`, `task next`, `graph render --format summary\|agent\|json\|tree\|html\|dot\|mermaid` |
| Grooming | `task check`, `task decide`, `task promote`, `groom [item...]`, `groom list\|show` |
| Prompt | `prompt [-y] <words>` (break a free-form prompt into tasks via the configured agent CLI) |
| Notes | `task note create [--system]` |
| Capture | `idea create\|list\|get\|search\|promote`, `bug create\|list\|get\|search\|triage` |
| Artifacts | `doc create --kind spec\|doc\|memory`, `doc list\|get\|search`, `memory create\|list\|get\|update\|delete\|search\|context\|reindex` |
| Milestone | `milestone create\|list\|done` |
| Run & serve | `run`, `groom`, `serve` |
| Audit | `event list` |

Global flags: `-c/--config`, `--user-config`, `--store`, `--store-opt key=value`, `--actor`,
`-o/--output text|json|yaml`, `--full`, `--no-hints`. Common shorthands: `-p/--project`,
`-t/--title`, `-b/--body`, `-r/--repo`, `-k/--kind`, `-s/--status`, `-d/--dep`, `-n/--limit`.
`ft task next` takes either `-p/--project` or `-a/--all`, never both.

Text output has one shape: single results print yaml-like `key: value` lines
(`task_id`, `kind`, `title`, `status`, ending `project`, `repo`); lists print a table; `get` prints a
human block. `-o json|yaml` is the machine-readable interface.

## Storage backends

Every backend passes the shared contract suite in `pkg/store/conformance`.

| Backend | Select with | Notes |
| --- | --- | --- |
| Memory | `--store memory` | Ephemeral; the default. |
| JSON file | `--store jsonfile --store-opt path=...` | Single document, atomic writes. |
| JSON dir | `--store jsondir --store-opt path=...` | Directory of markdown + YAML frontmatter. |
| SQLite | `--store sqlite --store-opt path=...` | Pure-Go driver; the backend `ft init` registers. |

SQLite versions its schema with `PRAGMA user_version` and migrates on open, backing the file up
first. A database written by a newer binary is rejected rather than modified. A one-time
`migrate_from` option moves a legacy `jsonfile` store into a `jsondir`.

## Memory and search

Memory is a first-class, searchable artifact for durable agent knowledge. Each entry is a `title`, a
one-line `brief` (what it is and when to load it), and a full `body`; `ft` stores them verbatim.

```sh
ft memory create -p acme -t "Deploys" --brief "when shipping" -b "The deploy path is ..."
ft memory context -p acme      # briefs of all memory: when to load which
ft memory search "<query>" -p acme
```

`ft doc search`, `ft memory search`, and `ft task search` share one lexical, token-prefix,
deterministic search — no network. Configuring the optional machine-scoped `[embed]` table (for
example a local Ollama `nomic-embed-text`) adds vector recall on top, so paraphrase matches are
found too; writes stay best-effort and `ft memory reindex` backfills. `ft doctor` diagnoses a
configured-but-broken provider.

## Design

- **Hexagonal.** A pure domain core (`pkg/core`) surrounded by ports and adapters; every capability
  (storage, rankers, renderers, harnesses, isolation, commands) is a registered plugin.
- **Agent-first, human-readable.** `graph render --format agent` emits compact text for agents;
  `--format html` emits a self-contained report for humans.
- **Append-only event log.** Every mutation records an event, powering the audit trail.

```
cmd/factotum        CLI entrypoint
internal/cli        Cobra command tree
internal/config     TOML + env + flag configuration
pkg/core            pure domain model and resolution policy
pkg/graph           DAG engine: readiness, waves, cycles
pkg/store           storage ports, adapters, and the conformance suite
pkg/rank            next-task rankers
pkg/render          agent/json/tree/html/dot/mermaid renderers
pkg/app             use-case services
```

## Development

Developed test-first; see [AGENTS.md](AGENTS.md) for the workflow and definition of done. `ft` is
managed by `ft` — start from the graph:

```sh
mise run ci                 # fmt-check, lint, test, cover, web-test, build, smoke-web, smoke-quickstart
ft task next                # the highest-ranked ready task
ft graph render --project factotum --format agent
```

`ft skill get ft` prints the embedded agent skill (see
[Teach your agent to use ft](#teach-your-agent-to-use-ft)), and `ft skill lint` checks the embedded
skills against the live CLI. Vector recall has an opt-in real-embedding test: `mise run test-embed`.
