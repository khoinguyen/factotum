# ft

`ft` manages projects and the human/agent task graph: the tasks, their
dependencies, and the specs, docs, memory, and events around them. This skill is
the shortest path to using it well as an agent.

## First-run setup

```sh
ft init            # create ~/.factotum/config.toml; offer to register this repo as a project
ft init -u         # only the machine-scoped config, never project scope
ft init -p         # register this directory as a project (id from the remote/dir)
ft init -p <name>  # ...using an explicit project name
```

`ft init` writes the machine-scoped config (`~/.factotum/config.toml`) when it is
missing and never clobbers an existing file. On a terminal, inside a git
repository, it offers to register the current directory as a project; with
`-p/--project` it registers directly, deriving the project id and name from the
git remote (else the directory name) unless a name is given. Registration writes
a `[projects.<id>]` entry with a `db_path` (default `~/.factotum/<id>.db`, sqlite) to the machine
config, pins `project = "<id>"` in the current directory's `.factotum/config.toml`, and creates the
project in its store. Without a terminal there are no prompts: `ft init` only creates the machine
config (and hints at `ft init -p`), while `ft init -p` registers using the derived defaults. `-u`
and `-p` are mutually exclusive.

## Start from the graph, not from guesswork

```sh
ft task next                    # the highest-ranked ready task (uses the default project)
ft task next --for <actor>      # what a specific human or agent should pick up
ft task next -n 5               # a shortlist
ft task next --explain          # why tasks are excluded; honors --for/--label/--repo/--groomed/-n
ft task next --for <actor> --groomed  # only groomed work, buildable autonomously
ft task get <task>              # full detail; its Next: block names the follow-up command
ft graph render --project <p>   # the whole DAG as text
```

`ft task next` ranks ready tasks by what unblocks the most work, proximity to a
milestone, the `--toward` target, and priority. Use it instead of scanning the
list by hand. When a task is missing from the ranking, `ft task next --explain`
lists each excluded task with its reason, applying the same
`--for`/`--label`/`--repo`/`--groomed`/`-n` filters as ranking, and `ft task get
<task>` prints a `not ready because:` line for the same reason.

## Watch the factory

```sh
ft serve                        # live dashboard + token-gated capture; also -p <project>, --all, --bind <host:port>
```
`ft serve` is an idea-centric live dashboard: ideas roll up their promoted tasks as
finished/active/blocked/captured, grouped board, drill-down; reads open, SSE live. Its write
side: `/capture` stores a sentence as an idea, gated by `serve.token` / `FACTOTUM_SERVE_TOKEN` (no token or `--all` disables); grooming enriches it later.

## Move work through its lifecycle

```sh
ft task start <task>    # todo -> in_progress
ft task review <task>   # in_progress -> ready_for_review (unblocks dependents)
ft task done <task>     # accepted and finished
ft task reopen <task>   # back to todo, e.g. after a failed review
```

The status verbs also work at the top level: `ft done <task>` == `ft task done
<task>` == `ft task set <task> status=done`.

Each command prints a `Next:` hint with the natural follow-up, so you rarely
need to remember the exact verb. Pass `--no-hints` (or set
`FACTOTUM_NO_HINTS=1`) to silence them.

Park work you cannot start yet without cancelling it:

```sh
ft task snooze <task> --until +7d        # also --until-task <task>, or --indefinite
ft task unsnooze <task>
```

A snoozed task leaves ranking until its condition passes (a date or task
condition clears itself; indefinite waits for `unsnooze`).

## Capture and manage ideas

```sh
ft idea create -p <project> -t "A half-formed thought" -b "context"  # capture, not execution
ft idea list -p <project>               # review; `ft idea search <query>` searches them
ft idea get <idea>                      # full detail; its Next: block points at promote
ft idea promote <idea>                  # create an executable task linked to the idea
```

`ft idea` is a distinct surface over the same storage as `ft task list -k idea`. An
`idea` is a non-executable capture: it never appears in `ft task next` or the
ready set, is not assignable or groomable, and only has `todo`, `done`, and
`cancelled`. Promotion creates a linked `task` carrying the idea's content and
keeps the idea as history; `ft task promote` remains as an alias.

## Gauge whether a task is ready

```sh
ft task check <task>                           # run every advisory check, cache the result
ft task check <task> --check grooming --force  # one check, recompute
ft task get <task>                             # shows the cached checks: block (no network)
ft task get <task> --fields checks             # just the checks block
ft task get <task> --fields not_ready          # just the readiness diagnostic
ft task decide <task> --ready --reason "why"   # record a human decision
ft task decide <task> --clear                  # restore the judge verdict
```

