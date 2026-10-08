# ft

`ft` manages projects and the human/agent task graph: the tasks, their
dependencies, and the specs, docs, memory, and events around them. This skill is
the shortest path to using it well as an agent.

## First-run setup

```sh
ft init            # create ~/.factotum/config.toml; offer to register this repo as a project
ft init -u         # only the machine-scoped config, never project scope
ft init -p [name]  # register this directory as a project (id from the remote/dir, or an explicit name)
ft init -p --tech-stack go  # register a greenfield dir, recording the stack non-interactively
```

`ft init` writes the machine-scoped config (`~/.factotum/config.toml`) when missing and
never clobbers it. On a terminal, inside a git repository, it offers to register the
current directory as a project; `-p/--project` registers directly, deriving the id and
name from the git remote (else the directory name) unless a name is given. Registration
writes a `[projects.<id>]` entry with a `db_path` (default `~/.factotum/<id>.db`,
sqlite) to the machine config, pins `project = "<id>"` in the current directory's
`.factotum/config.toml`, and creates the project in its store. Registering a greenfield
directory also asks for the tech stack (language/framework) and records `tech_stack` in
the project config; use `--tech-stack` non-interactively (empty records nothing). Without
a terminal there are no prompts: `ft init` only creates the machine config and hints at `ft init -p`; `-u` and `-p` are mutually exclusive.

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
`ft serve` is an idea-centric live dashboard: ideas roll up promoted tasks, a kanban groups by
origin idea, idea/task/memory/doc drill down; reads open and SSE-live. The whole app is the
shadcn/ui app (`mise run build-web` → `web/dist`, embedded) at `/`,`/capture`,`/idea`,`/task`,`/memory`,`/doc`; `/capture` posts a sentence to the token-gated `/api/capture` as an idea or a bug. The message port rides the same token at `/api/msg/*` (`send`,`inbox`,`get`,`read`,`ack`,`nack`,`register`,`claim`,`heartbeat`,`deregister`) so a receiver on another host can message through the project backend; no token leaves it off.

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

## Capture and manage ideas and bugs

```sh
ft idea create -p <project> -t "A half-formed thought" -b "context"  # capture, not execution
ft idea promote <idea>                  # groom: create an executable task linked to the idea
ft bug create -p <project> -t "It crashes on save" -b "steps"  # capture a defect
ft bug triage <bug>                     # triage: create an executable task linked to the bug
```

`ft idea` and `ft bug` are non-executable capture surfaces over the same storage as
`ft task list -k idea`/`-k bug`: never in `ft task next` or the ready set, not
assignable or groomable, with only `todo`, `done`, and `cancelled`. Refinement
creates a linked `task`, keeps the capture as history (`ft task promote` is the peer),
and each surface also has `list`, `search`, and `get`.

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

Checks are advisory, never a gate; `ft task check` only writes its derived cache.
The `grooming` check judges the task's **body** (title, kind, description) and
reports `ready`, `needs_grooming` (agent-owned body edits), or `needs_human` (a
decision only the author can make - escalate, do not edit the body). It also
scores **decomposable** and **right-sized**, plus **entailment vs origin** for a
promoted task, and carries an auditable **delta** (title change, lines added or
dropped). The body is the spec; notes are history - a note never changes a
verdict, so fold a decision into the body. Recording a human override requires a
human actor; editing the body makes the verdict stale.

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
ft task get <task>              # description, origin, deps, dependents, notes, memory
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

Memory search is lexical by default; configuring an embedding provider (the
machine-scoped `[embed]` table, or `FACTOTUM_EMBED_PROVIDER`) adds vector recall,
finding paraphrases that share no tokens. Writes stay best-effort; `ft memory
reindex` backfills skipped vectors and repairs a model change. A detectable embed
failure - endpoint unreachable, model not served, or empty response - is reported
by cause and fix, not a generic error, and the failed embed is skipped once known.

## Message peers

```sh
ft msg send <address|role|ticket-id> <text> -p <project>; ft msg inbox -p <project> [--state <state>]   # ft msg read <id> marks read
ft msg install [--harness opencode|pi] [--dir <plugin-dir>]; ft msg agent register|claim|ack|nack|heartbeat|deregister   # receiver staging + protocol
```

