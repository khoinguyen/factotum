# ft init — third-party prompt library evaluation

Status: **evaluation and recommendation** (t-y3h7fl22ml). No code change in this document.
t-xbacgizanl landed `ft init` with an in-repo `Prompter` and left the question "use a prompt
library for a more polished interactive session" open for this evaluation. The measurements below
were taken on Go 1.26.4 / darwin / arm64 on 2026-11-16; the commands are in the appendix.

## 1. Recommendation

**Keep the in-repo `Prompter`.** It is not a stopgap that we are failing to replace: for the shape
of interaction `ft init` actually has, a terminal prompt library costs 6–39 extra modules and
2.3–3.7 MB of binary to improve the look of five single-line questions, and the most polished
candidate (`huh`) **hangs** when stdin is not a terminal — the exact situation
`ft init` is required to handle cleanly.

Revisit when a command needs a real **form**: cross-field validation, multi-select, live filtering,
or an in-place re-prompt loop instead of "unknown value, here are the valid ones" as an error. At
that point adopt `charm.land/huh/v2` **behind the existing `Prompter` port**, with accessible mode
forced whenever stdout/stdin is not a terminal. That is the only actively maintained candidate whose
non-TTY path is scriptable, which is what keeps the real terminal implementation testable.

## 2. What `ft init` needs today

The port is two methods, and the tests pin its exact behavior
(`internal/cli/init_test.go`: `TestTerminalPrompterInput`, `TestTerminalPrompterConfirm`):

```go
type Prompter interface {
	Input(label, def string) (string, error)
	Confirm(label string, def bool) (bool, error)
}
```

Ten prompt sites across two commands:

| Command | Site | Prompt |
| --- | --- | --- |
| `ft init` | `init.go:188` | Confirm: create the missing machine config? (default yes) |
| `ft init` | `init.go:210` | Confirm: register this repository? (default yes) |
| `ft init -p` | `init.go:264` | Input: project id, prefilled with the derived id |
| `ft init -p` | `init.go:269` | Input: project name |
| `ft init -p` | `init.go:287` | Input: database path |
| `ft init -p` | `init.go:301` | Input: repository name |
| `ft init -p` | `init.go:314` | Input: tech stack (greenfield only) |
| `ft run` | `run.go:430` | Confirm: allow the unsandboxed local backend? (default no) |
| `ft run` | `run.go:444` | Input: pick one of N registered adapters (validated by the caller) |
| `ft run` | `run.go:465` | Input: persist to project or user scope (validated by the caller) |

Three requirements follow from the code and its tests, and they are what the evaluation has to
weigh against a library:

- **Defaults are load-bearing.** Blank accepts the default; EOF (stdin closed) also accepts the
  default rather than erroring, because a half-answered session must still produce a usable config.
  The derived project id is prefilled so the user edits rather than types.
- **Non-interactive is first-class, not a degraded path.** With no terminal `ft init` prompts
  nothing at all: it creates the machine config and hints at `ft init -p`, which registers from
  derived defaults. This is tested (`TestInitBareInRepoNonInteractiveDoesNotRegister`,
  `TestInitGreenfieldNonInteractiveDefault`).
- **Stderr prompts, stdout machine output.** The prompter writes to `d.Err`, so `-o json|yaml` and
  the `key: value` block on stdout stay parseable. The flow is sequential: at most five questions,
  each answerable by one line and each skippable by a flag.

What the flow is **not**: it is not a form. Nothing is answered side by side, nothing re-prompts in
place, nothing is filtered or multi-selected. The one enumerated choice (`ft run`) is an `Input`
with the options in the label whose rejection is an error listing the valid set.

## 3. Method

For each candidate I built a throwaway module with a minimal program that mirrors the port — one
`Input` with a default and one `Confirm` — and measured:

- **Weight:** modules in the module graph (`go list -m all`), third-party packages actually
  compiled (`go list -deps`, non-standard only), and binary size against a `bufio`-only baseline
  program built in the same environment.
- **Non-TTY behavior:** the program run with stdin from `/dev/null`, killed after 4 s if still
  alive. This is the case `ft init` must never get wrong.
- **Scriptability:** the same program fed a pipe (`printf 'demo\ny\n'`) to see whether a *test*
  can drive the real implementation without a pty.

The baseline (`bufio` line read, no library) is 1 module, 0 third-party packages, 2,510,210 bytes.

## 4. Results

| Candidate (version) | Modules | 3rd-party pkgs | Binary | Non-TTY stdin | Driveable from a pipe | Last release | License | Maintained |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| in-repo `Prompter` | 0 | 0 | 0 | no prompts, by design | yes (fake + real) | — | repo | yes |
| `charm.land/huh/v2` v2.0.3 | 39 | 40 | 6,255,650 (+3.75 MB) | **blocks forever**, full-screen UI on stdout | only in accessible mode | 2026-03-10 | MIT | active |
| `github.com/manifoldco/promptui` v0.9.0 | 6 | 5 | 4,839,042 (+2.33 MB) | works, but emits ANSI redraw noise | yes | 2021-10-30 | BSD-3 | pushes, no releases |
| `github.com/AlecAivazis/survey/v2` v2.3.7 | 25 | 20 | 5,300,450 (+2.79 MB) | **`ERR: EOF`** after escape sequences | no | 2023-06-13 | MIT | **archived** (2024-04) |
| `github.com/c-bata/go-prompt` v0.2.6 | 9 | 10 | 3,166,354 (+0.66 MB) | **panics**: `device not configured` | no | v0.2.6 (2021) | MIT | no 2026 commits |
| `github.com/peterh/liner` v1.2.3 | 5 | 4 | 2,788,866 (+0.28 MB) | works, plain output | yes | v1.2.3 | MIT | active |