Checks are advisory, never a gate, and `ft task check` is a read verb whose only
write is its derived cache. The first check, `grooming`, judges the task's
**body** (title, kind, and description) and reports `ready`, `needs_grooming`
(only agent-owned findings, each a concrete body edit), or `needs_human` (a
decision only the author can make - escalate, do not edit the body).

It also scores two task-shaping signals - **decomposable** (one cohesive unit, or
a bundle that should split) and **right-sized** (small enough to verify as one
unit) - plus, for a promoted task, **entailment vs its origin**: the origin is
immutable, so the refinement must contradict nothing and invent nothing it does
not support. A named gap is a concrete finding (`decomposable`,
`right_sized`, `entailment_origin`); the result also carries an auditable
**delta** - the title change and the lines added or dropped - so a reviewer sees
what changed. The delta never rewrites the task; editing the origin makes it stale.

The body is the spec; notes are history. A note never changes a verdict, so fold
a decision into the body to make it count. Recording a human override requires a
human actor; it stops agents re-grooming, and editing the body makes it stale.

## Definition of ready: groomed work an agent can start

`groomed` is a dedicated task **field**, set explicitly - never inferred from a
label or from the presence of acceptance criteria. A groomed task must carry at
least one acceptance criterion: an observable condition that defines done. Only
groomed work counts as agent-ready; an ungroomed task still needs a human to
decide its scope and acceptance, so it stays human work even when assigned to an
agent.

```sh
ft task create -p <project> -t "..." --groomed --acceptance "observable result"
ft task update <task> --groomed --acceptance "observable result"  # or --ungroomed
ft task set <task> groomed=true
ft task list -p <project> --groomed     # only buildable work
ft task list -p <project> --ungroomed   # still needs grooming
ft task next -p <project> --groomed
```

`ft task apply` and `ft task edit` carry `groomed` and `acceptance_criteria` in
the task document, so criteria can be set as a list. A `task get` document also
carries a read-only `base` that `apply` three-way merges.

## Record what you learn and file new work

```sh
ft task note create <task> -b "Decision: ..." --link issue=https://...
ft task note create <task> -b "Triaged by agent: ..." --system  # generated note; hidden from search
ft task create -p <project> -t "Short imperative title" -r <repo> \
  --body "Context and acceptance criteria." --dep <blocking-task>
```

A bug, TODO, missing test, or follow-up spotted while working is a task: create
it and link it (a note, or `--dep`). Update the graph in the same session as the
code.

## Report bugs and friction back to Factotum

```sh
ft feedback create -b "next lists blocked tasks" --flow "ft task next"
ft feedback create -b "..." -p <project> -r <repo>   # with originating context
```

`ft feedback create` stores the report as a task labeled `external-feedback` in
the Factotum database, not the caller's project store, so a bug found while
working on any project reaches the maintainers. It prints the stored task id. The
sink is the project named by `--feedback-store` (default `factotum`), resolved
from the machine-scoped `[projects.<id>]` entry; `--transport` selects the sink
transport (only `db` ships today). Reporting does not touch the caller's store, so
it works even when that store is misconfigured or absent. The report collects the
message, the command/flow involved, the `ft` version, and the originating project
and repo; paths under `$HOME` and token-shaped strings are redacted before storing.

## Break a prompt into tasks

```sh
ft prompt -p <project> break this feature into tasks   # preview the plan
ft prompt -p <project> -y break this feature into tasks # create the proposed tasks
```

`ft prompt` hands the words to the configured agent CLI and prints the tasks it
proposes, in the `ft task apply` document shape. Nothing is created until `-y`.
The agent CLI comes from the machine-scoped `[agent]` table,
`FACTOTUM_AGENT_COMMAND`, or the `--agent-command` flag (which wins for one
invocation):

```toml
[agent]
command = "my-agent-cli"   # reads the instruction on stdin, prints JSON on stdout
```

Without a configured agent, `ft prompt` reports a clear error.

## Find things

```sh
ft task list -p <project> --status todo
ft task search "<query>"        # titles, descriptions, and notes (terms ANDed, prefix match)
ft task get <task>              # description, deps, dependents, notes, memory
ft task context <task>          # task + deps + notes + memory + recent events
ft doc search "<query>"         # specs and docs (terms ANDed, prefix match)
ft doc get <artifact>           # read one spec/doc back
ft memory search "<query>"      # agent memory (same lexical search)
ft memory get <memory>          # read one memory back
```

`ft task search` ranks a title match above a description match above a note match,
with deterministic ties. Notes created with `--system` (generated triage or
reassignment messages) are kept on the task but excluded from search indexing.
When a judge is configured it then reranks the shortlist by meaning;
`--no-rerank` keeps the lexical order, and a search never fails because the
judge is absent or slow.

## Memory

Memory is a first-class, searchable artifact for durable agent knowledge:

