# ft init — third-party prompt library evaluation

Status: **evaluation and recommendation** (t-y3h7fl22ml). No code change in this document.
t-xbacgizanl landed `ft init` with an in-repo `Prompter` and left the question "use a prompt
library for a more polished interactive session" open for this evaluation. The measurements below
were taken on Go 1.26.4 / darwin / arm64 on 2026-11-16; the commands are in the appendix. Binary
sizes vary by a few tenths of a percent between builds (build id), so read them as approximate.

## 1. Recommendation

**Keep the in-repo `Prompter`.** This is not a stopgap we are failing to replace: for the shape of
interaction `ft init` actually has, a terminal prompt library costs 3–37 extra third-party packages
and up to 3.75 MB of binary to improve the look of five single-line questions — and the most
polished candidate, `huh`, **hangs on a redirected stdin**. In a normal shell, where the process has
a controlling terminal, it opens `/dev/tty`, paints a full-screen UI, and blocks; only a truly
headless process (no controlling terminal at all) gets an error instead
(`could not open TTY`, exit 1). The shape that breaks it is not the headless one, it is the ordinary
one: **stdout on a terminal, stdin redirected** (`ft init < /dev/null`). That invocation used to
silently take every default; the stdin gate (§6.2) now makes it headless, and under `huh` it would
hang.

Revisit when a command needs a widget the port cannot express. The first of those exists today:
`ft run` picks one of N registered adapters through an `Input` with the options in the label and
rejects a typo as an error (`run.go:444`); a **select** is the concrete polish win for a site that
is already here. The others are cross-field validation, multi-select, live filtering, and an
in-place re-prompt loop. When one of them is wanted, adopt `charm.land/huh/v2` behind the existing
`Prompter` port, with the non-TTY gate widened to stdin (see §6) — it is the only actively
maintained candidate with a scriptable non-TTY path, which is what keeps the real terminal
implementation testable.

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
  the `key: value` block on stdout stay parseable. The flow is sequential: at most six questions,
  each answerable by one line and each skippable by a flag.

What the flow is **not**: it is not a form. Nothing is answered side by side, nothing re-prompts in
place, nothing is filtered or multi-selected. The one enumerated choice (`ft run`) is an `Input`
with the options in the label whose rejection is an error listing the valid set — the single site
where a library would visibly change what the user sees.

## 3. Method

For each candidate I built a throwaway module with a minimal program that mirrors the port — one
`Input` with a default and one `Confirm` — and measured:

- **Weight.** Modules in the module graph (`go list -m all`) and third-party packages actually
  compiled (`go list -deps`, non-standard only). Both are reported **net-new against `ft`**: the
  probe's own `probe/lib` package and module are removed, then every package and module `ft` already
  compiles is subtracted (`ft` already has `dustin/go-humanize`, `golang.org/x/sys/unix`, and the
  `x/sys`/`x/sync` modules). The raw numbers are in the appendix.
- **Non-TTY stdin behavior.** The program run with stdin from `/dev/null`, killed after 6 s if still
  alive. This is the case `ft init` must never get wrong. `huh` was tested in a four-cell matrix —
  bare form versus explicit streams, each with and without a controlling terminal — because its
  failure mode depends on both (§4).
- **Scriptability.** Whether a *test* can drive the two-method port off a pipe
  (`printf 'demo\ny\n'`): one prompt is easy, two prompts in sequence is the port's actual shape.

Baseline: a `bufio` line read with no library, 0 net-new packages, 2,510,210-byte binary.
`ft` itself today: 35 modules, 41 third-party compiled packages, 25.1 MB binary (dominated by
sqlite).

## 4. Results

