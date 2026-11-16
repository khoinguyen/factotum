// Package promotetest drives the store's promotion guard from separate OS
// processes. Concurrent goroutines share one backend handle, so they prove only
// a process-local lock; only real processes can prove the cross-process claim
// the file backends' advisory lock buys.
//
// A package wires it in twice: TestMain re-executes the test binary as a
// promotion child, and a test calls one of the scenarios, which starts one child
// process per promotion — one at a time, so the machine never has a burst of
// forks in flight (the contention under test is at promotion, not at start) —
// and releases them together.
package promotetest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// Open opens the backend under test at path. The child and the parent use the
// same opener, so both exercise the backend's real Open and snapshot.
type Open func(ctx context.Context, path string) (store.Backend, error)

const (
	childEnv     = "FACTOTUM_PROMOTETEST_CHILD"
	dirEnv       = "FACTOTUM_PROMOTETEST_DIR"
	pathEnv      = "FACTOTUM_PROMOTETEST_PATH"
	jobEnv       = "FACTOTUM_PROMOTETEST_JOB"
	releaseByEnv = "FACTOTUM_PROMOTETEST_RELEASE_BY"

	// childStartBound bounds one child's start: fork, exec, open its handle and
	// signal ready. Children opened their handles in 8-18ms idle and tailed at
	// 163ms over 132 of them under two concurrent 'go test -race ./...' suites.
	// A child that misses this bound stalled (the reported flake sat on one for
	// over a minute while its siblings started in under 200ms), so the parent
	// replaces it rather than waiting out a bound this generous.
	childStartBound = 10 * time.Second

	// startBudget bounds the whole readiness phase, which draws on it across
	// every child and every replacement. It is the machine-is-broken backstop,
	// not the expected cost: the 20-caller phase measured 1.2s idle and 1.6s at
	// load average 35.
	startBudget = 60 * time.Second

	// releaseSlack is how long past startBudget a child keeps waiting for the
	// release. It only matters when the parent died mid-phase: without it an
	// orphaned child would poll forever.
	releaseSlack = time.Minute
)

// Job is one child process's promotion: capture is promoted into task.
type Job struct {
	Capture core.TicketID `json:"capture"`
	Task    core.TicketID `json:"task"`
}

// Result is what one child's promotion observed. Err is the error string, empty
// on success.
type Result struct {
	ID      core.TicketID `json:"id"`
	Created bool          `json:"created"`
	Err     string        `json:"err"`
}

// Child runs the test binary as a promotion child when the parent re-executed
// it, reporting whether it did and the exit code for TestMain. A package wires
// it in before running any test:
//
//	func TestMain(m *testing.M) {
//		if ran, code := promotetest.Child(open); ran {
//			os.Exit(code)
//		}
//		os.Exit(m.Run())
//	}
func Child(open Open) (ran bool, code int) {
	if os.Getenv(childEnv) == "" {
		return false, 0
	}
	child(open)
	return true, 0
}

// child opens the store, signals ready and promotes only after the parent
// releases it. Opening before the signal matters: the parent writes through
// another handle while the children wait, so each promotion runs on a handle
// whose snapshot deliberately predates that write.
func child(open Open) {
	dir := os.Getenv(dirEnv)
	var job Job
	if err := json.Unmarshal([]byte(os.Getenv(jobEnv)), &job); err != nil {
		writeResult(dir, "", Result{Err: "decode job: " + err.Error()})
		return
	}
	backend, openErr := open(context.Background(), os.Getenv(pathEnv))
	if err := os.WriteFile(filepath.Join(dir, "ready-"+string(job.Task)), nil, 0o644); err != nil {
		writeResult(dir, job.Task, Result{Err: "signal ready: " + err.Error()})
		return
	}
	if !waitForFile(filepath.Join(dir, "release"), releaseDeadline()) {
		writeResult(dir, job.Task, Result{Err: "the parent did not release the promotions before its deadline"})
		return
	}
	result := Result{}
	if openErr != nil {
		result.Err = openErr.Error()
	} else {
		promoted, created, err := backend.Tickets().CreatePromoted(context.Background(), job.Capture, candidate(job))
		switch {
		case err != nil:
			result.Err = err.Error()
		case promoted == nil:
			result.Err = "CreatePromoted returned no task"
		default:
			result.ID, result.Created = promoted.ID, created
		}
	}
	writeResult(dir, job.Task, result)
}