```sh
ft memory create -p <project> -t "Title" --brief "when to load me" -b "What to remember"
ft memory list -p <project>
ft memory context -p <project>       # briefs of all memory: when to load which
ft memory search "<query>" -p <project>
ft memory get <memory>
ft memory update <memory> [-t "Title"] [--brief "..."] [-b "Content" | -f file] [--task <task>]
ft memory delete <memory>
ft memory reindex -p <project>       # re-embed all memory (requires [embed])
```

A memory carries three things, all authored by the agent: a `title`, a one-line
`brief` (what it is and when to load it), and the full `body`. `ft memory list`
and `ft task context` show the brief; `ft memory get` returns the body. `ft`
stores them verbatim and never generates them. When a judge is configured,
`ft memory create`/`update` advise (on stderr) when the entry supersedes or is
strongly related to existing memory - reconcile it in the same session.

`ft memory update` patches in place; `--task <task>` attaches the memory to a
task and `--task ""` detaches it. Only memory artifacts are accepted: the verbs
reject specs and docs.

Memory search is lexical by default. Configuring an embedding provider (the
machine-scoped `[embed]` table, or `FACTOTUM_EMBED_PROVIDER`) adds vector recall:
`ft memory search` then also finds paraphrases that share no tokens. Writes stay
best-effort - a memory is created even when the embedder is down, and the skipped
vector is backfilled by the next edit or `ft memory reindex`. A change of embedding
model is detected and reported; run `ft memory reindex` after changing it.

## Diagnose the optional subsystems

```sh
ft doctor                    # is the embedder/judge configured, reachable, usable?
ft doctor -o json            # the same report for an agent to parse
ft doctor -p <project>       # include the project's memory vector index check
ft doctor --fix              # apply the recommended fixes (asks on a terminal)
ft doctor --strict           # exit non-zero on warnings too
```

`ft doctor` reports each check as `ok`/`warn`/`fail` with the observed state and a
concrete recommendation: a provider that is configured but unknown, an endpoint that
does not answer, a model the endpoint does not serve (`ollama pull <model>`), a
stdio command not on `PATH`, a vector index left on an old model (`ft memory
reindex`), or a judge without a key. It is read-only unless `--fix` is given; on a
terminal without `--fix` it asks before applying anything, and it never starts a
server or downloads a model on its own. The exit code is non-zero when any check
fails, so a script can gate on it.

## Conventions that matter

- `-p/--project` falls back to the configured default (`project` in
  `.factotum/config.toml`, `default_project` in `~/.factotum/config.toml`).
  Mutating commands error without a default; list/filter commands fall back to
  all.
- `-o json|yaml` is the machine-readable interface. Text output is yaml-like
  `key: value` lines for single results and tables for lists.
- Large output is bounded so it cannot flood an agent's context: when stdout is
  not a terminal and output exceeds 32 KiB or 400 lines, `ft` spills the full text
  to a temp file and prints a head/tail window plus its path (interactive output is
  never bounded); pass `--full` to print everything, `FACTOTUM_MAX_OUTPUT=<bytes>`
  to set a byte budget, or `=unlimited` to disable the bound. `-o json|yaml` becomes
  a truncation envelope.
- Never edit a database by hand: go through `ft`.
- A branch build can forward-migrate the shared database. If `ft` reports a schema version newer
  than it supports, update the installed binary (`mise run install`) or use the newer branch binary;
  never point a non-installed branch binary at the real project DB — exercise CLI changes against a
  throwaway store (`--store jsonfile --store-opt path=$(mktemp -d)/db.json`) or a temp config.
- `ft task set <id> field=value ...` updates fields, including
  `not_before=YYYY-MM-DD` (or `+7d`) to defer a task and `not_before=` to clear
  it.

## Agent runs and isolation

Agent runs execute a harness inside a pluggable isolation backend. The `local`
backend runs the harness directly on the host with no isolation at all: it is
dev-only, must be explicitly opted in, is never the default, and prints an
unsandboxed warning when it starts. It can read host credentials, reach the
network, and mutate files, so never use it for untrusted work. Isolating backends
(for example OpenShell and docker) are separate registrations of the same port
and are the only ones fit for untrusted input.

A harness describes one agent CLI end to end: its image, entrypoint, invocation,
model flag, prompt delivery, completion detection, and output parsing. The first
harness is OpenCode (image `ghcr.io/anomalyco/opencode`, binary `opencode`). A
headless run is `opencode run --model <provider/model> <prompt>`: the prompt is
the final positional argument, the agent's answer is read from stdout, and
progress and the banner go to stderr. The model and its credentials come from the
configured provider, never hardcoded: the model is passed through as the model
flag, and a configured credential is declared as a provider reference that the
isolation backend resolves. A local run uses the host `opencode` binary
(dev-only); an isolating backend runs the shipped image as a non-root user.

