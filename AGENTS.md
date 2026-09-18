# AGENTS.md

Guidance for AI coding agents working in this repository. Read this before making changes.

## What this is

Factotum is a toolset for managing agentic coding work. It manages **projects** (which may span
multiple repositories) and the **task graph** of mixed human/agent work, along with the specs,
docs, and memory around them. The first tool is the `ft` CLI. The Go module, config directory
(`.factotum/`), and environment prefix (`FACTOTUM_`) keep the project name.

The project is developed test-first and, eventually, managed by `ft` itself.

## Behavioral guidelines

Behavioral guidelines to reduce common LLM coding mistakes. They bias toward caution over speed;
for trivial tasks, use judgment, and merge them with the project-specific rules that follow.

### 1. Think before coding

**Don't assume. Don't hide confusion. Surface tradeoffs.**

Before implementing:

- State your assumptions explicitly. If uncertain, ask.
- If multiple interpretations exist, present them — don't pick silently.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop. Name what's confusing. Ask.

### 2. Simplicity first

**Minimum code that solves the problem. Nothing speculative.**

- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- No error handling for impossible scenarios.
- If you write 200 lines and it could be 50, rewrite it.

Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify.

### 3. Surgical changes

**Touch only what you must. Clean up only your own mess.**

When editing existing code:

- Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Match existing style, even if you'd do it differently.
- If you notice unrelated dead code, mention it — don't delete it.

When your changes create orphans:

- Remove imports/variables/functions that YOUR changes made unused.
- Don't remove pre-existing dead code unless asked.

The test: every changed line should trace directly to the user's request.

### 4. Goal-driven execution

**Define success criteria. Loop until verified.**

Transform tasks into verifiable goals:

- "Add validation" → "Write tests for invalid inputs, then make them pass"
- "Fix the bug" → "Write a test that reproduces it, then make it pass"
- "Refactor X" → "Ensure tests pass before and after"

For multi-step tasks, state a brief plan:

```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
3. [Step] → verify: [check]
```

Strong success criteria let you loop independently. Weak criteria ("make it work") require constant
clarification.

**These guidelines are working if:** fewer unnecessary changes in diffs, fewer rewrites due to
overcomplication, and clarifying questions come before implementation rather than after mistakes.

## Golden rule: TDD, always

Every unit of behavior is developed test-first. Do not write implementation before its tests.

1. **Brainstorm the tests** — decide what behavior is observable and how to assert it.
2. **Write the tests** — table-driven where it helps; name the cases.
3. **RED** — run `mise run test` and confirm the new tests fail *for the right reason*.
4. **Implement** the minimum code needed to pass.
5. **GREEN** — run `mise run test`; all tests pass.
6. **Refine / add the next tests** — cover edge cases, then repeat from RED.

Never claim a unit is done because the code "looks right". Run the tests.

## Definition of done for a unit

- Tests were written first and failed before the implementation existed.
- `mise run test` is green (`go test -race ./...`).
- `mise run fmt` has been applied and `mise run fmt-check` is clean.
- `mise run lint` is clean.
- Warnings are errors when the fix is trivial. Any deliberately ignored warning (lint
  suppression, skipped test, `//nolint`) requires **explicit human approval**, and the reason is
  documented inline next to it.
- Public behavior is documented; there is no dead code.
- The work is refactored for clarity and simplicity after it is green.

## Commands (mise)

| Command | Purpose |
| --- | --- |
| `mise install` | Install pinned tools (Go, golangci-lint). |
| `mise run fmt` | Format the tree. |
| `mise run fmt-check` | Verify formatting (used by CI). |
| `mise run lint` | Run golangci-lint. |
| `mise run test` | `go test -race ./...`. |
| `mise run cover` | Tests with a coverage profile. |
| `mise run build` | Build the CLI. |
| `mise run hooks` | Install the git pre-commit hook. |
| `mise run ci` | `fmt-check`, `lint`, `test`, `cover`, `build`. |

Run `mise run ci` before considering any task complete.

