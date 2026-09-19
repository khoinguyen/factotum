# Factotum

A toolset for managing agentic coding work: projects (which may span many repositories), the
task graph of mixed human and agent work, milestones, and the specs, docs, and memory around them.

> Status: early, but working end to end. The `ft` CLI manages projects, actors, tasks,
> milestones, artifacts, and an append-only event log, with pluggable storage backends and
> pluggable graph renderers.

## Why

A coding project rarely maps to one repository. It may span a backend, a frontend, a data repo,
and a devops repo — plus the humans and agents working across all of them. Factotum is the
generalist that keeps that work ordered: what exists, what depends on what, what is ready next,
and who is waiting on whom.

## Quickstart

```sh
mise install
mise run ci

# Build the CLI (binary: ft)
go build -o ft ./cmd/factotum

# Use the in-memory backend, or pick jsonfile / sqlite
./ft --store sqlite --store-opt path=.factotum/factotum.db project create "Acme"
```

## Concepts

- **Project** — spans many repositories. Each repository carries a name, optional URL, local
  path, and a **brief** describing its purpose (`--repo name=<k>,url=,path=,brief=`), managed
  with `project repo create|list|update|delete`.
- **Actor** — a human or an agent, registered once and referenced by ID or name. The ID is the
  slug of the name (`Khoi` → `khoi`, `Claude Code` → `claude-code`).
