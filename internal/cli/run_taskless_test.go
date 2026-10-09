package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
)

// tasklessContext creates a project with two local repo checkouts and no task,
// so a task-less `ft run` has every project repo to materialize. It returns the
// project id and a config file that makes it the configured project.
func tasklessContext(t *testing.T, r *runner) (projectID, cfgPath string) {
	t.Helper()
	projectID = firstField(t, r.run("project", "create", "Acme"))
	r.run("project", "repo", "create", projectID, "backend", "--path", t.TempDir())
	r.run("project", "repo", "create", projectID, "web", "--path", t.TempDir())
	return projectID, writeProjectConfig(t, projectID)
}

func TestRunCommandTasklessPromptFileRunsAllRepos(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	promptPath := filepath.Join(t.TempDir(), "grooming.md")
	if err := os.WriteFile(promptPath, []byte("You are the grooming agent.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	backend := promptRunBackend()
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")
	workspace := t.TempDir()

	out := r.run("--config", cfgPath, "run", "--prompt-file", promptPath,
		"--sandbox", "fake", "--harness", "fake", "--workspace", workspace)
	for _, want := range []string{"run: finished", "exit_code: 0", "complete: true", "repos: backend, web", "project: " + projectID} {
		if !strings.Contains(out, want) {
			t.Fatalf("task-less run output missing %q:\n%s", want, out)
		}
	}
	if got := strings.TrimSpace(lastPrompt(t, backend)); got != "You are the grooming agent." {
		t.Fatalf("prompt = %q, want the file content verbatim", got)
	}
	specs := backend.Prepared()
	if len(specs) != 1 {
		t.Fatalf("Prepare called %d times, want 1", len(specs))
	}
	if specs[0].Workdir != workspace {
		t.Fatalf("prepared Workdir = %q, want the all-repos workspace root %q", specs[0].Workdir, workspace)
	}
}

func TestRunCommandTasklessPromptArtifactRunsAllRepos(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	docID := firstField(t, r.run("doc", "create", "-p", projectID,
		"-t", "Grooming prompt", "-b", "You are the grooming agent."))

	backend := promptRunBackend()
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	r.run("--config", cfgPath, "run", "--prompt-artifact", docID,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if got := strings.TrimSpace(lastPrompt(t, backend)); got != "You are the grooming agent." {
		t.Fatalf("prompt = %q, want the artifact body verbatim", got)
	}
}

// TestRunCommandTasklessInjectsHubEnvFromConfig proves a task-less `ft run` with
// an actor reads the configured messaging hub and hands it to the launched
// harness, exactly as the single-task path does, so a task-less session can
// message peers over the HTTP transport instead of the local store.
func TestRunCommandTasklessInjectsHubEnvFromConfig(t *testing.T) {
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
	_, cfgPath := tasklessContext(t, r)
	r.run("actor", "create", "--kind", "agent", "claude")
	promptPath := filepath.Join(t.TempDir(), "p.md")
	mustWrite(t, promptPath, "groom")

	backend := promptRunBackend()
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	r.run("--config", cfgPath, "run", "--actor", "claude", "--prompt-file", promptPath,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())

	specs := backend.Prepared()
	if len(specs) != 1 {
		t.Fatalf("Prepare called %d times, want 1", len(specs))
	}
	env := specs[0].Env
	if env["FACTOTUM_MSG_URL"] != "http://hub:8484" {
		t.Errorf("harness env FACTOTUM_MSG_URL = %q, want the hub URL", env["FACTOTUM_MSG_URL"])
	}
	if env["FACTOTUM_SERVE_TOKEN"] != "tok" {
		t.Errorf("harness env FACTOTUM_SERVE_TOKEN = %q, want the serve token", env["FACTOTUM_SERVE_TOKEN"])
	}
}

// TestRunCommandTasklessWarnsWhenHubTokenMissing proves a task-less `ft run`
// with an actor surfaces a half-configured hub instead of silently launching a
// receiver that fails every message; the warning matches the single-task path
// and the incomplete hub is dropped from the harness env.
func TestRunCommandTasklessWarnsWhenHubTokenMissing(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(key string) string {
		if key == "FACTOTUM_MSG_URL" {
			return "http://hub:8484"
		}
		return ""
	}
	_, cfgPath := tasklessContext(t, r)
	r.run("actor", "create", "--kind", "agent", "claude")
	promptPath := filepath.Join(t.TempDir(), "p.md")
	mustWrite(t, promptPath, "groom")

	backend := promptRunBackend()
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	_, stderr := r.runSplit("--config", cfgPath, "run", "--actor", "claude", "--prompt-file", promptPath,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !strings.Contains(stderr, "serve.token") || !strings.Contains(stderr, "serve.url") {
		t.Fatalf("task-less run stderr missing the hub-token warning:\n%s", stderr)
	}
	prepared := backend.Prepared()
	if len(prepared) != 1 {
		t.Fatalf("Prepare called %d times, want 1", len(prepared))
	}
	if _, ok := prepared[0].Env["FACTOTUM_MSG_URL"]; ok {
		t.Errorf("harness env carried a hub URL with no token: %v", prepared[0].Env)
	}
}

// TestRunCommandTasklessWithoutActorOmitsHubWarning proves a task-less run with
// no actor installs no receiver, so the half-configured-hub warning is
// suppressed there exactly as on the single-task path.
func TestRunCommandTasklessWithoutActorOmitsHubWarning(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(key string) string {
		if key == "FACTOTUM_MSG_URL" {
			return "http://hub:8484"
		}
		return ""
	}
	_, cfgPath := tasklessContext(t, r)
	promptPath := filepath.Join(t.TempDir(), "p.md")
	mustWrite(t, promptPath, "groom")

	backend := promptRunBackend()
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	_, stderr := r.runSplit("--config", cfgPath, "run", "--prompt-file", promptPath,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if strings.Contains(stderr, "serve.token") {
		t.Fatalf("task-less run with no actor warned about the incomplete hub:\n%s", stderr)
	}
}

func TestRunCommandTasklessRequiresPromptSource(t *testing.T) {
	r := newRunner(t)
	r.runHarness = harnessfake.New("opencode")

	err := r.runErr("run", "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("no task, goal, or prompt error = %v, want usage", err)
	}
}

func TestRunCommandTasklessRequiresProject(t *testing.T) {
	r := newRunner(t)
	r.runHarness = harnessfake.New("opencode")
	path := filepath.Join(t.TempDir(), "p.md")
	if err := os.WriteFile(path, []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := r.runErr("run", "--prompt-file", path, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("task-less run with no project error = %v, want usage", err)
	}
}

// A missing or empty prompt file is an input error (exit 1), consistent with the
// sibling --file/--body-file flags, not a usage error (exit 2).
func TestRunCommandTasklessPromptFileMissingExitsOne(t *testing.T) {
	r := newRunner(t)
	_, cfgPath := tasklessContext(t, r)
	r.runHarness = harnessfake.New("opencode")
	missing := filepath.Join(t.TempDir(), "nope.md")

	err := r.runErr("--config", cfgPath, "run", "--prompt-file", missing,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if errors.Is(err, ErrUsage) {
		t.Fatalf("missing prompt file error = %v, want a non-usage input error", err)
	}
	if code := ExitCode(err); code != 1 {
		t.Fatalf("missing prompt file exit = %d, want 1", code)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("error does not name the missing file: %v", err)
	}
}
