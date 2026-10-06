package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
)

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

	out := r.run("run", taskID, "--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())
	for _, want := range []string{"task_id: " + taskID, "run: finished", "status: ready_for_review", "exit_code: 0"} {
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

func TestRunCommandRequiresExplicitSelection(t *testing.T) {
	r := newRunner(t)
	r.runHarness = harnessfake.New("opencode")
	workspace := t.TempDir()

	if err := r.runErr("run", "t-anything", "--harness", "fake", "--workspace", workspace); !errors.Is(err, ErrUsage) {
		t.Fatalf("missing backend error = %v, want usage", err)
	}
	if err := r.runErr("run", "t-anything", "--backend", "fake", "--workspace", workspace); !errors.Is(err, ErrUsage) {
		t.Fatalf("missing harness error = %v, want usage", err)
	}
	if err := r.runErr("run", "t-anything", "--backend", "nope", "--harness", "fake", "--workspace", workspace); !errors.Is(err, ErrUsage) {
		t.Fatalf("unknown backend error = %v, want usage", err)
	}
}

func TestRunCommandLocalBackendRequiresOptIn(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)
	r.runHarness = harnessfake.New("opencode")

	err := r.runErr("run", taskID, "--backend", "local", "--harness", "fake", "--workspace", t.TempDir())
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

	err := r.runErr("run", "--goal", goal, "--backend", "local", "--harness", "fake", "--workspace", t.TempDir())
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

	out := r.run("run", "--goal", goal, "--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())
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

func TestRunCommandLoopStopsOnBudget(t *testing.T) {
	r := newRunner(t)
	_, prereq, goal := loopContext(t, r)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out := r.run("run", "--goal", goal, "--max-tasks", "1", "--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())
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

	out := r.run("run", "--goal", goal, "--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())
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

func TestRunCommandLoopGoalSelectionIsExclusive(t *testing.T) {
	r := newRunner(t)
	r.runHarness = harnessfake.New("opencode")
	workspace := t.TempDir()

	if err := r.runErr("run", "t-anything", "--goal", "t-other", "--backend", "fake", "--harness", "fake", "--workspace", workspace); !errors.Is(err, ErrUsage) {
		t.Fatalf("task-id + --goal error = %v, want usage", err)
	}
	if err := r.runErr("run", "--backend", "fake", "--harness", "fake", "--workspace", workspace); !errors.Is(err, ErrUsage) {
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
		return append([]string{"run", taskID, "--backend", "fake", "--harness", "fake", "--workspace", workspace}, extra...)
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

	err := r.runErr("run", taskID, "--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, app.ErrRunFailed) {
		t.Fatalf("run error = %v, want ErrRunFailed", err)
	}
	if shown := r.run("task", "get", taskID); !strings.HasPrefix(shown, "(todo) ") {
		t.Fatalf("failed run changed task status:\n%s", shown)
	}
}
