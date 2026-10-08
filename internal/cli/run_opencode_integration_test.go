package cli

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// These tests exercise `ft run` end to end against the real OpenCode harness and
// a live model through the dev-only local host backend. Like the real-TypeSafe
// grooming smoke, they are opt-in so `go test ./...` and CI stay hermetic and
// offline.
//
// Run them with a real provider/model (the exact form the harness passes to
// `opencode run --model`):
//
//	FACTOTUM_RUN_SMOKE_MODEL=opencode-go/deepseek-v4.1-flash \
//	  go test -count=1 -run TestRealRunSmoke -v ./internal/cli/
//
// The store is a throwaway JSON file and the checkout is a temp directory, so
// the real project database is never touched. The local backend is unsandboxed:
// it runs the host `opencode` binary in place, selected only for this explicit
// dev-only opt-in.
const realRunGate = "FACTOTUM_RUN_SMOKE_MODEL"

func TestRealRunSmoke(t *testing.T) {
	model := os.Getenv(realRunGate)
	if model == "" {
		t.Skipf("set %s to a provider/model to run the real ft run smoke", realRunGate)
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skipf("opencode is not on PATH: %v", err)
	}

	r := newRunner(t)
	r.getenv = hermeticGetenv

	// A local checkout the launcher resolves in place (no clone, no network),
	// and one task with an unambiguous instruction.
	checkout := t.TempDir()
	project := firstField(t, r.run("project", "create", "Run Smoke",
		"--repo", "name=repo,path="+checkout))
	taskID := firstField(t, r.run("task", "create", "-p", project, "-r", "repo",
		"-t", "Create DONE.md",
		"-b", "Create a file named DONE.md in the working directory containing exactly the line: done"))

	workspace := t.TempDir()
	out := r.run("--full", "run", taskID,
		"--sandbox", "local",
		"--harness", "opencode",
		"--allow-host",
		"--model", model,
		"--workspace", workspace,
		"--arg=--auto",
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
