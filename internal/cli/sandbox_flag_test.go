package cli

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
)

// TestRunSandboxFlagSelectsBackend pins that `ft run` selects the isolation
// backend through the primary --sandbox flag, and that the primary flag does not
// print the deprecation notice reserved for the --backend alias.
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

// TestRunBackendFlagAliasWorksAndWarns pins the one-release compatibility path:
// --backend still selects the backend but prints a deprecation hint naming
// --sandbox.
func TestRunBackendFlagAliasWorksAndWarns(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out, errOut := r.runSplit("run", taskID, "--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !strings.Contains(out, "run: finished") {
		t.Fatalf("alias run did not finish:\n%s", out)
	}
	if !strings.Contains(errOut, "deprecated") || !strings.Contains(errOut, "--sandbox") {
		t.Fatalf("--backend alias did not print a deprecation hint naming --sandbox:\n%s", errOut)
	}
}

// TestRunSandboxAndBackendAreExclusive pins that naming the backend twice through
// both spellings is rejected rather than silently picking one.
func TestRunSandboxAndBackendAreExclusive(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)
	r.runHarness = harnessfake.New("opencode")

	err := r.runErr("run", taskID, "--sandbox", "fake", "--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("--sandbox with --backend error = %v, want usage", err)
	}
}

// TestGroomSandboxFlagSelectsBackend pins that `ft groom` uses --sandbox as its
// primary isolation-backend flag and stays quiet about deprecation.
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

// TestGroomBackendFlagAliasWorksAndWarns pins the same one-release alias on
// `ft groom`.
func TestGroomBackendFlagAliasWorksAndWarns(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("task", "create", "-p", projectID, "-t", "Add widget")

	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	mustWrite(t, promptPath, "# Grooming session prompt\n")

	backend := writingGroomBackend(t)
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out, errOut := r.runSplit("--config", cfgPath, "groom", "-p", projectID, "--prompt-file", promptPath,
		"--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !strings.Contains(out, "run: finished") {
		t.Fatalf("alias groom did not finish:\n%s", out)
	}
	if !strings.Contains(errOut, "deprecated") || !strings.Contains(errOut, "--sandbox") {
		t.Fatalf("--backend alias did not print a deprecation hint naming --sandbox:\n%s", errOut)
	}
}

// TestRunBackendFlagAliasIsHidden pins that the deprecated alias is not offered
// in help output, so the primary --sandbox is the discoverable surface.
func TestRunBackendFlagAliasIsHidden(t *testing.T) {
	r := newRunner(t)
	help, _ := r.runSplit("run", "--help")
	if strings.Contains(help, "--backend") {
		t.Fatalf("deprecated --backend alias must be hidden from help:\n%s", help)
	}
	if !strings.Contains(help, "--sandbox") {
		t.Fatalf("primary --sandbox flag missing from help:\n%s", help)
	}
}