For scale: the repository today has 35 modules in its graph and 96 third-party compiled packages,
with a 25.1 MB `ft` binary (dominated by sqlite). `huh` alone would add 40 packages — a 42% increase
in the third-party closure — plus 39 checksums in `go.sum` and a dependency on the `charm.land`
vanity import path.

### Per candidate

- **`huh` v2.** The polished option: forms, groups, validation, themes. Two things disqualify it
  for today's flow. First, in default mode it starts a full-screen bubbletea program even when
  stdin is `/dev/null` — it painted escape-sequence UI (still writing when killed at 4 s) and never
  returned; in an agent or CI run of `ft init` that is a hang, not an error. Second, its module path moved to the `charm.land`
  vanity domain, and it drags in the pty/terminal stack (`creack/pty`, `x/term`, `x/termios`,
  `x/conpty`). Its accessible mode (`WithAccessible(true)`) *does* work from a pipe and gives a
  scriptable path — that is why it is the one to pick when a form is genuinely needed.
- **`promptui`.** Cheap, and degrades to line reads off a pipe, but it re-renders with escape
  sequences when not a terminal, and it is effectively frozen: last release 2021, built on
  `chzyer/readline` from 2018. Its surface (prompt, select, confirm) is what we would want; its
  maintenance is not.
- **`survey`.** Excluded on maintenance alone: the repo is archived (last push 2024-04, last
  release 2023-06). It also cannot run from a pipe.
- **`go-prompt`.** Excluded: it is a full-screen line editor, the wrong shape for five sequential
  questions, and it panics outright when stdin is not a terminal.
- **`liner`.** Honest and small (4 packages, +0.28 MB), and it degrades cleanly. But it is a line
  editor: history, completion, masking. It would replace ~50 lines with ~50 lines and give no
  confirmation or selection polish — no UX win for the cost.

## 5. Analysis against the three criteria

**Dependency weight.** `ft` has five direct dependencies, each doing real work (cobra, toml, yaml,
sqlite, isatty). `huh` would multiply the third-party closure by 42% and add ~3.7 MB (an upper
bound: `ft` already links a few of its leaf deps) to serve five single-line questions.
`promptui` and `liner` are light enough not to matter, but see above: they buy nothing that the
existing 49-line prompter does not already do. The ratio of dependency weight to behavior gained is
the entire argument, and it is bad for every candidate on today's flow.

**Testability.** Any choice keeps the `Prompter` port and the scripted fake, so the fake criterion
is already satisfied and is *not* what separates the candidates — a library only replaces the
terminal implementation. What differs is whether that real implementation can be driven by a test
at all. It cannot be for `survey` (EOF) or `go-prompt` (panic). It can for `liner` and `promptui`
via a pipe, but with `promptui` the assertions would have to match its escape sequences. Only `huh`'s
accessible mode produces clean plain-text prompts from a pipe (`Project id ` then
`Register this repository? [Y/n] `), which is scriptable in a normal `go test` — at the cost of a
second, differently-behaving prompt mode that must be selected deliberately.

**The repository's posture.** AGENTS.md asks for small interfaces, explicit dependencies, and no
speculative flexibility, and CI is deliberately Go-and-network only. A TUI framework for a
sequential five-question setup is exactly the speculative flexibility that posture rejects: the
in-repo prompter is 49 lines, has 16 table-driven cases pinning blank/EOF/trim/yes/no behavior, and
its prompts land on stderr where the machine-readable output contract needs them.

## 6. If adopted later

The port makes this a one-file swap, and these are the constraints to keep:

1. Keep `Prompter` and the scripted fake; only `terminalPrompter` changes. Tests never construct a
   library form.
2. Gate on `d.interactive()` **before** constructing a form. `huh` does not fail on a non-TTY
   stdin — it blocks — so a library whose prompt cannot see a terminal must never be reached.
3. Force accessible mode when the terminal is present but stdin/stdout is not a TTY, so piped and
   redirected sessions keep working and stay testable.
4. Keep prompts on `d.Err`. A library that renders to stdout would corrupt `-o json|yaml`.
5. Preserve blank → default and EOF → default. `huh` returns an error on an aborted form; the
   init flow needs the EOF case to continue with derived defaults.
6. Re-measure weight at adoption time — the numbers above are dated, and `huh` v2 is young.

## 7. Limits of this evaluation

The numbers are real and were run for this document, but they are not a verdict on *feel*: nobody
drove a library on a real terminal and compared it to the current session, and the "polished UX"
upside is a human judgment this document deliberately does not overrule. I did not benchmark
startup latency, Windows behavior, or a second architecture. Versions and maintenance facts are as
of 2026-11-16.

## Appendix: measurements

Run in a scratch directory outside the repository, one module per candidate:

```sh
go mod init probe
# lib/main.go: one Input-with-default and one Confirm, mirroring the Prompter port
go mod tidy
go list -m all | wc -l                                     # module graph
go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./lib | grep -c .   # compiled 3rd-party packages
go build -o bin/probe ./lib && ls -l bin/probe             # binary size
./bin/probe </dev/null & pid=$!; sleep 4; kill -0 $pid && echo "still running"   # non-TTY behavior
printf 'demo\ny\n' | ./bin/probe                          # pipe-driveable?
```

Repository baseline: `go list -m all | wc -l` → 35; non-standard compiled packages → 96;
`mise run build` → 25,140,322 bytes.
