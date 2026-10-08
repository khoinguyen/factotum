package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPiRunThroughLocalBackend drives `ft run --harness pi` end to end with a
// stub `pi` on PATH through the local backend, so the harness is exercised
// through the real CLI selection, command build, and output capture without a
// model or network. The extension/hub env reaches pi through the harness Spec
// and Command, covered by the pi package tests.
func TestPiRunThroughLocalBackend(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "pi")
	script := "#!/bin/sh\nprintf 'stub-pi args:%s\\n' \"$*\"\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := newRunner(t)
	r.getenv = os.Getenv

	checkout := t.TempDir()
	project := firstField(t, r.run("project", "create", "Pi Local",
		"--repo", "name=repo,path="+checkout))
	taskID := firstField(t, r.run("task", "create", "-p", project, "-r", "repo",
		"-t", "Do the thing", "-b", "An instruction"))

	workspace := t.TempDir()
	out := r.run("--full", "run", taskID,
		"--sandbox", "local",
		"--harness", "pi",
		"--allow-host",
		"--model", "openai/gpt-5",
		"--workspace", workspace,
	)
	t.Logf("ft run output:\n%s", out)

	if !strings.Contains(out, "run: finished") {
		t.Errorf("run did not finish cleanly:\n%s", out)
	}
	if !strings.Contains(out, "status: ready_for_review") {
		t.Errorf("run did not reach ready_for_review:\n%s", out)
	}
	if !strings.Contains(out, "--approve --print --model openai/gpt-5") {
		t.Errorf("captured output did not come from the pi invocation:\n%s", out)
	}
}

// TestRealPiRunSmoke exercises `ft run` end to end against the real pi harness
// and a live model through the dev-only local host backend. Like the OpenCode
// smoke, it is opt-in so `go test ./...` and CI stay hermetic and offline.
//
// Run it with a real provider/model (the exact form the harness passes to
// `pi --model`):
//
//	FACTOTUM_RUN_SMOKE_PI_MODEL=openai/gpt-5 \
//	  go test -count=1 -run TestRealPiRunSmoke -v ./internal/cli/
//
// The store is a throwaway JSON file and the checkout is a temp directory, so
// the real project database is never touched.
const realPiRunGate = "FACTOTUM_RUN_SMOKE_PI_MODEL"

func TestRealPiRunSmoke(t *testing.T) {
	model := os.Getenv(realPiRunGate)
	if model == "" {
		t.Skipf("set %s to a provider/model to run the real ft run pi smoke", realPiRunGate)
	}
	if _, err := exec.LookPath("pi"); err != nil {
		t.Skipf("pi is not on PATH: %v", err)
	}

	r := newRunner(t)
	r.getenv = os.Getenv

	checkout := t.TempDir()
	project := firstField(t, r.run("project", "create", "Pi Run Smoke",
		"--repo", "name=repo,path="+checkout))
	taskID := firstField(t, r.run("task", "create", "-p", project, "-r", "repo",
		"-t", "Create DONE.md",
		"-b", "Create a file named DONE.md in the working directory containing exactly the line: done"))

	workspace := t.TempDir()
	out := r.run("--full", "run", taskID,
		"--sandbox", "local",
		"--harness", "pi",
		"--allow-host",
		"--model", model,
		"--workspace", workspace,
	)
	t.Logf("ft run output:\n%s", out)

	if !strings.Contains(out, "run: finished") {
		t.Errorf("run did not finish cleanly:\n%s", out)
	}
	if !strings.Contains(out, "status: ready_for_review") {
		t.Errorf("run did not reach ready_for_review:\n%s", out)
	}
	if got := r.run("task", "get", taskID); !strings.Contains(got, "(ready_for_review)") {
		t.Errorf("task was not moved to ready_for_review:\n%s", got)
	}
}