## Architecture

The codebase is hexagonal (ports and adapters) with everything pluggable from day one.

```
cmd/factotum        entrypoint: builds registries, registers builtins, runs the CLI
internal/cli        Cobra command tree and command registry
internal/config     config loading (file + env + flags)
pkg/registry        generic Registry[T] used by every plugin point
pkg/core            domain entities, statuses, ResolutionPolicy, validation
pkg/graph           DAG engine: readiness, waves, cycles, ranking primitives
pkg/store           storage ports (Backend and repositories), errors, factories
pkg/store/<impl>    storage adapters (memory, jsonfile, sqlite)
pkg/store/conformance  the shared contract suite every backend must pass
pkg/rank            next-task ranking plugins
pkg/render          graph/output renderer plugins
pkg/app             use-case services orchestrating core and ports
```

Rules:

- `pkg/core` is pure: it must not import any infrastructure, storage, CLI, or network package.
- Dependencies point inward: adapters depend on ports and core, never the reverse.
- New capabilities are registered as plugins (storage backends, rankers, renderers, commands),
  not hardcoded into core.
- Every backend must pass `pkg/store/conformance`; that suite is the definition of a backend.

## Search and navigation tooling

Prefer `rg`, `fd`, and `ast-grep` over `grep`/`find`/`sed`. They respect `.gitignore`, are much
faster, and understand Go syntax. Use them before reaching for ad-hoc scripts.

### ripgrep (`rg`) — content search

```sh
rg 'func \(s \*TaskService\)' pkg/app            # methods on a type (note the escaped parens)
rg -t go 'ResolutionPolicy'                      # Go files only
rg -l 'StatusReadyForReview'                     # list matching files
rg -C 3 'wouldCycle' pkg/app/task.go             # 3 lines of context
rg -U 'func.*\{\n(?:.*\n)*?\}' -t go pkg/app    # multiline pattern (-U)
rg --files-with-matches --glob '!*_test.go' 'AddDep'
rg -o 'core\.Status[A-Za-z]+' pkg | sort -u      # extract distinct tokens
rg --stats 'TODO|FIXME'                          # match count summary
```

### fd — file discovery

```sh
fd -e go -x gofmt -l                             # find unformatted Go files
fd 'template' pkg/render                         # filename matching (regex, smart case)
fd -e go -t f . internal/cli                     # files only under a path
fd -e go -x golangci-lint run --fix             # run a command on matches (-x)
fd -H -I '\.db$' .factotum                       # include hidden/ignored
fd . -e go --changed-within 1d                    # recently modified
```

### ast-grep (`ast-grep`/`sg`) — structural Go search and rewrite

```sh
sg run -p 'fmt.Sprintf($$$)' -l go               # find calls structurally ($$$ = any args)
sg run -p 'if err != nil { return err }' -l go   # exact shape, ignoring formatting
sg run -p 'return deps.emit($V, $F)' -l go internal/cli  # capture metavariables
sg run -p 'deps.printf($$$)' -l go internal/cli --rewrite 'deps.printf($$$)'  # dry-run rewrite
sg run -p '$X.Tasks.Get($CTX, $ID)' -l go        # who calls Get
sg scan -r sgconfig.yml                           # run project rules (if configured)
```

`sg` rewrites in place with `--update-all`; review the diff first.

## Conventions

- Go standard formatting, enforced by `gofmt`/`goimports`.
- Comments explain *why*, not *what*. Do not add comments that restate the code.
- Errors are wrapped with `%w` and carry context; sentinel errors live in the package that owns them.
- Prefer small interfaces, explicit dependencies, and constructor injection.
- Table-driven tests for anything with more than one interesting case.
- Prefer determinism. Inject clocks and IDs where tests need them; renderers must be byte-stable.

## Dogfooding: use `ft` for the work itself

This repository is managed by `ft`. The project id is `factotum`; `.factotum/config.toml` pins it
and `~/.factotum/config.toml` maps it to its database (see Configuration below). Do the work
through the tool, not around it.

