package cli

import (
	"errors"
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
	if shown := r.run("task", "get", taskID); !strings.HasPrefix(shown, "(todo) ") {
		t.Fatalf("local run without opt-in changed task state:\n%s", shown)
	}
}

// loopContext creates a project whose one repo is an existing checkout and a
// two-task chain (goal depends on prereq), so `ft run --goal` can be exercised.
func loopContext(t *testing.T, r *runner) (projectID, prereq, goal string) {
	t.Helper()
	projectID = firstField(t, r.run("project", "create", "Acme"))
	r.run("project", "repo", "create", projectID, "web", "--path", t.TempDir())
	prereq = firstField(t, r.run("task", "create",
		"--project", projectID, "--repo", "web", "--title", "Prereq", "--body", "Do the first part."))
	goal = firstField(t, r.run("task", "create",
		"--project", projectID, "--repo", "web", "--title", "Goal", "--body", "Do the last part."))
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
