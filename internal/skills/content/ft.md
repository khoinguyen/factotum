# ft

`ft` manages projects and the human/agent task graph: the tasks, their
dependencies, and the specs, docs, memory, and events around them. This skill is
the shortest path to using it well as an agent.

## Start from the graph, not from guesswork

```sh
ft task next                    # the highest-ranked ready task (uses the default project)
ft task next --for <actor>      # what a specific human or agent should pick up
ft task next -n 5               # a shortlist
ft task next --explain          # why tasks are excluded; honors --for/--label/--repo/-n
ft task get <task>              # full detail; its Next: block names the follow-up command
ft graph render --project <p>   # the whole DAG as text
```

`ft task next` ranks ready tasks by what unblocks the most work, proximity to a
milestone, the `--toward` target, and priority. Use it instead of scanning the
list by hand. When a task is missing from the ranking, `ft task next --explain`
lists each excluded task with its reason, applying the same
`--for`/`--label`/`--repo`/`-n` filters as ranking, and `ft task get <task>`
prints a `not ready because:` line for the same reason.

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

The body is the spec; notes are history. A note never changes a verdict, so fold
a decision into the body to make it count. Recording a human override requires a
human actor; it stops agents re-grooming, and editing the body makes it stale.

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
- Large output is bounded so it cannot flood an agent's context. When stdout is
  not a terminal and output exceeds 32 KiB or 400 lines, `ft` spills the full
  text to a temp file and prints the first 60 and last 20 lines plus that file's
  path; the file stays for the OS temp cleaner, so read it for the full text.
  Pass `--full` to print everything instead; `FACTOTUM_MAX_OUTPUT=<bytes>` sets a
  byte budget (the line threshold no longer applies) and
  `FACTOTUM_MAX_OUTPUT=unlimited` disables the bound. `-o json|yaml` becomes an
  envelope `{truncated, full_output_path, preview}`. On a terminal, output is
  never bounded.
- Never edit a database by hand: go through `ft`.
- A branch build can forward-migrate the shared database. If `ft` reports a schema version newer
  than it supports, update the installed binary (`mise run install`) or use the newer branch binary;
  never point a non-installed branch binary at the real project DB — exercise CLI changes against a
  throwaway store (`--store jsonfile --store-opt path=$(mktemp -d)/db.json`) or a temp config.
- `ft task set <id> field=value ...` updates fields, including
  `not_before=YYYY-MM-DD` (or `+7d`) to defer a task and `not_before=` to clear
  it.

## Getting help

```sh
ft <command> --help
ft skill list
ft skill get <name>
ft skill lint             # check the embedded skills against the live CLI (deterministic)
ft skill lint --semantic  # also judge prose references (needs a TypeSafe key)
```