**Start from the graph, not from guesswork.** Begin each session by asking `ft` what to do next:

```sh
ft task next                                        # default_project resolves from .factotum/config.toml
ft task next --all                                  # rank ready work across every registered project
ft task next -n 5 --for <actor>                     # what a specific human or agent should pick up
ft task show <task>                                 # its Next: block names the natural follow-up command
ft graph render --project factotum --format agent   # the whole DAG as text
```

**Move a task through its lifecycle as you work** (each status command prints the next hint):

```sh
ft task start <task>    # todo -> in_progress
ft task review <task>   # in_progress -> ready_for_review (unblocks dependents)
ft task done <task>     # accepted and finished
ft task reopen <task>   # back to todo, e.g. after a failed review
```

**Record what you learn and file new work the moment you discover it.** Never leave a decision,
TODO, or follow-up undocumented or the graph stale:

```sh
ft task note add <task> -b "Decision: ... " --link issue=https://...
ft task add -p factotum -t "Short imperative title" -r factotum \
  --body "Context and acceptance criteria." --dep <blocking-task>
```

- A bug, TODO, missing test, cleanup, or follow-up spotted while working → add a task and link it
  (a note, or `--dep`).
- Use `-r <repo>` when the work belongs to one repository of the project.
- Update the graph in the same session as the code: new tasks/milestones, statuses, notes.

## Configuration

Two scopes, merged `env > project file > user file > defaults`:

- `~/.factotum/config.toml` — machine-scoped, not committed: `default_project` plus a
  `[projects.<id>]` registry mapping each project to its `db_path`/`store`.
- `./.factotum/config.toml` — project-scoped, committed: `project = "factotum"` and optional
  overrides. Machine-local paths must never appear here.

Never edit a database by hand; go through `ft`. `-c/--config` selects the project file,
`--user-config` the machine file, and `--project`/`-p` an explicit project. Set `--no-hints` (or
`FACTOTUM_NO_HINTS=1`) to silence suggestions, and use `-o json` for machine-readable output.

## Branches, commits, and pull requests

`main` is protected: **never commit to it directly.** Every new feature or non-related bug fix
starts on its own branch and lands via a PR against `main`.

Name branches by intent, kebab-case:

```sh
feat/task-next-all
fix/usage-error-help
docs/agents-pr-workflow
refactor/config-scopes
test/store-conformance
chore/mise-pins
```

Workflow:

1. Branch from up-to-date `main` (`git switch main && git pull && git switch -c feat/...`).
2. Develop test-first and get `mise run ci` green.
3. Commit after **every meaningful unit** (not one big batch): imperative subject, and a short
   body explaining *why* when it is not obvious.
4. Include the matching `ft` graph changes (statuses, notes, new tasks/milestones) in the same
   commit.
5. **Review the branch before opening the PR** (`git log --oneline main..HEAD`).
   - If it carries commits from unrelated scopes, split them: branch each scope from `main`
     (`git rebase --onto`/cherry-pick) and open one **right-scoped PR per scope**. Never let one
     PR mix, say, a feature and its docs.
   - Otherwise, make sure the title and body cover **every** commit on the branch, not just the
     last one.
6. Push the branch and open a right-scoped PR against `main` with `gh pr create`.

Never commit `ft`, `coverage.out`, or `*.db`; `.factotum/config.toml` is intentionally
committable. Do not amend a merged commit, and never force-push a shared branch.

### PR body

Concise and easy to digest: short phrases, no walls of text. Cover, in order:

- **Intention** — what this PR does and why, in one or two sentences.
- **Fit** — where it lands in the existing system (packages, ports, command surface).
- **Risks** — what could break and the blast radius.
- **Reviewer focus** — the few things a human should scrutinize most.
- **Tests** — the scenarios added or exercised, and how to run them.
- **Relaxed tests** — any test weakened or skipped, and why (none is the norm).
- **Breaking change?** — yes/no; if yes, what callers must change.

Reference the `ft` task in the body so the PR and the graph stay linked.


