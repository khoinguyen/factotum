# ft run: the attached (TTY) path and the process tree it cannot own

Status: **design note, decision recorded** (t-oy6udjlyvj). No behavioural change in this change. The
attached path keeps leaking its worker until option C lands as its own task; A and B are both
rejected below.

PR #432 ended a forked worker when a *detached* command exits on its own, and deliberately left the
attached (default interactive) path alone, recording the follow-up as: the child must own a session
and become the terminal's foreground process group, so the group is ft's to reap. This note records
what that design actually costs, measured on a real pty, and the two designs that survive the
constraint it breaks.

## 1. The leak, as it stands

An attached run's harness may fork workers (an MCP server, a tool subprocess) that outlive it. The
worker is reparented away when the harness exits, so it is not in the harness's tree any more, and
the backend untracks the harness on its exit, so `Stop`/`Delete` have nothing left to signal. It is
still in ft's process group, and therefore still attached to the terminal.

Measured on `28f2974`, `ft run` under a pty, a fake harness that forks a sleeper and exits:

```
$ script -q /dev/null bash -c 'ft run <task> ... ; echo "ft exit=$?"; kill -0 $(cat worker.pid) && echo WORKER ALIVE'
run: finished
exit_code: 0
ft exit=0
WORKER ALIVE pid=70938
```

