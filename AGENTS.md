# AGENTS.md

Guidance for AI coding agents working in this repository. Read this before making changes.

## What this is

Factotum is a toolset for managing agentic coding work. It manages **projects** (which may span
multiple repositories) and the **task graph** of mixed human/agent work, along with the specs,
docs, and memory around them. The first tool is the `ft` CLI. The Go module, config directory
(`.factotum/`), and environment prefix (`FACTOTUM_`) keep the project name.

The project is developed test-first and, eventually, managed by `ft` itself.

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

## Dogfooding

Once the CLI is usable, this repository is managed by `ft`: the project, the task graph,
milestones, decisions, and memory all live in the tool. When changing behavior, also update the
project's own graph if the change alters its tasks.

## Commits

Do not commit, amend, push, or open pull requests unless asked explicitly. When asked, inspect
`git status` and `git diff` first, stage only intended files, and write a concise message in the
imperative mood describing the change.
