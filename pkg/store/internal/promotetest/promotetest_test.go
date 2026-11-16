package promotetest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/store"
)

// The two scripts below stand in for a promotion child. Only a shell process can
// be made to miss its ready signal on demand, and a child that never signals is
// exactly the shape a loaded machine produced once: its siblings opened their
// handles in under 200ms, then one process did not for a full minute.
const (
	signalsReadyThenPromotes = `
: > "$1"
while [ ! -e "$2/release" ]; do sleep 0.01; done
printf '{"id":"%s","created":true,"err":""}' "$3" > "$2/result-$3.json"
`
	neverSignals = `exec sleep 30`
)

// stubChild is a starter that runs script as the child, handing it its ready
// path, the shared directory and the task id.
func stubChild(script string) starter {
	return func(dir, path string, job Job, releaseBy time.Time) (*exec.Cmd, *bytes.Buffer, error) {
		output := &bytes.Buffer{}
		cmd := exec.Command("/bin/sh", "-c", script, "sh",
			filepath.Join(dir, "ready-"+string(job.Task)), dir, string(job.Task))
		cmd.Stdout, cmd.Stderr = output, output
		if err := cmd.Start(); err != nil {
			return nil, output, err
		}
		return cmd, output, nil
	}
}

// TestPromoteReplacesAChildThatNeverSignals is the regression guard for the
// flake: a child that does not open its handle is replaced, and the promotions
// still run and report. The old harness waited out its whole bound on that one
// child and failed the test.
func TestPromoteReplacesAChildThatNeverSignals(t *testing.T) {
	var stalled *exec.Cmd
	starts := 0
	working := stubChild(signalsReadyThenPromotes)
	start := func(dir, path string, job Job, releaseBy time.Time) (*exec.Cmd, *bytes.Buffer, error) {
		starts++
		if starts == 1 {
			cmd, output, err := stubChild(neverSignals)(dir, path, job, releaseBy)
			stalled = cmd
			return cmd, output, err
		}
		return working(dir, path, job, releaseBy)
	}

	results := promoteWith(t, start, t.TempDir(),
		[]Job{{Capture: "cap-1", Task: "t-1"}}, nil, 200*time.Millisecond, 10*time.Second)

	if starts != 2 {
		t.Fatalf("started %d children, want a stalled one and its replacement", starts)
	}
	if stalled == nil || stalled.ProcessState == nil {
		t.Fatalf("the stalled child was not killed and reaped; ProcessState = %v", stalled)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Err != "" || results[0].ID != "t-1" || !results[0].Created {
		t.Fatalf("promotion after the replacement = %+v, want created t-1 with no error", results[0])
	}
}

// TestStartReadyStopsAtThePhaseBudget keeps the replacement loop honest: when no
// child can open a handle at all, it gives up at the shared budget with the
// attempts it made, rather than retrying forever.
func TestStartReadyStopsAtThePhaseBudget(t *testing.T) {
	dir := t.TempDir()
	begin := time.Now()
	_, _, attempts, err := startReady(stubChild(neverSignals), dir, t.TempDir(),
		Job{Capture: "cap-1", Task: "t-1"}, begin.Add(time.Minute), 50*time.Millisecond, begin.Add(300*time.Millisecond))
	if err == nil {
		t.Fatal("startReady reported a ready child although none ever signalled")
	}
	if attempts < 2 {
		t.Fatalf("started %d child(ren) before giving up; a miss must be replaced", attempts)
	}
	if elapsed := time.Since(begin); elapsed > 3*time.Second {
		t.Fatalf("gave up after %s, want at most the 300ms budget plus one bound", elapsed)
	}
	if !strings.Contains(err.Error(), "phase budget") {
		t.Fatalf("error = %v; it must name the budget that ran out", err)
	}
}

// TestWaitForFileHonoursItsDeadline pins the one polling primitive: it finds a
// file that is already there and returns at its deadline, not at some built-in
// bound of its own.
func TestWaitForFileHonoursItsDeadline(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present")
	if err := os.WriteFile(present, nil, 0o644); err != nil {
		t.Fatalf("write %s: %v", present, err)
	}
	if !waitForFile(present, time.Now().Add(time.Second)) {
		t.Fatalf("waitForFile(%s) = false, want true", present)
	}

	begin := time.Now()
	if waitForFile(filepath.Join(dir, "absent"), begin.Add(40*time.Millisecond)) {
		t.Fatal("waitForFile reported a file that does not exist")
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Fatalf("waitForFile waited %s past its 40ms deadline", elapsed)
	}
}

// TestReleaseDeadlineIsTheParents pins the single clock: a child waits for the
// release until the parent's deadline, so an early child cannot time out while
// the parent is still starting the children after it.
func TestReleaseDeadlineIsTheParents(t *testing.T) {
	want := time.Now().UTC().Add(90 * time.Second)
	t.Setenv(releaseByEnv, want.Format(time.RFC3339Nano))
	if got := releaseDeadline(); !got.Equal(want) {
		t.Fatalf("releaseDeadline() = %s, want the parent's %s", got, want)
	}

	t.Setenv(releaseByEnv, "not a timestamp")
	fallback := releaseDeadline()
	if wantFallback := time.Now().Add(startBudget + releaseSlack); fallback.Before(wantFallback.Add(-time.Minute)) || fallback.After(wantFallback.Add(time.Minute)) {
		t.Fatalf("releaseDeadline() without a stamp = %s, want about %s", fallback, wantFallback)
	}
}

// TestBoundsKeepHeadroomOverTheMeasuredShape sizes the two bounds against what
// the harness was measured to need. Children started in 8-18ms idle and tailed at
// 163ms over 132 of them under two concurrent 'go test -race ./...' suites; with
// them started one at a time the 20-caller phase took 1.2s idle and 1.6s at load
// average 35, while the reported flake stalled one child past a minute.
func TestBoundsKeepHeadroomOverTheMeasuredShape(t *testing.T) {
	const (
		measuredChildTail = 163 * time.Millisecond
		measuredPhase     = 1600 * time.Millisecond
	)
	if childStartBound < 20*measuredChildTail {
		t.Fatalf("childStartBound = %s, want at least 20x the measured child tail (%s): a slow child must not be mistaken for a stalled one",
			childStartBound, measuredChildTail)
	}
	if childStartBound >= startBudget {
		t.Fatalf("childStartBound = %s leaves no room inside the %s phase budget for a replacement", childStartBound, startBudget)
	}
	if startBudget < 10*measuredPhase {
		t.Fatalf("startBudget = %s, want at least 10x the measured loaded phase (%s): the budget is for a broken machine, not a loaded one",
			startBudget, measuredPhase)
	}
}

// TestChildGivesUpAtTheParentsDeadline wires the child half end to end: with a
// release stamp already past, the child stops waiting and reports the deadline
// rather than polling for its fallback bound.
func TestChildGivesUpAtTheParentsDeadline(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(childEnv, "1")
	t.Setenv(dirEnv, dir)
	t.Setenv(pathEnv, filepath.Join(dir, "unused"))
	t.Setenv(jobEnv, `{"capture":"cap-1","task":"t-1"}`)
	t.Setenv(releaseByEnv, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano))

	open := func(_ context.Context, _ string) (store.Backend, error) { return nil, fmt.Errorf("no store") }
	begin := time.Now()
	if ran, code := Child(open); !ran || code != 0 {
		t.Fatalf("Child() = (%t, %d), want (true, 0)", ran, code)
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Fatalf("the child waited %s past a deadline that had already passed", elapsed)
	}
	data, err := os.ReadFile(filepath.Join(dir, "result-t-1.json"))
	if err != nil {
		t.Fatalf("the child wrote no result: %v", err)
	}
	var result Result
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode result %s: %v", data, err)
	}
	if !strings.Contains(result.Err, "deadline") {
		t.Fatalf("child reported %q, want the parent's missed deadline", result.Err)
	}
}