An interrupt is a different shape, and a race. `Stop`'s tree walk reaps the worker when its
enumeration still sees the harness; when the harness's own `^C` has already ended it - a cooked
harness in ft's group takes the same signal - there is nothing left to walk, and a worker that
ignores `SIGINT` (which a non-interactive shell's `&` job does) then survives. Both outcomes were
measured on the same shape: worker gone (`ft: run interrupted`, `ft exit=1`) when the walk still sees
the harness, and worker alive (`ft: run interrupted`, `ft exit=1`, already reparented to init and
still in ft's group) when it does not.

## 2. Why the attached path is a different problem

An attached command shares ft's process group because that group is the terminal's foreground group:
it is what lets the harness read the keyboard, take `SIGWINCH`, and receive `^C`/`^Z` without any
code in ft. A group kill would signal ft and its launcher along with the harness, which is why the
attached path walks the tree instead.

The recorded follow-up is the shell's own answer: give the child its own process group, make it the
terminal's foreground group with `tcsetpgrp`, and end that group on exit the way the detached path
ends its own.

## 3. What that costs (measured)

A terminal has exactly one foreground process group, and terminal-generated signals go to that group
and to nothing else. Handing it to the child takes them away from ft:

- **`^C` no longer reaches ft, so ft's own interrupt path never runs.** It is ft, not the terminal,
  that owns the message, the teardown, and the exit status: `withRunInterrupts` catches INT and TERM,
  cancels the run with `errRunInterrupted`, and the cancellation unwinds through the ordinary error
  path - retiring the minted session, deleting the sandbox - before printing `ft: run interrupted`
  with exit 1, with the escape the code documents, that a second Ctrl-C ends the process on the spot. The
  terminal's part is only the delivery, and with the child's group foreground it delivers to the
  child and nothing else. The run then ends the way the harness ends: one killed by the signal
  surfaces as death-by-signal (`ExitCode -1` in the port), and one that traps it and exits - as
  Node/Bun CLIs commonly do with `130`, opencode included - surfaces as that code. Either way ft
  reports a failed run with the harness's status: no graceful teardown, no `ft: run interrupted` and
  exit 1, and no second-Ctrl-C hatch either, because that escape cannot reach ft. Measured with the
  child's group foreground: the parent received no `SIGINT` in any shape tried - a bare `exec sleep`,
  `sh -c`, `bash -c`, and a `bash` wrapper.
- **A stop strands the run.** `^Z` (or a TUI that suspends itself on `^Z`, as several do) now stops
  only the child's group. `Wait` returns on *exit*, never on a stop, so nothing tells ft; `^C` sent
  to a stopped group stays pending; and the shell that launched ft sees a job that never stopped, so
  `fg` has nothing to resume and does not own the terminal to hand back. Measured: the run hangs
  with no output until something outside kills it.
- **ft cannot re-join to keep the signals.** Moving ft into the child's group would keep `^C`
  delivered to both, as today. That is refused when ft is a session leader - POSIX forbids changing
  a session leader's process group - which is ft's shape under a pty supervisor. Under a plain
  terminal it is permitted, and the reason not to is a different one: it moves ft out of the process
  group the launching shell tracks as the job, so the shell's `fg` would foreground and resume a
  group ft is no longer in.

Closing this needs a job-control loop over the child (a `WUNTRACED` wait, suspend/resume forwarding,
and re-foregrounding on `fg`), which `os/exec` does not expose: `Cmd.Wait` reports exits only. The
"own a session and take the terminal" design therefore needs more than the group handoff it looks
like, and it trades a leaked worker for a terminal that can hang.

## 4. Options

### A. Child group + `tcsetpgrp` (the recorded design) - rejected

Fixes the leak; ~80 lines. Breaks the `^C` message and exit status, and turns `^Z` into a hang, for
the reasons measured above.

### B. Reap by group membership (interim)

Keep the child in ft's group, so every terminal behaviour, signal, and job-control interaction is
exactly as it is today and there is no terminal state to restore. Snapshot the pids in ft's group
at `Exec`, and on the command's own exit kill the members that joined since - excluding ft itself
and ft's own live children. A worker forked by the command is still in the group at exit, so it is
found; nothing else changes. ~60 lines plus a process-group accessor beside `procgroup.children`.

The cost is that it kills by membership in a group ft shares with its launcher rather than in a
group ft owns: a process that joins ft's group during the run and is not ft's child is assumed to be
the command's. For a shell foreground job that assumption holds (a pipeline's siblings exist before
the snapshot; `&` jobs get their own group), and any ft-spawned helper started during a run in the
same group would be a real casualty - a footgun to document, not a law of the design.

### C. Give the attached child its own pty and session (the complete fix)

Keep the real terminal for ft and relay through a pty the child owns, which is what `ssh -t` and
`docker -it` do. The child becomes a session leader with that pty as its controlling terminal and a
process group of its own, so that group is ft's to reap (`kill(-pgid)`), including workers that
reparent away. What it does not own is the tree: a worker that calls `setsid`/`setpgid` itself
leaves the group and escapes - exactly as it escapes B - so C buys an owned group, not every
descendant. ft keeps the terminal, so `^C` still reaches it and the interrupt path, the message, the
exit status, and `^Z`/`fg` are unchanged.

The cost is real: pty allocation and teardown, putting ft's terminal in raw mode and restoring it on
every path (which moves the "terminal left broken" risk from the harness to ft), an input relay that
must not survive the run and steal keystrokes, `SIGWINCH` forwarding, and suspend handling; roughly
250-350 lines with the pty tests, and it wants a review with the terminal in front of it.

## 5. Decision

Do not ship A: it trades a leaked worker for a terminal that can hang.

Do not ship B either: killing by membership in a group ft shares with its caller can reach a process
ft never owned, and a misfired kill loses work where a leaked sleeper only wastes memory.

C is the fix, and it is filed as `t-xdyh6mipzw` with its own review - it wants a human in front of a
terminal to sign off on the raw-mode and restore behaviour. Until it lands, the attached path keeps
leaking the worker that started this note, which is a waste rather than a hazard.

## 6. How the measurements were taken

A throwaway store (`--store jsonfile --store-opt path=...`), a throwaway project and task, a fake
`opencode` on `PATH` that forks `sleep 300`, records its pid, and exits (or sleeps first, when the
interrupt shape is the one being measured), and `script -q /dev/null bash -c '...'` for the pty, with
`bash` - not ft - as the session leader so the leak is the real one rather than a pty hangup. `^C` is
`printf '\003'` into `script`'s stdin, and the worker's fate is checked with `kill -0` plus `ps`
before the shell exits. The group handoff itself was probed with a pty pair (`/dev/ptmx` +
`setsid`/`TIOCSCTTY` on the slave) so the child's group could be made foreground and the parent's
signal delivery observed directly; there the parent received no `SIGINT` under an `exec sleep`,
`sh -c`, `bash -c`, or a `bash` wrapper. Signal disposition explains the two interrupt shapes above:
a job a non-interactive shell forks with `&` ignores `SIGINT`, so whether a worker survives an
interrupt depends on whether the harness is still alive for `Stop` to walk.
