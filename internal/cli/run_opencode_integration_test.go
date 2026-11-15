package cli

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/harness/opencode"
)

// These tests exercise `ft run` end to end against the real OpenCode harnesses and
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
// The V2 smoke additionally needs the V2 executable on PATH (the `opencode2`
// alias the npm @opencode/cli package installs; the brew opencode-v2 formula
// links it as `opencode` and conflicts with V1, so the npm alias is the
// coexistence form) and is skipped without it.
//
// The store is a throwaway JSON file and the checkout is a temp directory, so
// the real project database is never touched. The local backend is unsandboxed:
// it runs the host binary in place, selected only for this explicit dev-only
// opt-in.
const realRunGate = "FACTOTUM_RUN_SMOKE_MODEL"

// runSmokeArtifactCheck is the guard the real-run smoke applies to the artifact
// the prompt asked the agent to write. It is a variable so a test can replace it
// with a recorder and prove the smoke consults it; the real smokes leave it at
// runArtifactError.
var runSmokeArtifactCheck = runArtifactError

func TestRealRunSmoke(t *testing.T) {
	realRunSmoke(t, newRunner(t), opencode.DefaultBinary, "DONE.md",
		"--sandbox", "local", "--harness", opencode.Name, "--allow-host")
}

// TestRealRunSmokeV2 pins that the V2 harness (`--harness opencode2`) drives a
// real OpenCode V2 binary end to end: headless `run`, the `--model` flag, and
// completion, against a live model.
func TestRealRunSmokeV2(t *testing.T) {
	realRunSmoke(t, newRunner(t), opencode.DefaultBinaryV2, "DONE2.md",
		"--sandbox", "local", "--harness", opencode.NameV2, "--allow-host")
}

// realRunSmoke drives one `ft run` and asserts it finished, reached
// ready_for_review, and delivered the artifact the prompt asked for. The runner
// and the harness selection are parameters so the hermetic hookup test can drive
// this exact function through fake adapters, watching the artifact check without
// a model or a network.
func realRunSmoke(t *testing.T, r *runner, binary, doneFile string, selection ...string) {
	t.Helper()
	model := os.Getenv(realRunGate)
	if model == "" {
		t.Skipf("set %s to a provider/model to run the real ft run smoke", realRunGate)
	}
	if _, err := exec.LookPath(binary); err != nil {
		t.Skipf("%s is not on PATH: %v", binary, err)
	}
	r.getenv = hermeticGetenv

	// A local checkout the launcher resolves in place (no clone, no network),
	// and one task with an unambiguous instruction.
	checkout := t.TempDir()
	project := firstField(t, r.run("project", "create", "Run Smoke",
		"--repo", "name=repo,path="+checkout))
	taskID := firstField(t, r.run("task", "create", "-p", project, "-r", "repo",
		"-t", "Create "+doneFile,
		"-b", "Create a file named "+doneFile+" in the working directory containing exactly the line: done"))

	workspace := t.TempDir()
	out := r.run(append([]string{
		"--full", "run", taskID,
		"--model", model,
		"--workspace", workspace,
		"--arg=--auto",
	}, selection...)...)
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
	// A clean exit and a reviewed task are not proof the agent worked: a no-op
	// harness reaches both. The artifact it was asked to write is.
	if err := runSmokeArtifactCheck(checkout, doneFile, "done"); err != nil {
		t.Errorf("run did not deliver the agent's work: %v", err)
	}
}