An isolating backend imposes a policy on the run. The OpenShell policy
(`pkg/isolation/openshell`, checked-in template `policy.yaml`) is deny-by-default:
the sandbox reaches no host until the project opts in, so OpenCode's phone-home
to `models.opencode.ai` and `registry.npmjs.org` stays denied. The workload runs
as a non-root identity, filesystem access is limited to read-only system paths
plus the workdir and `/tmp`, and no credential value is placed in the sandbox: a
configured credential is attached through a provider that injects a placeholder.
The policy advisor stays `manual`; agent-authored rules are never auto-approved.

A project opts in to the hosts it needs by committing
`.factotum/openshell-policy.yaml`:

```yaml
allow_hosts:
  - models.opencode.ai
  - registry.npmjs.org
```

Only `allow_hosts` is honored. Any other key is rejected, so a project can widen
egress but never weaken the identity, filesystem, or credential defaults. `ft run`
loads the policy next to the active project config (a custom `-c` uses its sibling
`openshell-policy.yaml`); with no policy file present it warns that egress is deny-all.

`ft run <task>` drives one task end-to-end: it resolves the task's repositories into a
workspace, runs the harness with the task as its prompt, and reflects progress into the store.

```sh
ft run t-abc123 --sandbox local --harness opencode --allow-host   # dev-only opt-in
ft run --prompt-file grooming.md --sandbox local --harness opencode --allow-host  # task-less
```

- `--prompt-file <path>` or `--prompt-artifact <id>` supplies a stored prompt: with a task it
  replaces the task prompt, and with no task id it runs the prompt once over every repository
  of the configured project (a task-less session such as grooming), writing nothing to the task
  graph. They are mutually exclusive; a missing or empty `--prompt-file` is an input error (exit 1).
- `--sandbox`/`--harness` are optional: they resolve flag > `FACTOTUM_RUN_*` > committed
  project `[run]` > machine `[run]`. On a terminal an unset one prompts once and saves to
  the chosen config; without a terminal it is an error. A committed project `[run]` may set
  only `sandbox`/`harness`; the machine table also holds workspace/model/args/allow_host/credential_env. `--refresh` hard-resets a reused checkout; a changed origin URL fails.
- `provider` and `credential_env` name the credential a harness may use and the
  variable it arrives under, e.g. `provider = "openrouter"` with
  `credential_env = "OPENROUTER_API_KEY"`; the value is read from the host
  environment and handed to the isolating backend, never placed in the sandbox.
- The `local` backend is unsandboxed and refuses to run until explicitly opted in
  with `--allow-host` or `run.allow_host`; the opt-in is machine-scoped and is
  ignored from the committed project file.
- The `openshell` backend runs the harness in a non-root sandbox under the
  deny-by-default policy above; it needs the `openshell` CLI and a gateway, and
  attaches a configured credential as a provider placeholder, never a value.
- The `docker` backend runs the harness in a per-task container from the
  harness's image, mounting only the resolved workspace at its same absolute
  path. It needs the `docker` CLI and a daemon, refuses a policy it cannot
  enforce, and injects credentials per-exec so no secret enters argv or metadata.
- A successful run moves the task to `ready_for_review` and records a note with the
  agent's output plus `task.run_started`/`task.run_finished` events. A failed run
  leaves the task's status untouched and records only a failure note and event, so
  a broken run never corrupts the graph.

`ft run --goal <task|milestone>` drives the graph toward a goal: it repeatedly runs the highest-ranked agent-ready on-path task until the goal is reached, work stalls, or the budget is exhausted.

```sh
ft run --goal t-abc123 --sandbox local --harness opencode --allow-host --max-tasks 5
```

- The goal is a task or a milestone: a task resolves, a milestone is a human gate, so the
  loop runs its prerequisites and stops `no_ready_work` until a human closes it.
- Only agent-ready work is run: a task must be assigned to an agent and `groomed`, the
  bucket `ft task next --for <agent>` offers. Startable but non-agent-ready on-path work
  (unassigned, human-owned, or ungroomed) is left for a human and named in `not_run:`.
- Only tasks on a path to the goal are run, ordered by the composite ranker with the
  goal as the toward preference, so readiness and priority always decide the order.
- `--max-tasks N` bounds the number of task runs (0 means no budget). The loop stops for
  a human when a run fails; a failed run leaves the task's status untouched.
- The stop reason is `stop:` (`goal_reached`, `no_ready_work`, `blocked`,
  `budget_exhausted`, or `failed`), and each iteration prints a compact `step:` block.

## Getting help

```sh
ft <command> --help
ft skill list
ft skill get <name>
ft skill lint             # check the embedded skills against the live CLI (deterministic)
ft skill lint --semantic  # also judge prose references (needs a TypeSafe key)
```
