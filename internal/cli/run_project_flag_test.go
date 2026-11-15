package cli

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	harnesspkg "github.com/khoinguyen/factotum/pkg/harness"
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
)

// seededProject is a second project with its own store, seeded so a
// cross-project run test can address it with -p. runner owns that store, so a
// test can read back what the run wrote there.
type seededProject struct {
	runner *runner
	id     string
	taskID string
	dbPath string
}

// seedRunProject creates a project in a store of its own - a separate jsonfile
// database, the way the machine registry maps each project to one - and
// registers that store in r's machine config, so `ft run -p <id>` can reach it.
//
// The project is seeded through a second runner whose --store is the second
// database. That is how a real second project's store comes into being, and it
// keeps the seeded task genuinely absent from the configured project's store.
func seedRunProject(t *testing.T, r *runner, name string) seededProject {
	t.Helper()
	other := newRunner(t)
	dbPath := filepath.Join(t.TempDir(), strings.ToLower(name)+".json")
	other.path = dbPath
	projectID := firstField(t, other.run("project", "create", name))
	other.run("project", "repo", "create", projectID, "web", "--path", t.TempDir())
	taskID := firstField(t, other.run("task", "create",
		"--project", projectID, "--repo", "web", "--title", "Ship the widget", "--body", "Do the work."))
	mustWrite(t, r.userPath,
		"[projects."+projectID+".store]\nbackend = \"jsonfile\"\n\n"+
			"[projects."+projectID+".store.options]\npath = \""+dbPath+"\"\n")
	return seededProject{runner: other, id: projectID, taskID: taskID, dbPath: dbPath}
}

// TestRunProjectFlagRunsTaskFromAnotherProjectsStore pins the acceptance
// contract: -p selects the named project's store, so a task that lives only
// there is found, run, and advanced there, and the launched session is handed
// that store rather than the caller's.
func TestRunProjectFlagRunsTaskFromAnotherProjectsStore(t *testing.T) {
	r := newRunner(t)
	_, alphaTask := runContext(t, r)
	beta := seedRunProject(t, r, "Beta")

	backend := promptRunBackend()
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out := r.run("run", beta.taskID, "-p", beta.id,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	for _, want := range []string{
		"task_id: " + beta.taskID, "run: finished", "status: ready_for_review", "project: " + beta.id,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("run -p output missing %q:\n%s", want, out)
		}
	}

	prepared := backend.Prepared()
	if len(prepared) != 1 {
		t.Fatalf("Prepare called %d times, want 1", len(prepared))
	}
	if want := "path=" + beta.dbPath; prepared[0].Env[harnesspkg.EnvStoreOpts] != want {
		t.Errorf("session store opts = %q, want the selected project's store %q",
			prepared[0].Env[harnesspkg.EnvStoreOpts], want)
	}

	if shown := beta.runner.run("task", "get", beta.taskID); !strings.Contains(shown, "ready_for_review") {
		t.Fatalf("the run did not advance the task in the selected project's store:\n%s", shown)
	}
	if shown := r.run("task", "get", alphaTask); !strings.HasPrefix(shown, "(todo) ") {
		t.Fatalf("the cross-project run touched the caller's task:\n%s", shown)
	}
}

