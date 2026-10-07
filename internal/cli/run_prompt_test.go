package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
)

// promptRunBackend returns a fake backend that completes one run with output.
func promptRunBackend() *isofake.Backend {
	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	return backend
}

// lastPrompt returns the prompt the fake harness passed to its process.
func lastPrompt(t *testing.T, backend *isofake.Backend) string {
	t.Helper()
	commands := backend.Commands()
	if len(commands) != 1 {
		t.Fatalf("Exec called %d times, want 1", len(commands))
	}
	argv := commands[0].Argv
	return argv[len(argv)-1]
}

func TestRunCommandPromptFileOverridesTaskPrompt(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)
	promptPath := filepath.Join(t.TempDir(), "grooming.md")
	if err := os.WriteFile(promptPath, []byte("You are the grooming agent.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	backend := promptRunBackend()
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	r.run("run", taskID, "--prompt-file", promptPath,
		"--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())

	prompt := lastPrompt(t, backend)
	if got := strings.TrimSpace(prompt); got != "You are the grooming agent." {
		t.Fatalf("prompt = %q, want the file content", got)
	}
	if strings.Contains(prompt, "Add widget") || strings.Contains(prompt, "Do the work.") {
		t.Fatalf("prompt still carries the task text:\n%s", prompt)
	}
}

func TestRunCommandPromptArtifactOverridesTaskPrompt(t *testing.T) {
	r := newRunner(t)
	projectID, taskID := runContext(t, r)
	docID := firstField(t, r.run("doc", "create", "-p", projectID,
		"-t", "Grooming prompt", "-b", "You are the grooming agent."))

	backend := promptRunBackend()
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	r.run("run", taskID, "--prompt-artifact", docID,
		"--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())

	prompt := lastPrompt(t, backend)
	if got := strings.TrimSpace(prompt); got != "You are the grooming agent." {
		t.Fatalf("prompt = %q, want the artifact body", got)
	}
	if strings.Contains(prompt, "Add widget") || strings.Contains(prompt, "Do the work.") {
		t.Fatalf("prompt still carries the task text:\n%s", prompt)
	}
}

func TestRunCommandPromptFileMissingErrorsClearly(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)
	r.runBackend = promptRunBackend()
	r.runHarness = harnessfake.New("opencode")
	missing := filepath.Join(t.TempDir(), "nope.md")

	err := r.runErr("run", taskID, "--prompt-file", missing,
		"--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("missing prompt file error = %v, want usage", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("error does not name the missing file: %v", err)
	}
	if shown := r.run("task", "get", taskID); !strings.HasPrefix(shown, "(todo) ") {
		t.Fatalf("a missing prompt file changed task state:\n%s", shown)
	}
}

func TestRunCommandPromptFileEmptyErrorsClearly(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)
	r.runBackend = promptRunBackend()
	r.runHarness = harnessfake.New("opencode")
	empty := filepath.Join(t.TempDir(), "empty.md")
	if err := os.WriteFile(empty, []byte("  \n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := r.runErr("run", taskID, "--prompt-file", empty,
		"--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("empty prompt file error = %v, want usage", err)
	}
}

func TestRunCommandPromptArtifactMissingErrorsClearly(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)
	r.runBackend = promptRunBackend()
	r.runHarness = harnessfake.New("opencode")

	err := r.runErr("run", taskID, "--prompt-artifact", "doc-nope",
		"--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing prompt artifact error = %v, want not_found", err)
	}
	if !strings.Contains(err.Error(), "doc-nope") {
		t.Fatalf("error does not name the missing artifact: %v", err)
	}
}

func TestRunCommandPromptArtifactEmptyErrorsClearly(t *testing.T) {
	r := newRunner(t)
	projectID, taskID := runContext(t, r)
	docID := firstField(t, r.run("doc", "create", "-p", projectID, "-t", "Blank", "-b", "   "))
	r.runBackend = promptRunBackend()
	r.runHarness = harnessfake.New("opencode")

	err := r.runErr("run", taskID, "--prompt-artifact", docID,
		"--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("empty prompt artifact error = %v, want usage", err)
	}
}

func TestRunCommandPromptSourceIsExclusive(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)
	r.runHarness = harnessfake.New("opencode")
	workspace := t.TempDir()
	path := filepath.Join(t.TempDir(), "p.md")
	if err := os.WriteFile(path, []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := r.runErr("run", taskID, "--prompt-file", path, "--prompt-artifact", "doc-x",
		"--backend", "fake", "--harness", "fake", "--workspace", workspace)
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("both prompt sources error = %v, want usage", err)
	}
}

func TestRunCommandPromptOverrideRejectsGoal(t *testing.T) {
	r := newRunner(t)
	_, _, goal := loopContext(t, r)
	r.runHarness = harnessfake.New("opencode")
	path := filepath.Join(t.TempDir(), "p.md")
	if err := os.WriteFile(path, []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := r.runErr("run", "--goal", goal, "--prompt-file", path,
		"--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("--goal with a prompt override error = %v, want usage", err)
	}
}
