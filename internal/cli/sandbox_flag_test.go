package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/internal/groom"
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
)

// TestRunSandboxFlagSelectsBackend pins that `ft run` selects the isolation
// backend through --sandbox and says nothing about deprecation.
func TestRunSandboxFlagSelectsBackend(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out, errOut := r.runSplit("run", taskID, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !strings.Contains(out, "run: finished") {
		t.Fatalf("run output missing the finish line:\n%s", out)
	}
	if strings.Contains(errOut, "deprecated") {
		t.Fatalf("--sandbox must not print a deprecation notice:\n%s", errOut)
	}
	if got := backend.Prepared(); len(got) != 1 {
		t.Fatalf("Prepare called %d times, want 1", len(got))
	}
}

// TestRunRejectsBackendFlag pins the end of the one-release rename: --backend is
// gone, so a stale invocation fails at flag parsing instead of being silently
// accepted.
func TestRunRejectsBackendFlag(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)

	err := r.runErr("run", taskID, "--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --backend") {
		t.Fatalf("--backend error = %v, want an unknown-flag error", err)
	}
	if shown := r.run("task", "get", taskID); !strings.HasPrefix(shown, "(todo) ") {
		t.Fatalf("a rejected --backend run touched the task:\n%s", shown)
	}
}

// TestGroomSandboxFlagSelectsBackend pins that `ft groom` uses --sandbox as its
// isolation-backend flag and stays quiet about deprecation.
func TestGroomSandboxFlagSelectsBackend(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("task", "create", "-p", projectID, "-t", "Add widget")

	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	mustWrite(t, promptPath, "# Grooming session prompt\n")

	backend := writingGroomBackend(t)
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out, errOut := r.runSplit("--config", cfgPath, "groom", "-p", projectID, "--prompt-file", promptPath,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !strings.Contains(out, "run: finished") {
		t.Fatalf("groom output missing the finish line:\n%s", out)
	}
	if strings.Contains(errOut, "deprecated") {
		t.Fatalf("--sandbox must not print a deprecation notice:\n%s", errOut)
	}
}

// TestGroomRejectsBackendFlag pins the same removal on `ft groom`.
func TestGroomRejectsBackendFlag(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("task", "create", "-p", projectID, "-t", "Add widget")

	err := r.runErr("--config", cfgPath, "groom", "-p", projectID, "--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --backend") {
		t.Fatalf("--backend error = %v, want an unknown-flag error", err)
	}
}

// TestRunAndGroomHelpOfferOnlySandbox pins that both commands expose --sandbox
// and no --backend line, so help-driven users never learn the removed spelling.
func TestRunAndGroomHelpOfferOnlySandbox(t *testing.T) {
	r := newRunner(t)
	for _, command := range []string{"run", "groom"} {
		help, _ := r.runSplit(command, "--help")
		if strings.Contains(help, "--backend") {
			t.Fatalf("%s help must not offer --backend:\n%s", command, help)
		}
		if !strings.Contains(help, "--sandbox") {
			t.Fatalf("%s help is missing --sandbox:\n%s", command, help)
		}
	}
}

// TestRunResolvesSandboxFlagBeforeStoreWork pins the resolution ordering: an
// unknown --sandbox is a flag error reported before the run resolves a prompt
// artifact or touches the task.
func TestRunResolvesSandboxFlagBeforeStoreWork(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)
	r.runHarness = harnessfake.New("opencode")

	err := r.runErr("run", taskID, "--sandbox", "nope", "--harness", "fake",
		"--prompt-artifact", "art-missing", "--workspace", t.TempDir())
	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), `unknown isolation backend "nope"`) {
		t.Fatalf("unknown --sandbox error = %v, want the flag's usage error", err)
	}
	if shown := r.run("task", "get", taskID); !strings.HasPrefix(shown, "(todo) ") {
		t.Fatalf("a run with an invalid --sandbox touched the task:\n%s", shown)
	}
}

// TestGroomResolvesSandboxFlagBeforeSessionWork pins the same ordering on groom:
// the flag is resolved before the session reads its prompt or creates the
// session directory beside the store.
func TestGroomResolvesSandboxFlagBeforeSessionWork(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("idea", "create", "-p", projectID, "-t", "Maybe cache")
	r.runHarness = harnessfake.New("opencode")

	missingPrompt := filepath.Join(t.TempDir(), "missing.md")
	err := r.runErr("--config", cfgPath, "groom", "-p", projectID, "--prompt-file", missingPrompt,
		"--sandbox", "nope", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), `unknown isolation backend "nope"`) {
		t.Fatalf("unknown --sandbox error = %v, want the flag's usage error", err)
	}
	sessions := filepath.Join(filepath.Dir(r.path), groom.SessionsDirName)
	if _, err := os.Stat(sessions); !os.IsNotExist(err) {
		t.Fatalf("groom with an invalid --sandbox created %s", sessions)
	}
}