// TestRunWithoutProjectFlagUsesConfiguredProject pins that -p is the only
// switch: with a second project registered, an unflagged run still resolves the
// configured project's store, exactly as before the flag existed.
func TestRunWithoutProjectFlagUsesConfiguredProject(t *testing.T) {
	r := newRunner(t)
	alpha, alphaTask := runContext(t, r)
	cfgPath := writeProjectConfig(t, alpha)
	seedRunProject(t, r, "Beta")

	backend := promptRunBackend()
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out := r.run("--config", cfgPath, "run", alphaTask,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !strings.Contains(out, "project: "+alpha) {
		t.Fatalf("unflagged run did not run the configured project's task:\n%s", out)
	}
	prepared := backend.Prepared()
	if len(prepared) != 1 {
		t.Fatalf("Prepare called %d times, want 1", len(prepared))
	}
	if want := "path=" + r.path; prepared[0].Env[harnesspkg.EnvStoreOpts] != want {
		t.Errorf("session store opts = %q, want the configured store %q",
			prepared[0].Env[harnesspkg.EnvStoreOpts], want)
	}
}

// TestRunProjectFlagRejectsTaskFromAnotherProject pins that -p is authoritative:
// a task the selected project does not own is a not-found error (exit 3) naming
// the task and the project, never a silent run of another project's work.
func TestRunProjectFlagRejectsTaskFromAnotherProject(t *testing.T) {
	r := newRunner(t)
	alpha, alphaTask := runContext(t, r)
	cfgPath := writeProjectConfig(t, alpha)
	beta := firstField(t, r.run("project", "create", "Beta"))

	backend := promptRunBackend()
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	_, _, err := r.runSplitErr("--config", cfgPath, "run", alphaTask, "-p", beta,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("cross-project run error = %v, want not-found", err)
	}
	if code := ExitCode(err); code != 3 {
		t.Fatalf("cross-project run exit = %d, want 3", code)
	}
	for _, want := range []string{alphaTask, beta} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("not-found error %q does not name %q", err, want)
		}
	}
	if got := backend.Prepared(); len(got) != 0 {
		t.Fatalf("the rejected run still prepared %d harnesses", len(got))
	}
	if shown := r.run("task", "get", alphaTask); !strings.HasPrefix(shown, "(todo) ") {
		t.Fatalf("the rejected run changed the task's status:\n%s", shown)
	}
}

// TestRunGoalProjectFlagDrivesAnotherProjectsStore pins that the --goal loop
// resolves the same selected project as a single-task run: it reads the goal and
// ranks its work in the named project's store, runs it there, and hands each
// launched session that store.
func TestRunGoalProjectFlagDrivesAnotherProjectsStore(t *testing.T) {
	r := newRunner(t)
	beta := seedRunProject(t, r, "Beta")

	beta.runner.run("actor", "create", "--kind", "agent", "claude")
	goal := firstField(t, beta.runner.run("task", "create",
		"--project", beta.id, "--repo", "web", "--title", "Ship the widget", "--body", "Do the work.",
		"--groomed", "--acceptance", "the widget ships"))
	beta.runner.run("task", "assign", goal, "--actor", "claude")

	backend := promptRunBackend()
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out := r.run("run", "--goal", goal, "-p", beta.id,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	for _, want := range []string{"goal: " + goal, "stop: goal_reached", "project: " + beta.id} {
		if !strings.Contains(out, want) {
			t.Fatalf("goal run -p output missing %q:\n%s", want, out)
		}
	}
	if shown := beta.runner.run("task", "get", goal); !strings.Contains(shown, "ready_for_review") {
		t.Fatalf("the loop did not advance the goal in the selected project's store:\n%s", shown)
	}
	prepared := backend.Prepared()
	if len(prepared) != 1 {
		t.Fatalf("Prepare called %d times, want 1", len(prepared))
	}
	if want := "path=" + beta.dbPath; prepared[0].Env[harnesspkg.EnvStoreOpts] != want {
		t.Errorf("session store opts = %q, want the selected project's store %q",
			prepared[0].Env[harnesspkg.EnvStoreOpts], want)
	}
}

// TestRunTasklessProjectFlagRunsAnotherProjectsRepos pins that -p reaches the
// task-less prompt run too: it materializes the named project's repos and hands
// the session that project's store.
func TestRunTasklessProjectFlagRunsAnotherProjectsRepos(t *testing.T) {
	r := newRunner(t)
	_, cfgPath := tasklessContext(t, r)
	beta := seedRunProject(t, r, "Beta")

	promptPath := filepath.Join(t.TempDir(), "grooming.md")
	mustWrite(t, promptPath, "You are the grooming agent.\n")

	backend := promptRunBackend()
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out := r.run("--config", cfgPath, "run", "--prompt-file", promptPath, "-p", beta.id,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	for _, want := range []string{"repos: web", "project: " + beta.id, "run: finished"} {
		if !strings.Contains(out, want) {
			t.Fatalf("task-less run -p output missing %q:\n%s", want, out)
		}
	}
	prepared := backend.Prepared()
	if len(prepared) != 1 {
		t.Fatalf("Prepare called %d times, want 1", len(prepared))
	}
	if want := "path=" + beta.dbPath; prepared[0].Env[harnesspkg.EnvStoreOpts] != want {
		t.Errorf("session store opts = %q, want the selected project's store %q",
			prepared[0].Env[harnesspkg.EnvStoreOpts], want)
	}
}