| Candidate (version) | Modules net-new | 3rd-party pkgs net-new | Binary | Non-TTY stdin | Pipe-driveable (2 prompts) | Last release | License | Maintained |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| in-repo `Prompter` | 0 | 0 | +0 | no prompts, by design | yes (fake + real) | — | repo | yes |
| `charm.land/huh/v2` v2.0.3 | 35 | 37 | 6.26 MB (+3.75) | **hangs** with a controlling terminal; `could not open TTY` (exit 1) headless | accessible mode only | 2026-03-10 | MIT | active |
| `github.com/manifoldco/promptui` v0.9.0 | 4 | 4 | 4.84 MB (+2.33) | prompt then EOF error, no default | **no** — first prompt drains the pipe, second always EOF | 2021-10-30 | BSD-3 | pushes, no releases |
| `github.com/AlecAivazis/survey/v2` v2.3.7 | 17 | 17 | 5.30 MB (+2.79) | **`ERR: EOF`** | no | 2023-06-13 | MIT | **archived** (2024-04) |
| `github.com/c-bata/go-prompt` v0.2.6 | 5 | 8 | 3.17 MB (+0.66) | **panics**: `device not configured` | no | v0.2.6 (2021) | MIT | no 2026 commits |
| `github.com/peterh/liner` v1.2.3 | 3 | 3 | 2.79 MB (+0.28) | prompt then EOF error, no default | yes | v1.2.3 | MIT | active |

For scale: `huh` alone would take the third-party closure from 41 packages to 78, **+90%**, adding
35 modules and their checksums to `go.sum`.

### Per candidate

- **`huh` v2.** The polished option: forms, groups, validation, themes. It is disqualified for
  today's flow by its non-TTY behavior, which depends on whether the process has a controlling
  terminal, not on how the form is wired:

  | form | controlling terminal | stdin `/dev/null`, stdout redirected |
  | --- | --- | --- |
  | bare `form.Run()` | yes | **hangs**; escape-sequence UI on stderr |
  | bare `form.Run()` | no | exits 1: `could not open TTY: open /dev/tty: device not configured` |
  | explicit streams | yes | **hangs**; escapes written to the given writer, even a file |
  | explicit streams | no | **hangs**; same |

  So the ordinary shell invocation blocks rather than failing, and the explicit-streams shape (which
  is what a caller reaches for to choose the writer) writes a full-screen UI into a redirected file
  — the `key: value` output contract is only safe because `huh`'s *default* writer is stderr
  (`form.go`), which a caller can override. Its accessible mode (`WithAccessible(true)`) *does* work
  from a pipe, returns defaults on EOF, and is the one scriptable path in the field — which is why
  it, and only it, is named as the later adoption target.
- **`promptui`.** Cheap, but it re-renders with escape sequences off a terminal, and it cannot run
  the port from a pipe: the first prompt's readline consumes the buffered input, so the second
  prompt gets EOF no matter how much is piped. On `/dev/null` it returns `^D` rather than the
  default. It is also effectively frozen: last release 2021, built on `chzyer/readline` from 2018.
- **`survey`.** Excluded on maintenance alone: the repo is archived (last push 2024-04, last
  release 2023-06). It cannot run from a pipe either.
- **`go-prompt`.** Excluded: a full-screen line editor, the wrong shape for five sequential
  questions, and it panics outright when stdin is not a terminal.
- **`liner`.** Honest and small (3 packages, +0.28 MB), and it is the only candidate that runs the
  two-prompt port off a pipe. But on `/dev/null` it returns an EOF error instead of the default, so
  §2's EOF contract becomes the caller's problem, and it is a line editor: history, completion,
  masking. It would replace ~50 lines with ~50 lines and give no confirmation or selection polish —
  no UX win for the cost.

## 5. Analysis against the three criteria

**Dependency weight.** `ft` has five direct dependencies, each doing real work (cobra, toml, yaml,
sqlite, isatty), and 41 third-party packages in total. `huh` would take that closure to 78 (+90%)
and add ~3.75 MB (an upper bound: `ft` already links two of its leaf packages) to serve five
single-line questions. `promptui` and `liner` are light enough not to matter, but see above: they
buy nothing the existing 49-line prompter does not do, and `liner` sheds the EOF-means-default
behavior. The ratio of dependency weight to behavior gained is the whole argument, and it is bad
for every candidate on today's flow.

**Testability.** Any choice keeps the `Prompter` port and the scripted fake, so the fake criterion
is already satisfied and is *not* what separates the candidates — a library only replaces the
terminal implementation. What differs is whether that real implementation can be driven by a test
at all, and the port's shape is two prompts in sequence, not one. It cannot be for `survey` (EOF),
`go-prompt` (panic), or `promptui` (first prompt drains the pipe, second gets EOF). `liner` can, but
only if the test also supplies the EOF-means-default logic that today's prompter already has. Only
`huh`'s accessible mode drives both prompts from a pipe — and even that is not plain text: it emits
SGR colour escapes (`\x1b[1;38;2;90;86;224mProject id\x1b[0m`), so assertions need ANSI stripping
or colour disabled. It works, but it is a second, differently-behaving prompt mode that must be
selected deliberately.

