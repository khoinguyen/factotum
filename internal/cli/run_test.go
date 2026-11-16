package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
	harnesspkg "github.com/khoinguyen/factotum/pkg/harness"
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
)

// ttyRefusingBackend refuses only interactive commands, like an isolating
// backend that cannot attach a terminal, while running headless ones normally.
type ttyRefusingBackend struct{ *isofake.Backend }

func (b ttyRefusingBackend) Exec(ctx context.Context, h isolation.Handle, cmd isolation.Command) (isolation.Execution, error) {
	if cmd.TTY {
		return nil, isolation.ErrNoTerminal
	}
	return b.Backend.Exec(ctx, h, cmd)
}

// runContext creates a project whose one repo is an existing local checkout, so
// `ft run`'s workspace resolution needs no git, and returns its task id.
func runContext(t *testing.T, r *runner) (projectID, taskID string) {
	t.Helper()
	projectID = firstField(t, r.run("project", "create", "Acme"))
	r.run("project", "repo", "create", projectID, "web", "--path", t.TempDir())
	taskID = firstField(t, r.run("task", "create",
		"--project", projectID, "--repo", "web", "--title", "Add widget", "--body", "Do the work."))
	return projectID, taskID
}

func TestRunCommandDrivesTaskToReview(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out := r.run("run", taskID, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	for _, want := range []string{"task_id: " + taskID, "run: finished", "status: ready_for_review", "exit_code: 0", "complete: true"} {
		if !strings.Contains(out, want) {
			t.Fatalf("run output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "done") {
		t.Fatalf("run output missing the agent output:\n%s", out)
	}
	if shown := r.run("task", "get", taskID); !strings.Contains(shown, "ready_for_review") {
		t.Fatalf("task not advanced to review:\n%s", shown)
	}
	if got := backend.Prepared(); len(got) != 1 {
		t.Fatalf("Prepare called %d times, want 1", len(got))
	}
	commands := backend.Commands()
	if len(commands) != 1 {
		t.Fatalf("Exec called %d times, want 1", len(commands))
	}
	prompt := commands[0].Argv[len(commands[0].Argv)-1]
	if !strings.Contains(prompt, "Add widget") || !strings.Contains(prompt, "Do the work.") {
		t.Fatalf("prompt did not carry the task:\n%s", prompt)
	}
}

// TestRunInjectsHubEnvFromConfig proves `ft run` reads the configured messaging
// hub (FACTOTUM_MSG_URL/FACTOTUM_SERVE_TOKEN, or the [serve] table) and hands it
// to the launched harness, so a receiver started by a remote run speaks the
// HTTP transport. No hub configured means no hub env, the local-ft fallback.
func TestRunInjectsHubEnvFromConfig(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		wantURL   string
		wantToken string
	}{
		{
			name:      "hub from environment",
			env:       map[string]string{"FACTOTUM_MSG_URL": "http://hub:8484", "FACTOTUM_SERVE_TOKEN": "tok"},
			wantURL:   "http://hub:8484",
			wantToken: "tok",
		},
		{
			name: "no hub configured",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(t)
			r.getenv = func(key string) string { return tc.env[key] }
			_, taskID := runContext(t, r)
			r.run("actor", "create", "--kind", "agent", "claude")

			backend := isofake.New("sandbox")
			backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
			r.runBackend = backend
			r.runHarness = harnessfake.New("opencode")

			r.run("run", taskID, "--actor", "claude", "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())

			prepared := backend.Prepared()
			if len(prepared) != 1 {
				t.Fatalf("Prepare called %d times, want 1", len(prepared))
			}
			env := prepared[0].Env
			if env["FACTOTUM_MSG_URL"] != tc.wantURL {
				t.Errorf("harness env FACTOTUM_MSG_URL = %q, want %q", env["FACTOTUM_MSG_URL"], tc.wantURL)
			}
			if env["FACTOTUM_SERVE_TOKEN"] != tc.wantToken {
				t.Errorf("harness env FACTOTUM_SERVE_TOKEN = %q, want %q", env["FACTOTUM_SERVE_TOKEN"], tc.wantToken)
			}
		})
	}
}

// TestRunWarnsWhenHubTokenMissing proves `ft run` surfaces a half-configured hub
// (a URL with no token) instead of silently launching a receiver that fails
// every message. The transport is token-gated, so the pair is required; the
// incomplete hub is dropped from the harness env, and the warning says so.
func TestRunWarnsWhenHubTokenMissing(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(key string) string {
		if key == "FACTOTUM_MSG_URL" {
			return "http://hub:8484"
		}
		return ""
	}
	_, taskID := runContext(t, r)
	r.run("actor", "create", "--kind", "agent", "claude")

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	_, stderr := r.runSplit("run", taskID, "--actor", "claude", "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !strings.Contains(stderr, "serve.token") || !strings.Contains(stderr, "serve.url") {
		t.Fatalf("run stderr missing the hub-token warning:\n%s", stderr)
	}
	prepared := backend.Prepared()
	if len(prepared) != 1 {
		t.Fatalf("Prepare called %d times, want 1", len(prepared))
	}
	if _, ok := prepared[0].Env["FACTOTUM_MSG_URL"]; ok {
		t.Errorf("harness env carried a hub URL with no token: %v", prepared[0].Env)
	}
}

// TestRunWithoutActorOmitsHubWarning proves the half-configured-hub warning is
// about a launched receiver: a run with no actor installs none, so warning about
// a hub no receiver can speak would be noise and is suppressed.
func TestRunWithoutActorOmitsHubWarning(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(key string) string {
		if key == "FACTOTUM_MSG_URL" {
			return "http://hub:8484"
		}
		return ""
	}
	_, taskID := runContext(t, r)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	_, stderr := r.runSplit("run", taskID, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if strings.Contains(stderr, "serve.token") {
		t.Fatalf("run with no actor warned about the incomplete hub:\n%s", stderr)
	}
}

// TestRunCommandInteractiveOnTerminal pins `ft run`'s mode selection: on a
// terminal a task run attaches the agent (the harness command requests a TTY);
// --unattended or a non-terminal stays headless.
func TestRunCommandInteractiveOnTerminal(t *testing.T) {
	tests := []struct {
		name       string
		terminal   bool
		unattended bool
		wantTTY    bool
	}{
		{"terminal attaches", true, false, true},
		{"terminal with unattended stays headless", true, true, false},
		{"non-terminal stays headless", false, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(t)
			r.isTerminal = func(io.Writer) bool { return tc.terminal }
			r.stdinTerminal = func(io.Reader) bool { return tc.terminal }
			_, taskID := runContext(t, r)

			backend := isofake.New("sandbox")
			backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
			r.runBackend = backend
			r.runHarness = harnessfake.New("opencode")

			args := []string{"run", taskID, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir()}
			if tc.unattended {
				args = append(args, "--unattended")
			}
			out := r.run(args...)
			if !strings.Contains(out, "run: finished") {
				t.Fatalf("run output:\n%s", out)
			}
			cmds := backend.Commands()
			if len(cmds) != 1 {
				t.Fatalf("Exec called %d times, want 1", len(cmds))
			}
			if cmds[0].TTY != tc.wantTTY {
				t.Fatalf("TTY = %v, want %v", cmds[0].TTY, tc.wantTTY)
			}
		})
	}
}

// TestRunInteractiveBackendWithoutTerminalGuidesToUnattended proves the mode is
// recoverable: when an interactive run's backend cannot attach a terminal, the
// command fails with guidance to rerun headless, and --unattended then runs it.
func TestRunInteractiveBackendWithoutTerminalGuidesToUnattended(t *testing.T) {
	r := newRunner(t)
	r.isTerminal = func(io.Writer) bool { return true }
	r.stdinTerminal = func(io.Reader) bool { return true }
	_, taskID := runContext(t, r)

	base := isofake.New("sandbox")
	base.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = ttyRefusingBackend{Backend: base}
	r.runHarness = harnessfake.New("opencode")

	err := r.runErr("run", taskID, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "cannot attach an interactive terminal") {
		t.Fatalf("interactive run error = %v, want usage error naming the terminal limit", err)
	}

	headless := newRunner(t)
	_, taskID = runContext(t, headless)
	base = isofake.New("sandbox")
	base.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	headless.runBackend = ttyRefusingBackend{Backend: base}
	headless.runHarness = harnessfake.New("opencode")
	if out := headless.run("run", taskID, "--unattended", "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir()); !strings.Contains(out, "run: finished") {
		t.Fatalf("headless rerun did not finish:\n%s", out)
	}
}

// TestRunCommandResolvesRelativeRepoPathFromProjectConfigRoot reproduces the
// factotum dogfood failure: the project registers its repo with a relative Path
// (".") and no URL, which `ft run` must resolve against the project root (the
// directory holding .factotum/config.toml) instead of rejecting it.
func TestRunCommandResolvesRelativeRepoPathFromProjectConfigRoot(t *testing.T) {
	r := newRunner(t)
	root := t.TempDir()
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("project", "repo", "create", projectID, "factotum", "--path", ".")
	cfgPath := filepath.Join(root, ".factotum", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("project = \""+projectID+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	taskID := firstField(t, r.run("--config", cfgPath, "task", "create",
		"--project", projectID, "--repo", "factotum", "--title", "T", "--body", "B"))

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out := r.run("--config", cfgPath, "run", taskID, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !strings.Contains(out, "run: finished") {
		t.Fatalf("relative-path run did not finish:\n%s", out)
	}
	prepared := backend.Prepared()
	if len(prepared) != 1 {
		t.Fatalf("Prepare called %d times, want 1", len(prepared))
	}
	if prepared[0].Workdir != root {
		t.Fatalf("workdir = %q, want the project root %q", prepared[0].Workdir, root)
	}
}

func TestRunCommandRequiresExplicitSelection(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing sandbox", []string{"--harness", "fake"}, "--sandbox is required"},
		{"missing harness", []string{"--sandbox", "fake"}, "--harness is required"},
		{"unknown sandbox", []string{"--sandbox", "nope", "--harness", "fake"}, `unknown isolation backend "nope"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(t)
			r.runBackend = isofake.New("sandbox")
			r.runHarness = harnessfake.New("opencode")
			args := append(append([]string{"run", "t-anything"}, tc.args...), "--workspace", t.TempDir())

			err := r.runErr(args...)
			if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s error = %v, want usage naming %q", tc.name, err, tc.want)
			}
		})
	}
}

func TestRunCommandLocalBackendRequiresOptIn(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)
	r.runHarness = harnessfake.New("opencode")

	err := r.runErr("run", taskID, "--sandbox", "local", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("local without opt-in error = %v, want usage", err)
	}
	if !strings.Contains(err.Error(), `backend "local"`) {
		t.Fatalf("guardrail message lost the backend name: %v", err)
	}
	if shown := r.run("task", "get", taskID); !strings.HasPrefix(shown, "(todo) ") {
		t.Fatalf("local run without opt-in changed task state:\n%s", shown)
	}
}

func TestRunCommandLoopLocalBackendRequiresOptIn(t *testing.T) {
	r := newRunner(t)
	_, _, goal := loopContext(t, r)
	r.runHarness = harnessfake.New("opencode")

	err := r.runErr("run", "--goal", goal, "--sandbox", "local", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("local loop without opt-in error = %v, want usage", err)
	}
	if !strings.Contains(err.Error(), `backend "local"`) {
		t.Fatalf("loop guardrail message lost the backend name: %v", err)
	}
}

// loopContext creates a project whose one repo is an existing checkout and a
// two-task chain (goal depends on prereq), so `ft run --goal` can be exercised.
// Both tasks are assigned to an agent and groomed, because the loop only runs
// agent-ready work.
func loopContext(t *testing.T, r *runner) (projectID, prereq, goal string) {
	t.Helper()
	projectID = firstField(t, r.run("project", "create", "Acme"))
	r.run("project", "repo", "create", projectID, "web", "--path", t.TempDir())
	r.run("actor", "create", "--kind", "agent", "claude")
	prereq = firstField(t, r.run("task", "create",
		"--project", projectID, "--repo", "web", "--title", "Prereq", "--body", "Do the first part.",
		"--groomed", "--acceptance", "the first part is done"))
	r.run("task", "assign", prereq, "--actor", "claude")
	goal = firstField(t, r.run("task", "create",
		"--project", projectID, "--repo", "web", "--title", "Goal", "--body", "Do the last part.",
		"--groomed", "--acceptance", "the last part is done"))
	r.run("task", "assign", goal, "--actor", "claude")
	r.run("task", "dep", "create", goal, prereq)
	return projectID, prereq, goal
}

func TestRunCommandLoopDrivesToGoal(t *testing.T) {
	r := newRunner(t)
	_, prereq, goal := loopContext(t, r)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out := r.run("run", "--goal", goal, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	for _, want := range []string{"goal: " + goal, "stop: goal_reached", "iterations: 2", prereq} {
		if !strings.Contains(out, want) {
			t.Fatalf("run loop output missing %q:\n%s", want, out)
		}
	}
	if shown := r.run("task", "get", prereq); !strings.Contains(shown, "ready_for_review") {
		t.Fatalf("prerequisite not advanced by the loop:\n%s", shown)
	}
	if shown := r.run("task", "get", goal); !strings.Contains(shown, "ready_for_review") {
		t.Fatalf("goal not advanced by the loop:\n%s", shown)
	}
	if got := backend.Commands(); len(got) != 2 {
		t.Fatalf("ran %d tasks, want 2", len(got))
	}
}

// TestRunCommandLoopSharesOneSession proves a `--goal` loop resolves one session
// for the whole invocation: every task it runs exports that same id, the loop
// reports it, and the loop's run events sit on it, so a goal run is followable
// as one timeline rather than a new session per task.
func TestRunCommandLoopSharesOneSession(t *testing.T) {
	r := newRunner(t)
	projectID, _, goal := loopContext(t, r)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out := r.run("run", "--goal", goal, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())

	prepared := backend.Prepared()
	if len(prepared) != 2 {
		t.Fatalf("prepared %d harnesses, want 2", len(prepared))
	}
	sid := prepared[0].Env[harnesspkg.EnvSession]
	if sid == "" {
		t.Fatal("the loop exported no FACTOTUM_SESSION")
	}
	if got := prepared[1].Env[harnesspkg.EnvSession]; got != sid {
		t.Fatalf("loop sessions = %q then %q, want one shared id", sid, got)
	}
	if !strings.Contains(out, "session: "+sid) {
		t.Fatalf("loop output does not report its session %q:\n%s", sid, out)
	}
	if listed := r.run("event", "list", "--project", projectID, "--session", sid); strings.Count(listed, "task.run_started") != 2 {
		t.Fatalf("the loop's run events are not all on session %q:\n%s", sid, listed)
	}
}

// TestRunCommandLoopBoundsItsSession proves the loop brackets the session it
// mints with session.started and session.retired, so a finished goal run leaves
// a retired session instead of a permanently alive one, while a session the
// caller inherited (FACTOTUM_SESSION) is never retired by the loop.
func TestRunCommandLoopBoundsItsSession(t *testing.T) {
	const inherited = "sess-chief"
	tests := []struct {
		name    string
		inherit bool
		minted  bool
	}{
		{name: "a loop that mints its session retires it", minted: true},
		{name: "a loop that inherits the caller's session leaves it alone", inherit: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRunner(t)
			_, _, goal := loopContext(t, r)

			backend := isofake.New("sandbox")
			backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
			r.runBackend = backend
			r.runHarness = harnessfake.New("opencode")
			if tt.inherit {
				r.getenv = func(key string) string {
					if key == "FACTOTUM_SESSION" {
						return inherited
					}
					return ""
				}
			}

			r.run("run", "--goal", goal, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())

			sid := inherited
			if tt.minted {
				prepared := backend.Prepared()
				if len(prepared) == 0 {
					t.Fatal("the loop prepared no harness")
				}
				sid = prepared[0].Env[harnesspkg.EnvSession]
				if sid == "" {
					t.Fatal("the loop exported no FACTOTUM_SESSION")
				}
			}
			var started, retired int
			for _, event := range listEvents(t, r) {
				if event.Session != sid {
					continue
				}
				switch event.Kind {
				case "session.started":
					started++
				case "session.retired":
					retired++
				}
			}
			if tt.minted {
				if started != 1 || retired != 1 {
					t.Fatalf("minted loop session %q started %d retired %d, want one of each", sid, started, retired)
				}
				return
			}
			if started != 0 || retired != 0 {
				t.Fatalf("inherited session %q started %d retired %d, want it untouched", sid, started, retired)
			}
		})
	}
}

// TestRunCommandRefusedInPreflightWritesNoSession pins the refusal an operator
// meets most: `ft run` against the unsandboxed local backend without
// --allow-host, or a goal loop stopped by the same refusal on its first task.
// The run is refused before the harness starts, so it must not leave an empty
// started/retired session row on the sessions page - a single-task run records
// the session it minted only once every preflight check has passed, and the
// loop records its one shared session at the same point.
func TestRunCommandRefusedInPreflightWritesNoSession(t *testing.T) {
	tests := []struct {
		name string
		loop bool
	}{
		{name: "a refused task run writes no session"},
		{name: "a refused goal loop writes no session", loop: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRunner(t)
			args := []string{"run"}
			if tt.loop {
				_, _, goal := loopContext(t, r)
				args = append(args, "--goal", goal)
			} else {
				_, taskID := runContext(t, r)
				args = append(args, taskID)
			}
			r.runHarness = harnessfake.New("opencode")

			_, stderr := r.runSplit(append(args,
				"--sandbox", "local", "--harness", "fake", "--workspace", t.TempDir())...)

			if !strings.Contains(stderr, "pass --allow-host") {
				t.Fatalf("run should have refused the local backend:\n%s", stderr)
			}
			for _, event := range listEvents(t, r) {
				if event.Kind == "session.started" || event.Kind == "session.retired" {
					t.Fatalf("a refused run recorded %s for session %q, want no session lifecycle", event.Kind, event.Session)
				}
			}
		})
	}
}

// TestRunCommandLoopInjectsHubEnvFromConfig proves the `--goal` loop reads the
// configured messaging hub (FACTOTUM_MSG_URL/FACTOTUM_SERVE_TOKEN) and hands it
// to each launched task harness, exactly as the single-task path does, so a
// remote receiver started by a goal run speaks the HTTP transport.
func TestRunCommandLoopInjectsHubEnvFromConfig(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(key string) string {
		switch key {
		case "FACTOTUM_MSG_URL":
			return "http://hub:8484"
		case "FACTOTUM_SERVE_TOKEN":
			return "tok"
		}
		return ""
	}
	_, _, goal := loopContext(t, r)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	r.run("run", "--goal", goal, "--actor", "claude", "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())

	prepared := backend.Prepared()
	if len(prepared) == 0 {
		t.Fatal("no harness prepared")
	}
	env := prepared[0].Env
	if env["FACTOTUM_MSG_URL"] != "http://hub:8484" {
		t.Errorf("harness env FACTOTUM_MSG_URL = %q, want the hub URL", env["FACTOTUM_MSG_URL"])
	}
	if env["FACTOTUM_SERVE_TOKEN"] != "tok" {
		t.Errorf("harness env FACTOTUM_SERVE_TOKEN = %q, want the serve token", env["FACTOTUM_SERVE_TOKEN"])
	}
}

func TestRunCommandLoopStopsOnBudget(t *testing.T) {
	r := newRunner(t)
	_, prereq, goal := loopContext(t, r)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out := r.run("run", "--goal", goal, "--max-tasks", "1", "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !strings.Contains(out, "stop: budget_exhausted") {
		t.Fatalf("run loop output missing budget stop:\n%s", out)
	}
	if shown := r.run("task", "get", prereq); !strings.Contains(shown, "ready_for_review") {
		t.Fatalf("prerequisite not advanced before budget:\n%s", shown)
	}
	if shown := r.run("task", "get", goal); !strings.Contains(shown, "(todo)") {
		t.Fatalf("goal should be untouched after budget stop:\n%s", shown)
	}
}

// An ungroomed on-path task is not agent-ready: the loop will not run it and
// reports it as not-run so a human knows to groom it.
func TestRunCommandLoopReportsUngroomedTaskNotRun(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("project", "repo", "create", projectID, "web", "--path", t.TempDir())
	r.run("actor", "create", "--kind", "agent", "claude")
	prereq := firstField(t, r.run("task", "create",
		"--project", projectID, "--repo", "web", "--title", "Prereq", "--body", "Do the first part."))
	r.run("task", "assign", prereq, "--actor", "claude")
	goal := firstField(t, r.run("task", "create",
		"--project", projectID, "--repo", "web", "--title", "Goal", "--body", "Do the last part.",
		"--groomed", "--acceptance", "the last part is done"))
	r.run("task", "assign", goal, "--actor", "claude")
	r.run("task", "dep", "create", goal, prereq)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out := r.run("run", "--goal", goal, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !strings.Contains(out, "stop: no_ready_work") {
		t.Fatalf("loop should stall on ungroomed work:\n%s", out)
	}
	if !strings.Contains(out, "not_run: "+prereq) {
		t.Fatalf("loop should report the ungroomed task as not-run:\n%s", out)
	}
	if got := backend.Commands(); len(got) != 0 {
		t.Fatalf("ran %d tasks, want 0", len(got))
	}
	if shown := r.run("task", "get", prereq); !strings.HasPrefix(shown, "(todo) ") {
		t.Fatalf("ungroomed task should be untouched:\n%s", shown)
	}
}

// loopField returns a `key: value` line's value from a loop's single-result
// output, if the loop printed that field.
func loopField(out, key string) (string, bool) {
	for _, line := range strings.Split(out, "\n") {
		if value, ok := strings.CutPrefix(line, key+": "); ok {
			return strings.TrimSpace(value), true
		}
	}
	return "", false
}

// TestRunCommandLoopSelectingNoWorkReportsOnlyARecordedSession pins what a loop
// which runs no task may report: a minted id was never recorded - nothing writes
// session.started for it - so printing it would name a session that resolves to
// no events and no row on the sessions page, while an inherited session exists
// independently of this loop and is always reported.
func TestRunCommandLoopSelectingNoWorkReportsOnlyARecordedSession(t *testing.T) {
	const inherited = "s-caller99"
	tests := []struct {
		name    string
		inherit bool
	}{
		{name: "a minted id is not reported when no task ran"},
		{name: "an inherited session is reported when no task ran", inherit: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRunner(t)
			goal := goalWithNoReadyWork(t, r)
			backend := isofake.New("sandbox")
			r.runBackend = backend
			r.runHarness = harnessfake.New("opencode")
			if tt.inherit {
				r.getenv = func(key string) string {
					if key == "FACTOTUM_SESSION" {
						return inherited
					}
					return ""
				}
			}

			out := r.run("run", "--goal", goal, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
			if !strings.Contains(out, "iterations: 0") || !strings.Contains(out, "stop: no_ready_work") {
				t.Fatalf("expected a zero-iteration loop:\n%s", out)
			}
			if got := backend.Commands(); len(got) != 0 {
				t.Fatalf("ran %d tasks, want 0", len(got))
			}
			for _, event := range listEvents(t, r) {
				if event.Kind == "session.started" || event.Kind == "session.retired" {
					t.Fatalf("a no-work loop recorded %s for session %q, want no session lifecycle", event.Kind, event.Session)
				}
			}
			sid, reported := loopField(out, "session")
			if !tt.inherit {
				if reported {
					t.Fatalf("a no-work loop reported session %q, which resolves to no recorded session:\n%s", sid, out)
				}
				return
			}
			if !reported || sid != inherited {
				t.Fatalf("a no-work loop reported session %q, want the inherited %q:\n%s", sid, inherited, out)
			}
		})
	}
}

// TestRunCommandLoopSelectingNoWorkReportsOnlyARecordedSessionInStructuredOutput
// pins the same rule in `-o json`: the session field appears exactly when the
// loop has a session to report, so a machine reader never follows an id that
// resolves to nothing and never loses the caller's.
func TestRunCommandLoopSelectingNoWorkReportsOnlyARecordedSessionInStructuredOutput(t *testing.T) {
	const inherited = "s-caller99"
	tests := []struct {
		name    string
		inherit bool
	}{
		{name: "a minted id is omitted when no task ran"},
		{name: "an inherited session is carried when no task ran", inherit: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRunner(t)
			goal := goalWithNoReadyWork(t, r)
			r.runBackend = isofake.New("sandbox")
			r.runHarness = harnessfake.New("opencode")
			if tt.inherit {
				r.getenv = func(key string) string {
					if key == "FACTOTUM_SESSION" {
						return inherited
					}
					return ""
				}
			}

			out := r.run("run", "--goal", goal, "-o", "json", "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
			var doc map[string]any
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatalf("decode loop output: %v\n%s", err, out)
			}
			if doc["iterations"] != float64(0) {
				t.Fatalf("iterations = %v, want 0\n%s", doc["iterations"], out)
			}
			session, ok := doc["session"]
			if !tt.inherit {
				if ok {
					t.Fatalf("a no-work loop reported session %v, want the field omitted:\n%s", session, out)
				}
				return
			}
			if !ok || session != inherited {
				t.Fatalf("a no-work loop reported session %v, want the inherited %q:\n%s", session, inherited, out)
			}
		})
	}
}

// captureLoopContext creates a project with an idea capture and two groomed,
// agent-assigned tasks that belong to it (a two-task chain), so `ft run <idea>`
// can be exercised. Both tasks carry the idea as their origin edge.
func captureLoopContext(t *testing.T, r *runner) (projectID, idea, first, second string) {
	t.Helper()
	projectID = firstField(t, r.run("project", "create", "Acme"))
	r.run("project", "repo", "create", projectID, "web", "--path", t.TempDir())
	r.run("actor", "create", "--kind", "agent", "claude")
	idea = firstField(t, r.run("idea", "create", "-p", projectID, "-t", "Feature"))
	first = firstField(t, r.run("task", "create",
		"--project", projectID, "--repo", "web", "--title", "First", "--body", "Do the first part.",
		"--groomed", "--acceptance", "the first part is done", "--dep", idea))
	r.run("task", "assign", first, "--actor", "claude")
	second = firstField(t, r.run("task", "create",
		"--project", projectID, "--repo", "web", "--title", "Second", "--body", "Do the last part.",
		"--groomed", "--acceptance", "the last part is done", "--dep", idea, "--dep", first))
	r.run("task", "assign", second, "--actor", "claude")
	return projectID, idea, first, second
}

// `ft run <idea>` targets the capture, not a single task: it runs every task
// that belongs to the idea, in DAG order, and reports the loop outcome.
func TestRunCommandTargetsCaptureRunsAllTasks(t *testing.T) {
	r := newRunner(t)
	_, idea, first, second := captureLoopContext(t, r)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out := r.run("run", idea, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	for _, want := range []string{"goal: " + idea, "goal_kind: idea", "stop: goal_reached", "iterations: 2", first, second} {
		if !strings.Contains(out, want) {
			t.Fatalf("capture run output missing %q:\n%s", want, out)
		}
	}
	if got := backend.Commands(); len(got) != 2 {
		t.Fatalf("ran %d tasks, want 2", len(got))
	}
	for _, id := range []string{first, second} {
		if shown := r.run("task", "get", id); !strings.Contains(shown, "ready_for_review") {
			t.Fatalf("task %s not advanced by the capture loop:\n%s", id, shown)
		}
	}
}

// `ft run --goal <idea>` is the same capture loop as the positional form.
func TestRunCommandGoalFlagTargetsCapture(t *testing.T) {
	r := newRunner(t)
	_, idea, _, _ := captureLoopContext(t, r)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out := r.run("run", "--goal", idea, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !strings.Contains(out, "goal_kind: idea") || !strings.Contains(out, "stop: goal_reached") {
		t.Fatalf("--goal capture run output unexpected:\n%s", out)
	}
}

// A stored prompt applies to a single-task or task-less run; a capture is a
// loop, so a prompt source is a usage error.
func TestRunCommandCaptureRejectsPromptSource(t *testing.T) {
	r := newRunner(t)
	_, idea, _, _ := captureLoopContext(t, r)
	r.runBackend = isofake.New("sandbox")
	r.runHarness = harnessfake.New("opencode")
	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}

	err := r.runErr("run", idea, "--prompt-file", promptFile, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("capture with a prompt source error = %v, want usage", err)
	}
}

func TestRunCommandLoopGoalSelectionIsExclusive(t *testing.T) {
	r := newRunner(t)
	r.runHarness = harnessfake.New("opencode")
	workspace := t.TempDir()

	if err := r.runErr("run", "t-anything", "--goal", "t-other", "--sandbox", "fake", "--harness", "fake", "--workspace", workspace); !errors.Is(err, ErrUsage) {
		t.Fatalf("task-id + --goal error = %v, want usage", err)
	}
	if err := r.runErr("run", "--sandbox", "fake", "--harness", "fake", "--workspace", workspace); !errors.Is(err, ErrUsage) {
		t.Fatalf("neither task-id nor --goal error = %v, want usage", err)
	}
}

// TestRunCommandRefreshUpdatesReusedCheckout drives `ft run` against a real
// file:// remote through the real git executor, proving --refresh reaches the
// workspace: the first run clones, an unrefreshed re-run stays stale, and a
// refreshed re-run follows the advanced upstream.
func TestRunCommandRefreshUpdatesReusedCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	src := t.TempDir()
	commitCLIRepo(t, src, "v1\n")

	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("project", "repo", "create", projectID, "web", "--url", "file://"+src)
	taskID := firstField(t, r.run("task", "create", "--project", projectID, "--repo", "web", "--title", "T", "--body", "B"))

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")
	workspace := t.TempDir()
	checkout := filepath.Join(workspace, "web")
	args := func(extra ...string) []string {
		return append([]string{"run", taskID, "--sandbox", "fake", "--harness", "fake", "--workspace", workspace}, extra...)
	}

	r.run(args()...)
	if got := readCLIFile(t, filepath.Join(checkout, "README.md")); got != "v1\n" {
		t.Fatalf("checkout README = %q, want v1 after the first run", got)
	}

	commitCLIRepo(t, src, "v2\n")

	r.run(args()...)
	if got := readCLIFile(t, filepath.Join(checkout, "README.md")); got != "v1\n" {
		t.Fatalf("checkout README = %q, want the stale v1 without --refresh", got)
	}

	r.run(args("--refresh")...)
	if got := readCLIFile(t, filepath.Join(checkout, "README.md")); got != "v2\n" {
		t.Fatalf("checkout README = %q, want v2 after --refresh", got)
	}
}

func commitCLIRepo(t *testing.T, dir, contents string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		runCLIGit(t, dir, "init", "-q")
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	runCLIGit(t, dir, "add", "README.md")
	runCLIGit(t, dir, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-q", "-m", "update")
}

func runCLIGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func readCLIFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestRunCommandFailureLeavesTaskIntact(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("boom"), ExitCode: 3})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	err := r.runErr("run", taskID, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, app.ErrRunFailed) {
		t.Fatalf("run error = %v, want ErrRunFailed", err)
	}
	if shown := r.run("task", "get", taskID); !strings.HasPrefix(shown, "(todo) ") {
		t.Fatalf("failed run changed task status:\n%s", shown)
	}
}

// TestRunCommandPrintsHarnessStderrOnFailure proves a failed run prints the
// harness's stderr next to the exit code, so the reason is visible without
// re-running by hand.
func TestRunCommandPrintsHarnessStderrOnFailure(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{
		Stdout:   []byte("partial work\n"),
		Stderr:   []byte("No API key found for opencode-go. Use /login\n"),
		ExitCode: 1,
	})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out, stderrOut, err := r.runSplitErr("run", taskID, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, app.ErrRunFailed) {
		t.Fatalf("run error = %v, want ErrRunFailed", err)
	}
	for _, want := range []string{"run: failed", "exit_code: 1", "complete: false", "stderr:", "No API key found for opencode-go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("run output missing %q:\nstdout:\n%s\nstderr:\n%s", want, out, stderrOut)
		}
	}
	if !strings.Contains(out, "partial work") {
		t.Fatalf("run output dropped the harness stdout:\n%s", out)
	}
}

// TestRunCommandJSONCarriesHarnessStderr proves the structured output keeps the
// stderr in a bounded field, so a machine reader learns why a run failed without
// the exit code alone.
func TestRunCommandJSONCarriesHarnessStderr(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{
		Stderr:   []byte("No API key found for opencode-go. Use /login\n"),
		ExitCode: 1,
	})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out, _, err := r.runSplitErr("run", taskID, "-o", "json", "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, app.ErrRunFailed) {
		t.Fatalf("run error = %v, want ErrRunFailed", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("run -o json output is not JSON: %v\n%s", err, out)
	}
	if got, _ := doc["stderr"].(string); !strings.Contains(got, "No API key found for opencode-go") {
		t.Fatalf("json stderr = %q, want the harness's error stream\n%s", got, out)
	}
	if got, _ := doc["exit_code"].(float64); got != 1 {
		t.Fatalf("json exit_code = %v, want 1\n%s", doc["exit_code"], out)
	}
	if got, _ := doc["complete"].(bool); got {
		t.Fatalf("json complete = true, want false\n%s", out)
	}
}

// TestRunCommandTasklessPrintsHarnessStderrOnFailure is the task-less twin: a
// prompt run that dies in the sandbox reports the harness's stderr too.
func TestRunCommandTasklessPrintsHarnessStderrOnFailure(t *testing.T) {
	r := newRunner(t)
	_, cfgPath := tasklessContext(t, r)
	promptPath := filepath.Join(t.TempDir(), "grooming.md")
	if err := os.WriteFile(promptPath, []byte("You are the grooming agent.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{
		Stderr:   []byte("docker: Error response from daemon: No such image: pi\n"),
		ExitCode: 125,
	})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out, _, err := r.runSplitErr("--config", cfgPath, "run", "--prompt-file", promptPath,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, app.ErrRunFailed) {
		t.Fatalf("task-less run error = %v, want ErrRunFailed", err)
	}
	for _, want := range []string{"run: failed", "exit_code: 125", "stderr:", "No such image: pi"} {
		if !strings.Contains(out, want) {
			t.Fatalf("task-less run output missing %q:\n%s", want, out)
		}
	}
}

// TestRunCommandGoalPrintsHarnessStderrOnFailure proves a failed step in the
// --goal loop prints the harness's stderr too, so a loop that stops on a broken
// harness names the reason instead of only the exit code.
func TestRunCommandGoalPrintsHarnessStderrOnFailure(t *testing.T) {
	r := newRunner(t)
	_, prereq, goal := loopContext(t, r)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{
		Stderr:   []byte("No API key found for opencode-go. Use /login\n"),
		ExitCode: 1,
	})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out, _, err := r.runSplitErr("run", "--goal", goal, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, app.ErrRunFailed) {
		t.Fatalf("loop run error = %v, want ErrRunFailed", err)
	}
	for _, want := range []string{"stop: failed", prereq, "No API key found for opencode-go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("loop output missing %q:\n%s", want, out)
		}
	}
	// The step's stderr is a block, not another field: a blank line separates it
	// from the fields, like the task and task-less run blocks.
	if !strings.Contains(out, "remaining: 2\n\nstderr:\n") {
		t.Fatalf("loop step stderr is not a separated block:\n%s", out)
	}
}
