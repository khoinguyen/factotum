package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
)

// TestRunProgressHeartbeatLine pins the heartbeat text: the run's label and the
// elapsed time, so a silent multi-minute harness is visibly alive.
func TestRunProgressHeartbeatLine(t *testing.T) {
	var buf bytes.Buffer
	p := &runProgress{w: &buf, label: "run t-abc", start: time.Now().Add(-30 * time.Second)}
	p.heartbeat()
	if got := buf.String(); !strings.Contains(got, "run t-abc still running (30s)") {
		t.Fatalf("heartbeat line = %q, want the label and elapsed time", got)
	}
}

func TestRunProgressStopIsNilSafe(t *testing.T) {
	var p *runProgress
	p.stop()
}

// TestRunCommandProgressOnHeadlessTerminal proves a headless run narrates live
// progress on stderr: a start line naming the run, emitted while the harness
// runs, separate from the stdout result.
func TestRunCommandProgressOnHeadlessTerminal(t *testing.T) {
	r := newRunner(t)
	r.isTerminal = func(io.Writer) bool { return true }
	r.stdinTerminal = func(io.Reader) bool { return true }
	_, taskID := runContext(t, r)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	stdout, stderr := r.runSplit("run", taskID, "--unattended", "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !strings.Contains(stderr, "run "+taskID) || !strings.Contains(stderr, "started") {
		t.Fatalf("headless run stderr missing progress:\n%s", stderr)
	}
	if strings.Contains(stdout, "started") {
		t.Fatalf("progress leaked into stdout:\n%s", stdout)
	}
}

// TestRunCommandProgressSuppressed pins when progress stays off: an interactive
// run (the agent owns the terminal), a non-terminal stderr, and machine output
// (-o json|yaml), which must stay clean.
func TestRunCommandProgressSuppressed(t *testing.T) {
	tests := []struct {
		name       string
		terminal   bool
		unattended bool
		format     string
	}{
		{"interactive run owns the terminal", true, false, "text"},
		{"non-terminal stderr", false, true, "text"},
		{"json output", true, true, "json"},
		{"yaml output", true, true, "yaml"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(t)
			r.isTerminal = func(io.Writer) bool { return tc.terminal }
			r.stdinTerminal = func(io.Reader) bool { return tc.terminal }
			_, taskID := runContext(t, r)

			backend := isofake.New("sandbox")
			backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
			r.runBackend = backend
			r.runHarness = harnessfake.New("opencode")

			args := []string{"run", taskID, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir()}
			if tc.unattended {
				args = append(args, "--unattended")
			}
			if tc.format != "text" {
				args = append(args, "-o", tc.format)
			}
			stdout, stderr := r.runSplit(args...)
			if strings.Contains(stderr, "started") {
				t.Fatalf("progress should be suppressed:\n%s", stderr)
			}
			if tc.format == "json" {
				var doc runDoc
				if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
					t.Fatalf("json output is not clean: %v\n%s", err, stdout)
				}
				if doc.TaskID != taskID {
					t.Fatalf("json task_id = %q, want %q", doc.TaskID, taskID)
				}
			}
		})
	}
}

// TestGroomCommandProgressNamesSession proves a headless groom narrates the
// session id on stderr, so the user can follow and later read the session.
func TestGroomCommandProgressNamesSession(t *testing.T) {
	r := newRunner(t)
	r.isTerminal = func(io.Writer) bool { return true }
	projectID, cfgPath := tasklessContext(t, r)
	r.run("idea", "create", "-p", projectID, "-t", "Maybe cache")

	promptPath := t.TempDir() + "/prompt.md"
	mustWrite(t, promptPath, "# Grooming session prompt\n\nYou are the team lead.\n")

	backend := writingGroomBackend(t)
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	_, stderr := r.runSplit("--config", cfgPath, "groom", "-p", projectID, "--prompt-file", promptPath,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !strings.Contains(stderr, "groom session groom-") || !strings.Contains(stderr, "started") {
		t.Fatalf("headless groom stderr missing session progress:\n%s", stderr)
	}
}