**The repository's posture.** AGENTS.md asks for small interfaces, explicit dependencies, and no
speculative flexibility, and CI is deliberately Go-and-network only. A TUI framework for a
sequential five-question setup is exactly the speculative flexibility that posture rejects: the
in-repo prompter is 49 lines, has 16 table-driven cases pinning blank/EOF/trim/yes/no behavior, and
its prompts land on stderr where the machine-readable output contract needs them.

## 6. If adopted later

The port makes this a one-file swap, and these are the constraints to keep:

1. Keep `Prompter` and the scripted fake; only `terminalPrompter` changes. Tests never construct a
   library form.
2. **Gate on stdin, not just stdout.** The gate is now `d.promptsAllowed(cmd)`: a terminal on
   stdout **and** stdin (`init.go`, `run.go`). It used to be `d.interactive()` =
   `d.IsTerminal(d.Out)` — stdout only — so with stdout on a terminal and stdin redirected (a
   common way scripts and agents invoke `ft init`) the prompter ran all six questions and
   silently accepted every default:

   ```sh
   script -q /tmp/x.log /bin/sh -c 'HOME=$T ft init </dev/null'   # T is a throwaway HOME
   # before: Register this repository as a Factotum project? [Y/n] ... created: true
   # now:    user_config: ... / created: true, then a hint to run ft init -p
   ```

   Keep the rule at stdout-TTY **and** stdin-TTY *before* any form is constructed: a `huh` form
   behind the old gate does not inherit the safe behavior — a bare form opens `/dev/tty` and
   blocks, and only fails with `could not open TTY` when the process has no controlling terminal
   at all. The fully headless case is the easy one; terminal-stdout with piped stdin is the one
   that hangs.
3. Accessible mode is for terminals where raw mode is unavailable, and for tests — never the
   non-TTY path. A non-TTY stdin keeps today's behavior: no prompts at all.
4. Keep prompts on `d.Err`. `huh` already writes there by default (`form.go` sets
   `tea.WithOutput(os.Stderr)`), so do not override it: passing `WithOutput(os.Stdout)` moves the
   UI into stdout and ignores that stdout is redirected (§4).
5. Preserve blank → default and EOF → default. `huh`'s accessible mode does this already; its
   default mode hangs or errors on EOF, so the mapping is not free.
6. Strip colour in tests, or disable it: accessible mode still emits SGR escapes when piped.
7. Re-measure weight at adoption time — the numbers above are dated, and `huh` v2 is young.

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
go list -m all | tail -n +2 | awk '{print $1}' | grep -vx probe | sort -u    # modules
go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./lib | grep -v '^probe/lib$' | sort -u
go build -o bin/probe ./lib && ls -l bin/probe                               # binary size
./bin/probe </dev/null & pid=$!; sleep 6; kill -0 $pid && echo "still running"   # non-TTY
printf 'demo\ny\n' | ./bin/probe                                             # two prompts from a pipe
```

Net-new is those sets minus what `ft` already compiles
(`go list -deps ... ./... | grep -v '^github.com/khoinguyen/factotum'` → 41 packages, 35 modules).

For `huh`, the non-TTY matrix needs both axes — a controlling terminal or not, and bare form versus
explicit streams — so each cell was run once through a pty (`script -q /tmp/x.log /bin/sh -c
'./bin/probe </dev/null >/tmp/out 2>/tmp/err'`) and once in a session with no controlling terminal
(`setsid`, where available), checking that the probe process itself was still alive after 6 s.

| Candidate | Modules (raw) | 3rd-party pkgs (raw) | overlap with `ft` |
| --- | --- | --- | --- |
| `huh` v2 | 38 | 39 | 3 modules, 2 packages |
| `promptui` | 5 | 4 | 1 module, 0 packages |
| `survey` | 24 | 19 | 7 modules, 2 packages |
| `go-prompt` | 8 | 9 | 3 modules, 1 package |
| `liner` | 4 | 3 | 1 module, 0 packages |

Repository baseline: `go list -m all | wc -l` → 35; third-party compiled packages → 41;
`mise run build` → 25.1 MB.