A bare ticket id is sugar for `task:<id>` (send resolves it to a live run, else the task's agent assignee, else `task:<id>`). A bare **loop role** - `chief`, or `builder-<task>`/`reviewer-<task>`/`qa-<task>` - is sugar for `actor:<role>`, the stable mailbox a loop session registers as; `--from` accepts a role even before an `Actor` record exists, so peers address each other by role with no cmux surface. `ft msg read` is idempotent and `--from` defaults to the configured actor. `ft msg install` stages the receiver plugin where a harness loads it: OpenCode's global plugin dir by default (so a session launched directly, not through `ft run`, still loads it), or the `--dir` you pass; the plugin is inert without `FACTOTUM_PROJECT`/`FACTOTUM_ACTOR`. A receiver registers with `ft msg agent register`, long-polls `claim`, injects each message as a user turn, and `ack`s it; `ft run`'s OpenCode and pi harnesses stage that receiver (per the `FACTOTUM_*` env it sets) when the run has an actor. For a receiver on another host, the same protocol rides `ft serve` at `/api/msg/*` (token-gated by `serve.token`); the shipped receivers take the HTTP transport when `FACTOTUM_MSG_URL` points at a hub (auth via `FACTOTUM_SERVE_TOKEN`) and fall back to the local `ft` binary when it is unset. `ft run` sets both from `serve.url`/`serve.token` (or their `FACTOTUM_MSG_URL`/`FACTOTUM_SERVE_TOKEN` env), so a launched remote agent receives over the hub. A local `ft msg ...` and a remote HTTP client are two transports over one store.

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
- A branch build can forward-migrate the shared database; SQLite now guards this: a newer-schema
  database is rejected and an older one migrates only with `--store-opt migrate=yes` (after a backup);
  `ft version`/`ft skill` never open the store. If `ft` reports a newer schema, `mise run install` or
  use the newer branch binary; never point an uninstalled branch binary at the real DB.
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

A harness describes one agent CLI end to end: image, entrypoint, invocation,
model flag, prompt delivery, completion, and output parsing. Two ship: OpenCode
(`opencode`, image `ghcr.io/anomalyco/opencode`) and pi (`pi`, an npm package
with no published image). Each builds a headless invocation that passes the
prompt as the final message and reads the answer from stdout; an interactive run
attaches the agent's TUI instead. Model and credentials come from the configured
provider, never hardcoded (a credential is a provider reference the backend
resolves). A local run uses the host binary (dev-only); an isolating pi backend
needs an explicit image. The pi harness trusts project-local files, so its
staged receiver extension loads.

An isolating backend imposes a policy on the run. The OpenShell policy
(`pkg/isolation/openshell`, checked-in template `policy.yaml`) is deny-by-default:
the sandbox reaches no host until a project opts in, except the model provider
endpoint (`openrouter.ai` for OpenRouter), which the credential provider attaches
from `run.provider` + `run.credential_env`. OpenCode's phone-home to
`models.opencode.ai` and `registry.npmjs.org` is optional and stays denied until a
project opts in. The workload runs non-root with no credential value in the
sandbox; filesystem and the `manual` advisor are pinned by `policy.yaml`.

A project opts in to extra hosts by committing
`.factotum/openshell-policy.yaml`:

```yaml
allow_hosts:
  - models.opencode.ai
  - registry.npmjs.org
```

Only `allow_hosts` is honored. Any other key is rejected, so a project can widen
egress but never weaken the identity, filesystem, or credential defaults. `ft run`
resolves it next to the active project config (a custom `-c` uses its sibling
`openshell-policy.yaml`); with no policy file it warns that egress is deny-all.

`ft run <task>` drives one task end-to-end: it resolves the task's repositories into a
workspace, runs the harness with the task as its prompt, and reflects progress into the store.

```sh
ft run t-abc123 --sandbox local --harness opencode --allow-host   # dev-only opt-in
ft run --prompt-file grooming.md --sandbox local --harness opencode --allow-host  # task-less
```

- `--prompt-file <path>` or `--prompt-artifact <id>` supplies a stored prompt: with a task it
  replaces the task prompt, and with no task id it runs the prompt once over every repository
  of the configured project (a task-less session such as grooming), writing nothing to the task
  graph. They are mutually exclusive; a missing or empty `--prompt-file` is an input error (exit 1). On a terminal a single-task or task-less run runs **interactive** (the agent attached, the OpenCode TUI) so a human can answer it; `--unattended`, a piped/redirected stdin/stdout, or `--goal` runs **headless**. Interactive output stays live and is not captured; it needs a backend that can attach a terminal (`local`; not `openshell`/`docker`, which tell you to rerun with `--unattended`). A **headless** run narrates live progress on stderr so a multi-minute harness is visibly alive: `ft: run <task> (sandbox=<b> harness=<h>) started`, then a periodic `still running (Ns)` heartbeat. Progress is written only when stderr is a terminal and output is text; `-o json|yaml`, a piped/redirected stderr, and an interactive run print none.
- `--sandbox`/`--harness` are optional: they resolve flag > `FACTOTUM_RUN_*` > committed
  project `[run]` > machine `[run]`. On a terminal an unset one prompts once and saves to
  the chosen config; choosing `local` there asks to opt in (default no) and records
  `run.allow_host = true` in the user config. A committed project `[run]` may set only `sandbox`/`harness`; the machine table also holds workspace/model/args/allow_host/credential_env. `--refresh` hard-resets a reused checkout; a changed origin URL fails.
- `provider` and `credential_env` name the credential a harness may use and the
  variable it arrives under, e.g. `provider = "openrouter"` with
  `credential_env = "OPENROUTER_API_KEY"`; the value is read from the host
  environment and handed to the isolating backend, never placed in the sandbox.
- The `local` backend is unsandboxed and refuses to run until explicitly opted in
  with `--allow-host`, `run.allow_host`, or the interactive prompt above; the opt-in
  is machine-scoped and is ignored from the committed project file.
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