// candidate builds the task a promotion stores: a task-kind ticket whose origin
// edge declares the capture (the shape store.ValidatePromotion accepts).
func candidate(job Job) *core.Ticket {
	return &core.Ticket{
		ID:        job.Task,
		ProjectID: "prj-1",
		Kind:      core.KindTask,
		Title:     "promoted " + string(job.Task),
		Status:    core.StatusTodo,
		Deps:      []core.TicketID{job.Capture},
	}
}

// writeResult hands one child's outcome to the parent. A failure here leaves
// the result missing, which the parent reports; the child has nothing else to
// do with the error.
func writeResult(dir string, task core.TicketID, result Result) {
	data, err := json.Marshal(result)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "result-"+string(task)+".json"), data, 0o644)
}

// releaseDeadline is when this child gives up waiting for the parent's release.
// The parent stamps it from the one clock the whole readiness phase draws on, so
// an early child cannot run out of time while the parent is still starting (and
// replacing) the children after it.
func releaseDeadline() time.Time {
	if deadline, err := time.Parse(time.RFC3339Nano, os.Getenv(releaseByEnv)); err == nil {
		return deadline
	}
	return time.Now().Add(startBudget + releaseSlack)
}

// waitForFile reports whether path exists before deadline. Polling backs off to
// 10ms: every waiting child polls, and a millisecond loop across 20 of them would
// burn a slice of the machine the test is trying to measure.
func waitForFile(path string, deadline time.Time) bool {
	for delay := time.Millisecond; ; {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(delay)
		if delay < 10*time.Millisecond {
			delay *= 2
		}
	}
}

// starter starts one child for job, returning its handle and captured output.
type starter func(dir, path string, job Job, releaseBy time.Time) (*exec.Cmd, *bytes.Buffer, error)

// promote starts one process per job, waits until every child has opened its
// handle, runs beforeRelease, releases them together and returns each child's
// result in job order.
//
// Children start one at a time: a burst of forks only adds scheduling pressure
// to a machine already running the rest of the suite, and the cross-process
// claim is made when the children promote together after the release, not when
// they start. All of them still hold a handle opened before the release.
func promote(t *testing.T, path string, jobs []Job, beforeRelease func(t *testing.T)) []Result {
	return promoteWith(t, startChild, path, jobs, beforeRelease, childStartBound, startBudget)
}

// promoteWith is promote with its starter and bounds injected, so the tests
// here can drive a child that never signals without waiting out the real bound.
func promoteWith(t *testing.T, start starter, path string, jobs []Job, beforeRelease func(t *testing.T), bound, budget time.Duration) []Result {
	t.Helper()
	dir := t.TempDir()
	outputs := make([]*bytes.Buffer, len(jobs))
	cmds := make([]*exec.Cmd, len(jobs))
	waited := make([]bool, len(jobs))
	t.Cleanup(func() {
		for i, cmd := range cmds {
			if cmd != nil && !waited[i] {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		}
	})
	deadline := time.Now().Add(budget)
	releaseBy := deadline.Add(releaseSlack)
	for i, job := range jobs {
		cmd, output, attempts, err := startReady(start, dir, path, job, releaseBy, bound, deadline)
		outputs[i] = output
		if err != nil {
			t.Fatalf("promotion %d of %s: %v", i, job.Capture, err)
		}
		if attempts > 1 {
			t.Logf("promotion %d of %s: replaced %d stalled child(ren) before one opened its handle",
				i, job.Capture, attempts-1)
		}
		cmds[i] = cmd
	}
	if beforeRelease != nil {
		beforeRelease(t)
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o644); err != nil {
		t.Fatalf("release promotions: %v", err)
	}
	results := make([]Result, len(jobs))
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("promotion %d of %s failed: %v; output:\n%s", i, jobs[i].Capture, err, outputText(outputs[i]))
		}
		waited[i] = true
		data, err := os.ReadFile(filepath.Join(dir, "result-"+string(jobs[i].Task)+".json"))
		if err != nil {
			t.Fatalf("promotion %d of %s wrote no result: %v", i, jobs[i].Capture, err)
		}
		if err := json.Unmarshal(data, &results[i]); err != nil {
			t.Fatalf("promotion %d of %s: decode result: %v", i, jobs[i].Capture, err)
		}
	}
	return results
}

