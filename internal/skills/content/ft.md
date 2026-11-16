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
directory also asks for the tech stack and records `tech_stack`; use `--tech-stack`
non-interactively (requires `-p`); read it back with `ft config get [key]`. Without a
terminal there are no prompts: `ft init` only creates the machine config and hints at `ft init -p`; `-u` and `-p` are mutually exclusive.

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
ft serve                        # live dashboard + token-gated capture; also -p <project>, --all, --bind <host:port>, --token <secret>
```
`ft serve` is an idea-centric live dashboard: ideas roll up promoted tasks, a kanban groups by
origin idea, idea/task/memory/doc drill down; reads open and SSE-live. The whole app is an embedded
shadcn/ui SPA served at `/`,`/capture`,`/idea`,`/task`,`/memory`,`/doc`; `/capture` posts a sentence to the token-gated `/api/capture` as an idea or a bug, and its prompt box stores a free-form prompt as a document artifact and ft-msgs the chief at the token-gated `/api/prompt`. The message port rides the same token at `/api/msg/*` (`send`,`inbox`,`get`,`read`,`ack`,`nack`,`register`,`claim`,`heartbeat`,`deregister`) so a receiver on another host can message through the project backend. Every write side authenticates with `Authorization: Bearer <token>`. The token is resolved from `--token`, then `FACTOTUM_SERVE_TOKEN`, then `serve.token` in the machine config; an explicitly provided token is used as-is and never printed. When none is set on a scoped serve, a fresh random token is minted each start and printed to the console for you to copy; with no token and `--all` (no single capture target), capture is disabled and the read side stays open.

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
ft idea promote <idea> --acceptance "<observable result>" --actor <agent>  # groom: linked, agent-ready task
ft bug create -p <project> -t "It crashes on save" -b "steps"  # capture a defect
ft bug triage <bug> --acceptance "<observable result>" --actor <agent>     # triage: linked, agent-ready task
```

`ft idea` and `ft bug` are non-executable capture surfaces over the same storage as
`ft task list -k idea`/`-k bug`: never in `ft task next` or the ready set, not
assignable or groomable, with only `todo`, `done`, and `cancelled`. Refinement
creates a linked `task`, keeps the capture as history (`ft task promote` is the peer),
and each surface also has `list`, `search`, and `get`. Promotion requires grooming.

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
ft task note backfill -p <project> [--dry-run]  # mark older generated notes; --help lists families; one-way, no unmark
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
proposes, in the `ft task apply` document shape. Nothing is created until `-y`;
applying is idempotent — a proposed task whose **title** already matches one in the
project or earlier in the plan is skipped, not duplicated (case-insensitive,
whitespace-trimmed) and reported (text marks them `skipped: true`; `-o json|yaml` returns a `{created, skipped}` object). The agent CLI comes from the machine-scoped `[agent]` table, `FACTOTUM_AGENT_COMMAND`, or the `--agent-command` flag (which wins for one invocation):

```toml
[agent]
command = "my-agent-cli"   # reads the instruction on stdin, prints JSON on stdout
```

Without a configured agent, `ft prompt` reports a clear error.

## Find things

```sh
ft task list -p <project> --status todo
ft task search "<query>"        # titles, descriptions, and notes (terms ANDed, prefix match)
ft task get <task>              # description, origin, blocking deps, grouped-under captures, dependents, notes, memory
ft task context <task>          # task + deps + notes + memory + recent events
ft doc search "<query>"         # specs and docs (terms ANDed, prefix match)
ft doc get <artifact>           # read one spec/doc back
ft memory search "<query>"      # agent memory (same lexical search)
ft memory get <memory>          # read one memory back
```

`ft task search` ranks a title match above a description match above a note match,
with deterministic ties. Notes created with `--system` (generated triage or
reassignment messages) are kept on the task but excluded from search indexing.
When a judge is configured it reranks the shortlist by meaning; `--no-rerank` keeps
the lexical order, and a search never fails because the judge is absent or slow.

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
ft msg send <address|role|ticket-id> <text> -p <project>; ft msg inbox -p <project> [--state <state>]   # ft msg read <id> shows and marks read
ft msg install [--harness opencode|opencode2|pi] [--dir <plugin-dir>]; ft msg runs [-p <project>] [--live]; ft msg agent register|claim|ack|nack|heartbeat|deregister   # receiver staging, run liveness, protocol
```

A bare ticket id is sugar for `task:<id>` (send resolves it to a live run, else the task's agent assignee, else `task:<id>`). A bare **loop role** - `chief`, or `builder-<task>`/`reviewer-<task>`/`qa-<task>` - is sugar for `actor:<role>`, the stable mailbox a loop session registers as; `--from` accepts a role even before an `Actor` record exists, so peers address each other by role with no cmux surface. An actor-valued flag (`--from`, `--for`, `--on`, `--actor`, including `ft task assign --actor`) and an `actor:` send target accept a bare id or name or the canonical `actor:<id>`, normalizing the address form to the bare id, so the canonical form never doubles into `actor:actor:<id>`; a `run:`/`task:` address or a malformed or doubled actor address is rejected, never stored verbatim. Prefer the canonical `actor:<role>` address; a bare target that is neither a loop role nor a `t-...` ticket id is rejected as invalid rather than coerced into ticket sugar. The OpenCode receiver waits for its session before claiming and `nack`s (releases) a message it cannot inject instead of acking it failed, so a handoff is never silently dropped. `ft msg read` is idempotent and `--from` defaults to the configured actor. `ft msg runs` lists the registered receiver runs with their liveness, and `ft msg runs --live` is the channel check: it keeps only valid leases, so a busy project's full run list cannot bury your own run. `ft msg install` stages the receiver plugin where a harness loads it: OpenCode's global plugin dir by default (so a session launched directly, not through `ft run`, still loads it), or the `--dir` you pass; the plugin is inert without `FACTOTUM_PROJECT`/`FACTOTUM_ACTOR`. Install is a no-op when the staged file already matches the embedded receiver, so re-running it (every loop-agent launch does) never rewrites the file and never makes OpenCode's plugin watcher reload a running agent's receiver. A reloaded V2 receiver resumes the OpenCode session it persisted on the previous load, so an idle agent still claims a queued handoff instead of waiting for an event an idle session never emits. The receiver's single default export carries both majors' entrypoints - V1's `server()` and V2's `Plugin.define({ id, setup })` from `@opencode/plugin` - so one staged `factotum-msg.js` loads under either; V2 is selected with `--harness opencode2` and stages into the same global `<config>/opencode/plugins` (both majors also discover the project-local `.opencode/plugin/`). A receiver registers with `ft msg agent register` (passing the chief session via `--session` or `FACTOTUM_SESSION`), long-polls `claim`, injects each message as a user turn, and `ack`s it; `ft run`'s OpenCode and pi harnesses stage that receiver (per the `FACTOTUM_*` env it sets) when the run has an actor, and launch OpenCode V2 on its own private server (--standalone) instead of the shared background service, whose environment carries no `FACTOTUM_*` and would leave the staged receiver inert. For a receiver on another host, the same protocol rides `ft serve` at `/api/msg/*` (token-gated by `serve.token`); the shipped receivers take the HTTP transport when `FACTOTUM_MSG_URL` points at a hub (auth via `FACTOTUM_SERVE_TOKEN`) and fall back to the local `ft` binary when it is unset. `ft run` sets both from `serve.url`/`serve.token` (or their `FACTOTUM_MSG_URL`/`FACTOTUM_SERVE_TOKEN` env), so a launched remote agent receives over the hub; a URL without a token is a half-configured hub, so a run with an actor warns and drops it rather than launch a receiver that fails every message (a run with no actor launches no receiver and stays quiet). A local `ft msg ...` and a remote HTTP client are two transports over one store.

## Chief sessions

A chief session is an identity for one chief run and the work it drives; actor ids stay role-scoped, the session is a separate field. `ft session start -p <project> [--session <sid>]` records `session.started` (minting an id when none is given); `ft session retire ... [--successor <sid>]` records `session.retired`. `loop-agent.sh` mints a new id per chief launch and exports `FACTOTUM_SESSION`, which builders/reviewers/QA inherit; every `ft` call attributes its events to the session (`FACTOTUM_SESSION`), queryable with `ft event list --session <sid>`. `ft session ack` records a `session.acked` event from the id the caller's environment carries (never from an argument), so a run can require proof that a launched child picked the id up; `ft groom --unattended` requires it before it judges the tasks the session produced. A run may name its own session id explicitly (`ft groom`
injects the grooming session id) so what it creates is attributed to it, not the caller; every other run inherits the caller's session when it has one, else mints a run-scoped one, and that attribution
is what lets `ft groom` judge exactly the tasks its session produced. The caller actor is resolved per invocation by precedence `--actor` > `FACTOTUM_CALLER` > `FACTOTUM_DEFAULT_ACTOR` > project/user `default_actor`; each accepts either the bare id or the canonical `actor:<id>`. `FACTOTUM_CALLER` is the authoritative detection channel: a well-formed name that matches a registered actor stamps the events; a well-formed name matching none is dropped with a warning (no attribution, no auto-create, no failure), so a typo never silently misattributes; a malformed value (a `run:`/`task:` address, or a doubled `actor:actor:<id>`) fails the invocation rather than misattribute. An explicit `--actor` follows the same rule as an unregistered caller: a name matching no Actor record and none of the four loop-role prefixes above is dropped with a warning, so a typo neither stamps events nor becomes the auto-QA reminder target. A configured default (`FACTOTUM_DEFAULT_ACTOR`, `default_actor`) is attributed when it names an actor registered in this invocation's store or is a role-shaped identity the loop exports - `chief`, or one of `builder-<task>`, `reviewer-<task>`, `qa-<task>`, `grooming-agent-<artifact>`, `architecture-reviewer-<session>`, each scoped by a well-formed id (a task `t-`, an artifact `art-`, a grooming session `groom-`); it needs no Actor record, and a default that is neither is dropped with a warning, so a typo there is not misrouted either. A default that merely starts with a role name (`builder-gpt-4`) is not a role; `--actor` is deliberately looser and still exempts any non-empty role prefix. Name those roles as the default, not as an explicit `--actor`. `ft task claim` resolves its configured default the same way, so a role-only session can claim (the task is assigned to the role) and an unregistered non-role default warns and then fails the claim exactly as if none were set. Readiness and grooming classify an assignee by that same rule, so a task a loop role holds with no Actor record is agent-next and agent-ready, while a human or a role lookalike stays human. Every actor-valued flag that names work - `ft task assign --actor`, `ft task wait --on`, `ft task claim --for`, `ft task next --for` and its `--explain`, and `ft idea promote`/`ft bug triage`/`ft task promote --actor` - resolves by that same identity rule: the Actor record first (by id or name), else a loop role as its agent identity. Assigning, waiting on, claiming, or promoting to a role therefore needs no record, and a role-held task stays agent-next and agent-ready. A registered record always wins, even when its id is role-shaped (so promoting to a registered human still fails as not an agent); a lookalike that is neither a record nor a role is still an unknown actor. Attribution is separate and follows its own rules: the caller's global `--actor` keeps the drop-with-a-warning behavior above, while `ft msg send --from` accepts a registered record or a loop-role sender without one and fails an unknown non-role sender. A note records that attributed caller as its `author` - the same actor that stamps its event - so a note a loop role wrote is authored, and `ft task note backfill` does not read it as machine text. Environment identity is **spoofable** - any process can export `FACTOTUM_CALLER` - so it is a convenience signal, never a trustworthy one; do not gate security on it. With no identity named at all - no `--actor`, no `FACTOTUM_CALLER`, no configured default - `ft` makes a best-effort guess so the silence is explained: it checks the environment markers the supported harnesses export (`CLAUDECODE`/`CLAUDE_CODE_ENTRYPOINT`/`CLAUDE_CODE_CHILD_SESSION` for Claude Code, `CODEX_THREAD_ID`/`CODEX_CI` for Codex CLI, `CURSOR_AGENT`/`CURSOR_TRACE_ID` for Cursor, `OPENCODE`/`OPENCODE_SESSION_ID` for OpenCode, `PI_CODING_AGENT` for pi, `GEMINI_CLI`, `QWEN_CODE`), then the inherited generic `AI_AGENT`, then the nearest ancestor process named for a known agent binary (`claude`, `codex`, `cursor-agent`, `opencode`, `gemini`, `qwen`, `aider`, `goose`, `pi`). Every guess is reported on stderr as an **unverified heuristic** naming its evidence (and whether stdin is a terminal) and saying the invocation stays unattributed; it is never recorded as an actor - not an event stamp, note author, assignee, or reminder target - is never auto-registered, and never overrides a named identity, including one dropped as unregistered, which keeps its own warning. A terminal on stdin is evidence only and never produces a guess on its own. Both signals are spoofable (any process can export a marker, any binary can be named `claude`), and a nested agent passes the outer harness's markers down, so the guess names the outermost marker found: treat it as a nudge to set `FACTOTUM_CALLER` or `--actor`, never as identity or a security boundary.

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
  `.factotum/config.toml`, `default_project` in `~/.factotum/config.toml`);
  mutating commands error without a default, list/filter commands fall back to all.
- `-o json|yaml` is the machine-readable interface. Text output is yaml-like
  `key: value` lines for single results and tables for lists.
- Large output is bounded so it cannot flood an agent's context: when stdout is not a terminal and
  output exceeds 32 KiB or 400 lines, `ft` spills the full text to a temp file and prints a head/tail
  window plus its path; pass `--full` to print everything, `FACTOTUM_MAX_OUTPUT=<bytes>` to set a byte
  budget, or `=unlimited` to disable it (interactive output is never bounded; `-o json|yaml` truncates).
- Never edit a database by hand: go through `ft`.
- A branch build can forward-migrate the shared database; SQLite now guards this: a newer-schema
  database is rejected and an older one migrates only with `--store-opt migrate=yes` (after a backup);
  `ft version`/`ft skill` never open the store. If `ft` reports a newer schema, install a newer `ft`
  that matches it; never point an uninstalled branch binary at the real DB.
- `ft task set <id> field=value ...` updates fields, including `not_before=YYYY-MM-DD`
  (or `+7d`) to defer a task and `not_before=` to clear it.

## Agent runs and isolation

`ft run <target>` drives one task end-to-end: it materializes the task's **declared** repo
(`task -r`), or all the project's repos when none is declared — an exclusive or, never a union.
A task that needs another repo must declare it, or the agent clones it manually; `ft run` runs
the harness with the task as its prompt. A capture (idea or bug) runs the goal loop below over
every task that belongs to it, in DAG order. A repo registered with a local `Path` (not a `URL`)
is used **in place** — not copied into the workspace — so a backend on the workdir (`local`,
`docker`) can modify that checkout; `ft run` warns on stderr naming it, while a `URL` repo runs
in an isolated copy (`ft groom` resolves and warns the same way). The launched session inherits
the run's project, store, and session (`FACTOTUM_PROJECT`/`FACTOTUM_STORE`/`FACTOTUM_STORE_OPTS`/
`FACTOTUM_SESSION`), so the `ft` it runs never resolves the checkout's config (your real
`~/.factotum`, say) and its events land on the run's session. The session is the caller's own when
this invocation inherited one (a nested run rolls up to the loop's timeline, exactly as
`loop-agent.sh` does for its children), else a freshly minted run-scoped id; an explicitly named id
(a groom session's own) wins. The run prints the id as `session:` (a `--goal` loop prints the one id
its tasks share), so `ft event list --session` and the sessions page follow the run and its agent's
work. A run with an actor exports `FACTOTUM_ACTOR`, `FACTOTUM_TASK_ID`, `FACTOTUM_DEFAULT_ACTOR` (what
a bare `ft msg send` is attributed to); every run exports `FACTOTUM_HARNESS`, so a registered run
reports `opencode2`, not `opencode`. A run whose actor resolves to no record and no loop role registers
no receiver, so it warns and names `ft actor create <name>`; a caller has no role exemption. The
warning is per launch: a `--goal` loop with no ready work is quiet.

```sh
ft run t-abc123 --sandbox local --harness opencode2 --allow-host   # dev-only opt-in
ft run --prompt-file grooming.md --sandbox local --harness opencode2 --allow-host  # task-less
ft run --goal t-abc123 --sandbox local --harness opencode2 --allow-host --max-tasks 5
```

A run is a harness (one agent CLI end to end: image, invocation, model flag, prompt
delivery, output parsing) inside an isolation backend. The `local` backend
runs the harness directly on the host with no isolation: dev-only, never the default, and
refused until explicitly opted in with `--allow-host` or `run.allow_host` (machine-scoped,
ignored from the committed project file). It can read host credentials, reach the network,
and mutate files, so never use it for untrusted input; the isolating backends (`openshell`,
`docker`) are the only ones fit for that. Prefer OpenCode V2 (`opencode2`); `opencode` (V1)
and `pi` also ship.

`--sandbox`/`--harness` resolve flag > `FACTOTUM_RUN_*` > committed project `[run]` >
machine `[run]`; on a terminal an unset one prompts once and saves to the chosen scope,
while non-interactively it is an error. A committed project `[run]` may set only those
two; the machine table holds the rest (workspace/model/image/user/args/allow_host/
credential_env), and `--image` names an isolating backend's base image (`pi` needs one).
`--user`/`run.user`/`FACTOTUM_RUN_USER` sets the container identity: on `docker` a uid, uid:gid,
or name, defaulting to empty so the image's own `USER` applies (a root `USER` writes root-owned
files into the bind-mounted checkout, so set `$(id -u):$(id -g)`); on `openshell` numeric ids or
the literal `sandbox`, seeding `run_as_user`/`run_as_group` and otherwise keeping the hardened
`1000:1000` (a root request is refused); `local` ignores it. `provider` + `credential_env` name
the credential a harness may use, read from the host environment and handed to an isolating
backend, never placed in the sandbox, and ignored by `local`, which runs the host binary with
the host's own credentials.

An isolating backend is deny-by-default: no host is reachable until the project opts in, except
the model provider endpoint the credential attaches. Committing `.factotum/openshell-policy.yaml`
with `allow_hosts` and nothing else widens egress but never weakens the identity, filesystem, or
credential defaults; with no policy file `ft run` warns egress is deny-all. The README's "Run
agents" and "Isolation policy" sections carry the rest.

- `--prompt-file <path>` or `--prompt-artifact <id>` supplies a stored prompt: with a task
  it replaces the task prompt, and with no task id it runs the prompt once over every
  repository of the selected project (a task-less session such as grooming), writing
  nothing to the graph. They are mutually exclusive; a missing or empty `--prompt-file`
  is an input error (exit 1). On a terminal a single-task or task-less run runs
  **interactive** (the agent attached) so a human can answer it, and its run note records
  the outcome only; `--unattended`, a piped/redirected stdin/stdout, or `--goal` runs
  **headless**. Interactive needs an attaching backend (`local`; not
  `openshell`/`docker`, which say to rerun with `--unattended`). A headless run narrates
  on stderr — `ft: run <task> (sandbox=<b> harness=<h>) started`, then a periodic `still
  running (Ns)` heartbeat — only when stderr is a terminal and output is text.
- `-p/--project` picks the project whose store the run reads and writes (else the configured one); a target it does not hold is not found (exit 3).
- `ft run --goal <task|milestone|capture>` repeatedly runs the highest-ranked agent-ready
  on-path task until the goal is reached, work stalls, or `--max-tasks N` (0 = no budget)
  is exhausted. Only groomed work assigned to an agent is run; startable but
  non-agent-ready on-path work is left for a human and named in `not_run:`. A milestone
  is a human gate, so the loop runs its prerequisites and stops `no_ready_work`. A
  capture is done when every task carrying it as its origin edge resolves; one that owns
  no runnable task is rejected (`invalid:`, exit 5), naming the gates when its only
  children are milestones.
- A failed run leaves the task's status untouched and stops the loop for a human. The
  stop reason is `stop:` (`goal_reached`, `no_ready_work`, `blocked`, `budget_exhausted`,
  or `failed`), and each iteration prints a compact `step:` block.
- A successful run moves the task to `ready_for_review` (`session: <id>`, `run: finished`,
  `exit_code: 0`, `complete: true`, a note of the output and `task.run_started`/`task.run_finished`);
  a failed one is `run: failed`/`complete: false` with only a failure note. Either prints the
  harness's stderr tail (4 KiB) as a `stderr:` block and a json/yaml `stderr` field.

## Getting help

```sh
ft <command> --help
ft skill list
ft skill get <name>
ft skill lint             # check the embedded skills against the live CLI (deterministic)
ft skill lint --semantic  # also judge prose references (needs a TypeSafe key)
```
