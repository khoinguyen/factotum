# Project-agnosticism audit

`ft` is a general factory: it builds *other* software, so the artifacts it ships —
or that a project commits for it — must not assume the Factotum repository or the
`factotum` project id. This audits the embedded usage skills, the config
templates, and the prompts, and records how each finding was resolved.

Scope: embedded usage skills (`internal/skills/content/*.md`), config templates
and defaults (`internal/config`, `pkg/store`), and prompts
(`docs/grooming/prompt.md`, the `ft prompt` planning instruction).

## Findings

| # | Artifact | Factotum-specific assumption | Disposition |
| - | -------- | ---------------------------- | ----------- |
| 1 | `docs/grooming/prompt.md` | "the Factotum project" names one project as the session's target | **Fixed** — now "the current project"; `ft groom`'s kickoff names the actual project id |
| 2 | `internal/skills/content/software-factory.md` | "here under `.agents/skills/<name>/`" assumes the reader is in this repo | **Fixed** — now "for example, under `.agents/skills/<name>/`" |
| 3 | `pkg/agent/command/command.go` (`ft prompt`) | "the Factotum task graph" names the maintainer's graph | **Fixed** — now "the project's task graph"; the instruction already names `req.Project` |
| 4 | `internal/skills/content/ft.md` (`ft feedback`) | the feedback sink defaults to the `factotum` project | **Parameterized** — `--feedback-store` (default `factotum`, the tool's own maintainer project); a deployment points it at its own project through the machine `[projects.<id>]` registry |
| 5 | `internal/config/write.go` templates | "Factotum config" | **Not an assumption** — names the tool, not a project |
| 6 | `pkg/store/sqlite`, `pkg/store/jsonfile`, `pkg/store/jsondir` defaults | `.factotum/factotum.db` / `.factotum/factotum.json` / `.factotum/jsondir` | **Not an assumption** — a tool-named fallback; the live path is each project's `[projects.<id>].db_path` |
| 7 | `.agents/skills/*` role skills | `mise run ci`, `/tmp/ft-<t>` worktrees, "Factotum" dashboard naming | **Out of scope** — harness role skills, not served by `ft`; each project commits its own copies with its own CI command. Recommended follow-up: make the build/CI command a project setting instead of a hardcoded `mise run ci` |

## Guard

Three tests pin the invariant so it cannot regress:

- `internal/skills/projectagnostic_test.go` — no embedded skill hardcodes the
  `factotum` project or assumes the reader is in this repo.
- `internal/groom/groom_test.go` (`TestSessionPromptIsProjectAgnostic`) — the
  durable session prompt names no concrete project.
- `pkg/agent/command/command_test.go` (`TestInstructionIsProjectAgnostic`) — the
  `ft prompt` instruction names the request's project, never Factotum.

A second project (for example `kubecharge`) can commit the groom prompt and use
the embedded skills unchanged.