// startReady starts a child and waits for it to open its handle, replacing one
// that misses bound so a stalled fork costs a replacement rather than the test.
// Every attempt draws on the same deadline: a machine that cannot start a
// process at all fails within the phase budget instead of retrying forever. It
// returns how many children it started.
func startReady(start starter, dir, path string, job Job, releaseBy time.Time, bound time.Duration, deadline time.Time) (*exec.Cmd, *bytes.Buffer, int, error) {
	ready := filepath.Join(dir, "ready-"+string(job.Task))
	for attempts := 1; ; attempts++ {
		cmd, output, err := start(dir, path, job, releaseBy)
		if err != nil {
			return nil, output, attempts, fmt.Errorf("start a child after %d attempt(s): %w", attempts, err)
		}
		until := time.Now().Add(bound)
		if until.After(deadline) {
			until = deadline
		}
		if waitForFile(ready, until) {
			return cmd, output, attempts, nil
		}
		// Cmd.Wait, not Process.Wait: it reaps the child and drains the pipe
		// that os/exec is still copying its output through, so the output can be
		// read afterwards (Process.Wait leaves that copy running: a data race).
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if !time.Now().Before(deadline) {
			return nil, output, attempts, fmt.Errorf(
				"no child opened its handle before the phase budget ran out at %s: %d attempt(s), each given %s; output:\n%s",
				deadline.Format(time.RFC3339Nano), attempts, bound, outputText(output))
		}
	}
}

// outputText is a started child's captured output, or "" when it never got far
// enough to produce one.
func outputText(output *bytes.Buffer) string {
	if output == nil {
		return ""
	}
	return output.String()
}

// startChild re-executes this test binary as a promotion child for job.
func startChild(dir, path string, job Job, releaseBy time.Time) (*exec.Cmd, *bytes.Buffer, error) {
	encoded, err := json.Marshal(job)
	if err != nil {
		return nil, nil, fmt.Errorf("encode promotion of %s into %s: %w", job.Capture, job.Task, err)
	}
	output := &bytes.Buffer{}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(),
		childEnv+"=1",
		dirEnv+"="+dir,
		pathEnv+"="+path,
		jobEnv+"="+string(encoded),
		releaseByEnv+"="+releaseBy.Format(time.RFC3339Nano),
	)
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		return nil, output, err
	}
	return cmd, output, nil
}

// DistinctCaptures promotes a different capture from every process at once and
// requires all of them to survive: the store keeps every promoted task and hands
// each caller an id it can still read. A whole-document rewrite from the
// snapshot a handle loaded at open loses most of them.
func DistinctCaptures(t *testing.T, open Open, path string) {
	t.Helper()
	ctx := context.Background()
	seed, err := open(ctx, path)
	if err != nil {
		t.Fatalf("open seed handle: %v", err)
	}
	defer func() { _ = seed.Close() }()

	const callers = 20
	jobs := make([]Job, callers)
	for i := range jobs {
		capture := core.TicketID(fmt.Sprintf("cap-%02d", i))
		if err := seed.Tickets().Create(ctx, &core.Ticket{
			ID:        capture,
			ProjectID: "prj-1",
			Kind:      core.KindIdea,
			Title:     "idea " + string(capture),
			Status:    core.StatusTodo,
		}); err != nil {
			t.Fatalf("create capture %s: %v", capture, err)
		}
		jobs[i] = Job{Capture: capture, Task: core.TicketID(fmt.Sprintf("t-%02d", i))}
	}

	results := promote(t, path, jobs, nil)

	fresh, err := open(ctx, path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = fresh.Close() }()
	for i, result := range results {
		if result.Err != "" {
			t.Fatalf("promotion %d of %s error = %s", i, jobs[i].Capture, result.Err)
		}
		if !result.Created {
			t.Fatalf("promotion %d of %s reported created=false; a distinct capture's first promotion creates", i, jobs[i].Capture)
		}
		if result.ID != jobs[i].Task {
			t.Fatalf("promotion %d returned %s, want its own %s", i, result.ID, jobs[i].Task)
		}
		if _, err := fresh.Tickets().Get(ctx, result.ID); err != nil {
			t.Fatalf("promotion %d was handed %s, which the store does not have: %v", i, result.ID, err)
		}
	}
	kind := core.KindTask
	tasks, err := fresh.Tickets().List(ctx, store.TicketFilter{ProjectID: "prj-1", Kind: &kind})
	if err != nil {
		t.Fatalf("list promoted tasks: %v", err)
	}
	if len(tasks) != callers {
		t.Fatalf("store holds %d promoted tasks, want %d: %v", len(tasks), callers, ticketIDs(tasks))
	}
}

