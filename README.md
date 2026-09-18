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
  with `project repo add|list|update|rm`.
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
ft actor add --kind agent claude
ft actor add --kind human Khoi

PID=$(ft project create "Acme" \
  --repo "name=backend,url=git@example.com:acme/backend.git,path=repos/backend,brief=Go API service" \
  --repo "name=web,path=repos/web,brief=Next.js frontend" | cut -f1)
T1=$(ft task add --project "$PID" --title "Write the spec" | cut -f1)
T2=$(ft task add --project "$PID" --title "Build the API" --dep "$T1" | cut -f1)
M=$(ft milestone create --project "$PID" --title "v0.1 release" | cut -f1)
ft task add --project "$PID" --title "Deploy v0.1" --dep "$M"

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
| Project | `project create`, `project list`, `project show`, `project rm` |
| Repositories | `project repo add`, `repo list`, `repo update`, `repo rm` |
| Actor | `actor add --kind human\|agent`, `actor list` |
| Task | `task add --repo <name>`, `task list --repo <name>`, `task show`, `task update`, `task rm` |
| Dependencies | `task dep add`, `task dep rm` (cycles are rejected) |
| Assignment | `task assign --actor <ref>` / `--unassign` |
| Status | `task start`, `review`, `done`, `reopen`, `block`, `cancel` |
| Notes | `task note add --body ... [--link kind=url]` |
| Ranking | `task next --project ... [--for <actor>] [--repo <name>] [--toward <task>] [--rank unblock\|milestone\|toward\|composite]` |
| Milestone | `milestone create`, `milestone list`, `milestone done` |
| Artifacts | `doc add --kind spec\|doc\|memory`, `doc list`, `doc search` |
| Rendering | `graph render --format agent\|json\|tree\|html\|dot\|mermaid [--layout tree\|waves]` |
| Audit | `event list` |

Global flags: `--config`, `--store`, `--store-opt key=value`, `--actor`, `-o/--output text|json`,
`--no-hints`.

## Next-step suggestions

After text output, `ft` prints a short `Next:` block of natural follow-up commands to **stderr**, so
pipes and redirections stay clean:

```sh
$ ft task show APS-10803
(todo) APS-10803: Executor loop + reaper (multi-replica, SKIP LOCKED claims)
repo: backend
assignee: (agent) agent
...

Next:
  ft task start APS-10803                   begin work
  ft task assign APS-10803 --actor <actor>  claim it (see ft actor list)
```

Suggestions are context-aware: `task next` points at `task show` for the top task, `task show`
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
