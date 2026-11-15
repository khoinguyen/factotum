package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
)

// runArtifactError reports why the file a run's prompt asked the agent to
// create is not in the checkout. A harness that exits 0 having written nothing
// still moves the task to review and prints a clean run, so the artifact is the
// only evidence the agent did the work.
func runArtifactError(checkout, name, want string) error {
	path := filepath.Join(checkout, name)
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("agent did not create %s in the checkout %s: %w", name, checkout, err)
	}
	if got := strings.TrimSpace(string(body)); !strings.EqualFold(got, want) {
		return fmt.Errorf("%s in the checkout %s contains %q, want %q", name, checkout, got, want)
	}
	return nil
}

// writingRunBackend is the fake isolation backend `ft run` drives here. It hands
// every Exec command to onExec, so a test can stage the artifact a real agent
// would have written in the workdir it was given.
type writingRunBackend struct {
	*isofake.Backend
	onExec func(isolation.Command)
}

func (b writingRunBackend) Exec(ctx context.Context, h isolation.Handle, cmd isolation.Command) (isolation.Execution, error) {
	if b.onExec != nil {
		b.onExec(cmd)
	}
	return b.Backend.Exec(ctx, h, cmd)
}

// TestRunSmokeRequiresTheAgentArtifact pins the assertion the real-harness smoke
// makes, hermetically: a run whose agent writes the file the prompt asked for
// passes; a harness that exits 0 having written nothing, or the wrong thing,
// fails even though the run finished cleanly and the task reached
// ready_for_review.
func TestRunSmokeRequiresTheAgentArtifact(t *testing.T) {
	tests := []struct {
		name          string
		writes        bool
		body          string
		wantDelivered bool
	}{
		{name: "agent writes the file", writes: true, body: "done\n", wantDelivered: true},
		{name: "agent writes the wrong content", writes: true, body: "not done yet\n"},
		{name: "harness exits zero without writing"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(t)
			r.getenv = hermeticGetenv

			checkout := t.TempDir()
			projectID := firstField(t, r.run("project", "create", "Run Smoke",
				"--repo", "name=repo,path="+checkout))
			taskID := firstField(t, r.run("task", "create", "-p", projectID, "-r", "repo",
				"-t", "Create DONE.md",
				"-b", "Create a file named DONE.md in the working directory containing exactly the line: done"))

			base := isofake.New("sandbox")
			base.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
			r.runBackend = writingRunBackend{Backend: base, onExec: func(cmd isolation.Command) {
				if tc.writes {
					mustWrite(t, filepath.Join(cmd.Workdir, "DONE.md"), tc.body)
				}
			}}
			r.runHarness = harnessfake.New("opencode")

			out := r.run("run", taskID, "--sandbox", "fake", "--harness", "fake",
				"--workspace", t.TempDir())
			t.Logf("ft run output:\n%s", out)
			if !strings.Contains(out, "status: ready_for_review") {
				t.Fatalf("the fake run did not reach ready_for_review:\n%s", out)
			}

			err := runArtifactError(checkout, "DONE.md", "done")
			if !tc.wantDelivered && err == nil {
				t.Fatalf("%s passed the smoke check", tc.name)
			}
			if tc.wantDelivered && err != nil {
				t.Fatalf("the smoke check rejected the artifact a real agent would have written: %v", err)
			}
		})
	}
}

// TestRealRunSmokeChecksTheAgentArtifact pins the hookup the artifact helper's
// own tests cannot see: that the opt-in real-harness smoke consults
// runArtifactError at all. It drives realRunSmoke through fake adapters with the
// gate satisfied, so a refactor that drops the check turns this red without a
// model call or a network.
func TestRealRunSmokeChecksTheAgentArtifact(t *testing.T) {
	// Satisfy the opt-in gate; the fake harness never reaches a provider.
	t.Setenv(realRunGate, "fake/model")

	r := newRunner(t)
	base := isofake.New("sandbox")
	base.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = writingRunBackend{Backend: base, onExec: func(cmd isolation.Command) {
		mustWrite(t, filepath.Join(cmd.Workdir, "DONE.md"), "done\n")
	}}
	r.runHarness = harnessfake.New("opencode")

	checks := 0
	original := runSmokeArtifactCheck
	runSmokeArtifactCheck = func(checkout, name, want string) error {
		checks++
		return runArtifactError(checkout, name, want)
	}
	t.Cleanup(func() { runSmokeArtifactCheck = original })

	// "sh" stands in for the harness binary the smoke only probes with
	// exec.LookPath; the fake adapters never spawn it.
	realRunSmoke(t, r, "sh", "DONE.md", "--sandbox", "fake", "--harness", "fake")

	if checks != 1 {
		t.Fatalf("realRunSmoke checked the agent artifact %d times, want 1", checks)
	}
}
