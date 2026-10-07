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
		"--backend", "fake", "--harness", "fake", "--workspace", workspace)
	for _, want := range []string{"run: finished", "exit_code: 0", "repos: backend, web", "project: " + projectID} {
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
		"--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if got := strings.TrimSpace(lastPrompt(t, backend)); got != "You are the grooming agent." {
		t.Fatalf("prompt = %q, want the artifact body verbatim", got)
	}
}

func TestRunCommandTasklessRequiresPromptSource(t *testing.T) {
	r := newRunner(t)
	r.runHarness = harnessfake.New("opencode")

	err := r.runErr("run", "--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())
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

	err := r.runErr("run", "--prompt-file", path, "--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())
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
		"--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())
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
