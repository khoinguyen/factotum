# ft

`ft` manages projects and the human/agent task graph: the tasks, their
dependencies, and the specs, docs, memory, and events around them. This skill is
the shortest path to using it well as an agent.

## Start from the graph, not from guesswork

```sh
ft task next                    # the highest-ranked ready task (uses the default project)
ft task next --for <actor>      # what a specific human or agent should pick up
ft task next -n 5               # a shortlist
ft task get <task>              # full detail; its Next: block names the follow-up command
ft graph render --project <p>   # the whole DAG as text
```

`ft task next` ranks ready tasks by what unblocks the most work, proximity to a
milestone, the `--toward` target, and priority. Use it instead of scanning the
list by hand.

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

## Record what you learn and file new work

```sh
ft task note create <task> -b "Decision: ..." --link issue=https://...
ft task create -p <project> -t "Short imperative title" -r <repo> \
  --body "Context and acceptance criteria." --dep <blocking-task>
```

A bug, TODO, missing test, or follow-up spotted while working is a task: create
it and link it (a note, or `--dep`). Update the graph in the same session as the
code.

## Find things

```sh
ft task list -p <project> --status todo
ft task get <task>              # description, deps, dependents, notes, memory
ft task context <task>          # task + deps + notes + memory + recent events
ft doc search "<query>"         # specs and docs (terms ANDed, prefix match)
ft memory search "<query>"      # agent memory (same lexical search)
ft memory get <memory>          # read one memory back
```

## Memory

Memory is a first-class, searchable artifact for durable agent knowledge:

```sh
ft memory create -p <project> -t "Title" -b "What to remember"
ft memory list -p <project>
ft memory search "<query>" -p <project>
ft memory get <memory>
ft memory update <memory> [-t "Title"] [-b "Content" | -f file] [--task <task>]
ft memory delete <memory>
```

`ft memory update` patches in place; `--task <task>` attaches the memory to a
task and `--task ""` detaches it. Only memory artifacts are accepted: the verbs
reject specs and docs.

## Conventions that matter

- `-p/--project` falls back to the configured default (`project` in
  `.factotum/config.toml`, `default_project` in `~/.factotum/config.toml`).
  Mutating commands error without a default; list/filter commands fall back to
  all.
- `-o json|yaml` is the machine-readable interface. Text output is yaml-like
  `key: value` lines for single results and tables for lists.
- Never edit a database by hand: go through `ft`.
- `ft task set <id> field=value ...` updates fields, including
  `not_before=YYYY-MM-DD` (or `+7d`) to defer a task and `not_before=` to clear
  it.

## Getting help

```sh
ft <command> --help
ft skill list
ft skill get <name>
```