- **Task** — a unit of work with a body (`--body` or `--body-file`), dependencies, status, an
  optional **repository** (`--repo <name>`, one of the project's repos), assignee, waiting-on,
  notes, and labels.
- **Milestone** — a task of kind `milestone` that gates a release.
- **Resolution** — a regular task unblocks its dependents when it is *resolved*
  (`ready_for_review`, `done`, or `cancelled`); a milestone unblocks only when explicitly `done`.
- **Artifact** — a spec, doc, or memory note, searchable across a project.
- **Event** — every mutation is recorded in an append-only audit log.

## Example session

```sh
ft actor create --kind agent claude
ft actor create --kind human Khoi

PID=$(ft project create "Acme" \
  --repo "name=backend,url=git@example.com:acme/backend.git,path=repos/backend,brief=Go API service" \
  --repo "name=web,path=repos/web,brief=Next.js frontend" | sed -n 's/^project: //p')
T1=$(ft task create --project "$PID" --title "Write the spec" | sed -n 's/^task_id: //p')
T2=$(ft task create --project "$PID" --title "Build the API" --dep "$T1" | sed -n 's/^task_id: //p')
M=$(ft milestone create --project "$PID" --title "v0.1 release" | sed -n 's/^task_id: //p')
ft task create --project "$PID" --title "Deploy v0.1" --dep "$M"

# What can the agent or a human pick up next?
ft task next --project "$PID" --for claude
ft graph render --project "$PID" --format agent

# Agent finishes an agent task and submits it for review; dependents unblock.
ft task review "$T1"

# Human-readable report for review
ft graph render --project "$PID" --format html --layout tree --out dag.html

# Audit trail
ft event list --project "$PID"
```

## Commands

| Area | Commands |
| --- | --- |
| Project | `project create`, `project list`, `project get`, `project delete` |
| Repositories | `project repo create`, `repo list`, `repo update`, `repo delete` |
| Actor | `actor create --kind human\|agent`, `actor list` |
| Task | `task create --repo <name>`, `task list --repo <name>`, `task get`, `task update`, `task set field=value...`, `task apply -f`, `task edit`, `task delete` |
| Dependencies | `task dep create`, `task dep delete` (cycles are rejected) |
| Assignment | `task assign --actor <ref>` / `--unassign` |
| Status | `task start\|review\|done\|reopen\|block\|cancel`, or the top-level shortcuts `ft start\|review\|done\|reopen\|block\|cancel` |
| Notes | `task note create --body ... [--link kind=url]` |
| Ranking | `task next -p <project> \| -a/--all [--for <actor>] [--repo <name>] [--toward <task>] [--rank unblock\|milestone\|toward\|composite]` |
| Milestone | `milestone create`, `milestone list`, `milestone done` |
| Artifacts | `doc create --kind spec\|doc\|memory`, `doc list`, `doc search` |
| Rendering | `graph render --format agent\|json\|tree\|html\|dot\|mermaid [--layout tree\|waves]` |
| Audit | `event list` |

Global flags: `-c/--config`, `--store`, `--store-opt key=value`, `--actor`, `-o/--output text|json|yaml`,
`--no-hints`.

Common flags carry shorthands: `-p/--project`, `-t/--title`, `-b/--body`, `-r/--repo`, `-k/--kind`,
`-s/--status`, `-d/--dep`, `-n/--limit`, and `-a` (`--all` on `task next`, `--actor` on `task
assign`). `task next` takes either `-p/--project` or `-a/--all` (ready tasks across every project),
never both.

Every command that takes `-p/--project` falls back to the configured project (`project` in the
project file, or `default_project` in the machine file) when the flag is omitted; mutating commands
error if no default is configured.

Text output follows one convention:

- commands that report a single result print yaml-like `key: value` lines, ending with `project`
  (e.g. `task_id: t-xxx`, `created: true`, `title: ...`, `project: factotum`);
- list commands print a table with a `PROJECT` column;
- `get` prints a human-readable block, including the `project`.

`-o json` / `-o yaml` remain the machine-readable interfaces and are unaffected.

Invalid invocations (wrong argument count, or a missing required flag) print the command's help to
stderr and exit with status `2`, instead of a terse one-line error.

`task set <task> field=value...` assigns several fields at once, validating each type: `status`,
`kind`, `priority` (integer), `repo`, `title`, `labels` (comma-separated), and `body`. Long text can
come from a file (`body=@notes.md`) or stdin (`body=-`).

`task get -o json|yaml` prints a round-trippable task document. Edit it and feed it back with
`task apply -f <file>` (format inferred from the extension; override with `--format`), or open it
in `$EDITOR` with `task edit <task>`. `id` and `project_id` are immutable, and command-managed
relations (`assignee`, `deps`, `waiting_on`) may be echoed back unchanged but any modification is
rejected. `updated_at` is a compare-and-swap token: applying a document produced before a concurrent
change fails with a conflict instead of overwriting it (`created_at` is carried for information
only).

## Next-step suggestions

After text output, `ft` prints a short `Next:` block of natural follow-up commands to **stderr**, so
pipes and redirections stay clean:

```sh
$ ft task get APS-10803
(todo) APS-10803: Executor loop + reaper (multi-replica, SKIP LOCKED claims)
repo: backend
assignee: (agent) agent
...

Next:
  ft task start APS-10803                   begin work
  ft task assign APS-10803 --actor <actor>  claim it (see ft actor list)
```

Suggestions are context-aware: `task next` points at `task get` for the top task, `task get`
points at the next status transition (or at a blocking dependency when one is unmet), and mutations
point at the relevant inspection command. They are suppressed for `-o json`, by `--no-hints`, by
`FACTOTUM_NO_HINTS=1`, or by `no_hints = true` in the config file.

## Storage backends

Every backend passes the shared contract suite in `pkg/store/conformance`.

| Backend | Select with | Notes |
| --- | --- | --- |
| Memory | `--store memory` | Ephemeral; the default. |
| JSON file | `--store jsonfile --store-opt path=...` | Single document, atomic writes. |
| SQLite | `--store sqlite --store-opt path=...` | Pure-Go driver (`modernc.org/sqlite`). |

## Design

- **Hexagonal architecture.** A pure domain core (`pkg/core`) surrounded by ports and adapters.
- **Pluggable from day one.** Storage backends, next-task rankers, output renderers, and CLI
  commands are registered plugins (`pkg/registry`).
- **Agent-first, human-readable.** `graph render --format agent` emits compact text for agents;
  `--format html` emits a self-contained report for humans.
- **Append-only event log.** Every mutation records an event, which powers the audit trail and the
  report's activity history.

## Repository layout

```
cmd/factotum        CLI entrypoint
internal/cli        Cobra command tree
internal/config     TOML + env + flag configuration
pkg/registry        generic plugin registry
pkg/core            pure domain model and resolution policy
pkg/graph           DAG engine: readiness, waves, cycles, canonical parents
pkg/store           storage ports, adapters, and the conformance suite
pkg/rank            next-task rankers
pkg/render          agent/json/tree/html/dot/mermaid renderers
pkg/app             use-case services
```

## Development

This project is developed test-first. See [AGENTS.md](AGENTS.md) for the workflow and the
definition of done.