// SameCapture promotes one capture from every process at once and requires the
// guard to hold across processes: exactly one caller creates the task, every
// caller is handed that same id, the store keeps one task and one origin edge,
// and no loser's candidate id exists.
func SameCapture(t *testing.T, open Open, path string) {
	t.Helper()
	ctx := context.Background()
	seed, err := open(ctx, path)
	if err != nil {
		t.Fatalf("open seed handle: %v", err)
	}
	defer func() { _ = seed.Close() }()

	capture := &core.Ticket{ID: "cap-1", ProjectID: "prj-1", Kind: core.KindBug, Title: "one defect", Status: core.StatusTodo}
	if err := seed.Tickets().Create(ctx, capture); err != nil {
		t.Fatalf("create capture: %v", err)
	}
	const callers = 12
	jobs := make([]Job, callers)
	for i := range jobs {
		jobs[i] = Job{Capture: capture.ID, Task: core.TicketID(fmt.Sprintf("t-c%02d", i))}
	}

	results := promote(t, path, jobs, nil)

	fresh, err := open(ctx, path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = fresh.Close() }()

	winners := 0
	for i, result := range results {
		if result.Err != "" {
			t.Fatalf("promotion %d error = %s", i, result.Err)
		}
		if result.ID != results[0].ID {
			t.Fatalf("promotion %d returned %s, want every caller to get %s", i, result.ID, results[0].ID)
		}
		if result.Created {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("%d promotions reported created; want exactly one", winners)
	}
	won := results[0].ID
	if _, err := fresh.Tickets().Get(ctx, won); err != nil {
		t.Fatalf("every caller was handed %s, which the store does not have: %v", won, err)
	}
	dependencies := capture.ID
	dependents, err := fresh.Tickets().List(ctx, store.TicketFilter{ProjectID: "prj-1", DependsOn: &dependencies})
	if err != nil {
		t.Fatalf("list capture dependents: %v", err)
	}
	if len(dependents) != 1 || dependents[0].ID != won {
		t.Fatalf("capture depends on %v, want exactly %s", ticketIDs(dependents), won)
	}
	for i, result := range results {
		if result.ID == jobs[i].Task {
			continue
		}
		if _, err := fresh.Tickets().Get(ctx, jobs[i].Task); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("loser %d stored %s; want only %s", i, jobs[i].Task, won)
		}
	}
}

// KeepsAnotherWritersWrite promotes through handles whose snapshot predates a
// write made while they waited, and requires that write to survive: a promotion
// must apply to the stored document, never rewrite the stale snapshot over it.
func KeepsAnotherWritersWrite(t *testing.T, open Open, path string) {
	t.Helper()
	ctx := context.Background()
	seed, err := open(ctx, path)
	if err != nil {
		t.Fatalf("open seed handle: %v", err)
	}
	defer func() { _ = seed.Close() }()

	capture := &core.Ticket{ID: "cap-1", ProjectID: "prj-1", Kind: core.KindIdea, Title: "an idea", Status: core.StatusTodo}
	if err := seed.Tickets().Create(ctx, capture); err != nil {
		t.Fatalf("create capture: %v", err)
	}
	now := time.Now().UTC()
	message := &core.Message{
		ID:        "msg-1",
		ProjectID: "prj-1",
		To:        core.ActorAddress("claude"),
		Body:      "handoff",
		State:     core.MessageQueued,
		CreatedAt: now,
		UpdatedAt: now,
	}
	jobs := []Job{{Capture: capture.ID, Task: "t-promoted"}}
	results := promote(t, path, jobs, func(t *testing.T) {
		t.Helper()
		writer, err := open(ctx, path)
		if err != nil {
			t.Fatalf("open concurrent writer: %v", err)
		}
		defer func() { _ = writer.Close() }()
		if err := writer.Messages().Create(ctx, message); err != nil {
			t.Fatalf("concurrent writer Messages().Create() error = %v", err)
		}
	})
	if results[0].Err != "" {
		t.Fatalf("promotion error = %s", results[0].Err)
	}

	fresh, err := open(ctx, path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = fresh.Close() }()
	if _, err := fresh.Messages().Get(ctx, message.ID); err != nil {
		t.Fatalf("the promotion erased the concurrent writer's message %s: %v", message.ID, err)
	}
	if _, err := fresh.Tickets().Get(ctx, capture.ID); err != nil {
		t.Fatalf("the promotion erased the capture it was promoted from: %v", err)
	}
	if _, err := fresh.Tickets().Get(ctx, results[0].ID); err != nil {
		t.Fatalf("the promotion was handed %s, which the store does not have: %v", results[0].ID, err)
	}
}

func ticketIDs(tickets []*core.Ticket) []core.TicketID {
	ids := make([]core.TicketID, len(tickets))
	for i, ticket := range tickets {
		ids[i] = ticket.ID
	}
	return ids
}
