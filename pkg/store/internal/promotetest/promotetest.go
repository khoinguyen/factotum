// Package promotetest drives the store's promotion guard from separate OS
// processes. Concurrent goroutines share one backend handle, so they prove only
// a process-local lock; only real processes can prove the cross-process claim
// the file backends' advisory lock buys.
//
// A package wires it in twice: TestMain re-executes the test binary as a
// promotion child, and a test calls one of the scenarios, which starts one child
// process per promotion — one at a time, so the machine never has a burst of
// forks in flight (the contention under test is at promotion, not at start) —
// releases them together, and then starts every promotion in one write.
//
// A same-capture wave is also run with the parent holding the store as a reader,
// and that is what makes the wave a proof rather than a hope. A promotion's
// lookup and insert take the store's exclusive lock, so a writer queues behind a
// reader: holding it across the release puts every child at its promotion —
// queued at the store's begin() — before any of them is let in, and a promotion
// that takes the shared lock instead runs straight through the reader and
// finishes inside the hold. That is the mutation, and it is the one thing timing
// cannot show: whole-call spans overlap even when a straggler reaches its stamp
// check after the winner's write, so the wave looks raced while the race never
// ran. (Starting the children together was tried first and caught the mutation
// only 6 runs in 10, when a polled go-file woke them up to 10ms apart.)
//
// Holding the *exclusive* lock as the start instead was measured and rejected:
// the host wakes flock's queued waiters one at a time — a probe saw twelve of
// them granted 0.5ms apart, the first 5ms after the release — which serialized
// the wave so thoroughly that the shared-lock mutation survived 10 runs in 10.
// The harness would have certified the OS's queue as the guard.
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

// ReadLock takes the store's shared cross-process lock at path — the lock its
// reads hold — and returns its release. Holding it is how the harness tells a
// promotion that takes the exclusive lock (it queues, as a writer must) from one
// that takes the shared lock (it runs straight through): a reader lock never
// excludes another reader.
type ReadLock func(path string) (func(), error)

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

	// readerHeld is how long the parent holds the store as a reader while a
	// wave proves that promotions are writers. A promotion that takes the
	// shared lock finishes inside it — the twelve-caller wave measured 70-89µs
	// from the last door at load average 56 — and a promotion that takes the
	// exclusive lock waits the whole hold out, which is what the clean tree
	// pays to prove it. Like startBudget it is sized for a broken machine, not
	// a loaded one.
	readerHeld = 500 * time.Millisecond
)

// Job is one child process's promotion: capture is promoted into task.
type Job struct {
	Capture core.TicketID `json:"capture"`
	Task    core.TicketID `json:"task"`
}

// Result is what one child's promotion observed. Err is the error string, empty
// on success. Start and End bracket the CreatePromoted call on the host clock,
// so the parent can tell whether any two promotions were ever in flight
// together: a wave that never overlapped proves nothing about the guard.
type Result struct {
	ID      core.TicketID `json:"id"`
	Created bool          `json:"created"`
	Err     string        `json:"err"`
	Start   int64         `json:"start"`
	End     int64         `json:"end"`
}

// overlapped reports whether any two promotions were in flight at the same
// time. Touching spans (one ending exactly as the other starts) do not count.
func overlapped(results []Result) bool {
	for i := range results {
		for j := i + 1; j < len(results); j++ {
			if results[i].Start < results[j].End && results[j].Start < results[i].End {
				return true
			}
		}
	}
	return false
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

// child opens the store, signals ready and promotes only after the parent has
// released the promotions, which it does with the store held as a reader.
// Opening before the signal matters: the parent writes through another handle
// while the children wait, so each promotion runs on a handle whose snapshot
// deliberately predates that write.
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
	if err := os.WriteFile(filepath.Join(dir, "door-"+string(job.Task)), nil, 0o644); err != nil {
		writeResult(dir, job.Task, Result{Err: "signal door: " + err.Error()})
		return
	}
	result := Result{}
	if openErr != nil {
		result.Err = openErr.Error()
	} else {
		result.Start = time.Now().UnixNano()
		promoted, created, err := backend.Tickets().CreatePromoted(context.Background(), job.Capture, candidate(job))
		result.End = time.Now().UnixNano()
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
	_ = os.WriteFile(resultPath(dir, task), data, 0o644)
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

// wave is one run of the promotion children: what starts them, what the parent
// holds while they run, and the bounds it judges them by.
type wave struct {
	start starter
	// reader is the store's shared lock, held by the parent for the length of the
	// wave; nil means the wave races on its own. Holding it queues every child
	// that promotes (a writer) at the store's begin(), and lets a promotion that
	// takes the shared lock run.
	reader ReadLock
	// holds is how long the parent keeps the reader while it waits for the
	// promotions to prove they were not queued behind it.
	holds time.Duration
	// bound is how long one child may take to open its handle.
	bound time.Duration
	// budget bounds the readiness phase: the machine-is-broken backstop.
	budget time.Duration
}

// promote runs the wave the way the scenarios do: real processes, the harness's
// bounds, and the store held as a reader when reader is not nil. It returns each
// child's result in job order and how many promotions finished while the store
// was held as a reader — 0 when reader is nil, because nothing was held.
func promote(t *testing.T, path string, jobs []Job, reader ReadLock, beforeRelease func(t *testing.T)) ([]Result, int) {
	return promoteWith(t, wave{start: startChild, reader: reader, holds: readerHeld, bound: childStartBound, budget: startBudget}, path, jobs, beforeRelease)
}

// promoteWith is promote with the wave injected, so the tests here can drive a
// child that never signals, or a hold that ends early, without waiting out the
// real bounds.
//
// Children start one at a time: a burst of forks only adds scheduling pressure
// to a machine already running the rest of the suite, and the cross-process
// claim is made when the children promote together after the release, not when
// they start. All of them still hold a handle opened before the release.
//
// The start itself is one write to a file every child is waiting on, so they
// enter CreatePromoted within microseconds of each other. What makes that a race
// on the store is the reader lock: the parent takes it before the release and
// drops it only after every child has reached its promotion, so a promotion that
// takes the exclusive lock cannot run its stamp check until all of its siblings
// are waiting at the same place.
func promoteWith(t *testing.T, w wave, path string, jobs []Job, beforeRelease func(t *testing.T)) ([]Result, int) {
	t.Helper()
	if w.reader != nil && w.holds <= 0 {
		t.Fatal("the wave was asked to hold the store as a reader without a bound: a promotion that waits on it would hold the phase forever")
	}
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
	deadline := time.Now().Add(w.budget)
	releaseBy := deadline.Add(releaseSlack)
	for i, job := range jobs {
		cmd, output, attempts, err := startReady(w.start, dir, path, job, releaseBy, w.bound, deadline)
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
	// Held after the parent's own writes, which would otherwise wait on a lock
	// this process never drops until the children are done.
	var releaseRead func()
	if w.reader != nil {
		release, err := w.reader(path)
		if err != nil {
			t.Fatalf("hold the store as a reader at %s: %v", path, err)
		}
		if release == nil {
			release = func() {}
		}
		releaseRead = release
	}
	readHeld := releaseRead != nil
	t.Cleanup(func() {
		if readHeld {
			releaseRead()
		}
	})
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o644); err != nil {
		t.Fatalf("release promotions: %v", err)
	}
	// Every child is now at its promotion: a child that dies on the way there
	// fails the phase by name rather than being silently left out of the start.
	if err := awaitDoors(dir, jobs, deadline); err != nil {
		t.Fatal(err)
	}
	ranWhileRead := 0
	if readHeld {
		// A promotion that finished inside the hold ran past a reader, which only
		// a shared lock allows. A writer is still queued, so the hold runs out:
		// that bound is what the clean tree pays to prove it.
		until := time.Now().Add(w.holds)
		for _, job := range jobs {
			if waitForFile(resultPath(dir, job.Task), until) {
				ranWhileRead++
			}
		}
		releaseRead()
		readHeld = false
	}
	results := make([]Result, len(jobs))
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("promotion %d of %s failed: %v; output:\n%s", i, jobs[i].Capture, err, outputText(outputs[i]))
		}
		waited[i] = true
		data, err := os.ReadFile(resultPath(dir, jobs[i].Task))
		if err != nil {
			t.Fatalf("promotion %d of %s wrote no result: %v", i, jobs[i].Capture, err)
		}
		if err := json.Unmarshal(data, &results[i]); err != nil {
			t.Fatalf("promotion %d of %s: decode result: %v", i, jobs[i].Capture, err)
		}
	}
	return results, ranWhileRead
}

// resultPath is where a child hands the parent its outcome.
func resultPath(dir string, task core.TicketID) string {
	return filepath.Join(dir, "result-"+string(task)+".json")
}

// awaitDoors waits until every child has reached its promotion. A child that
// dies on the way there fails the phase by name rather than being silently left
// out of the start, which would leave the remaining promotions racing a field
// the harness thinks is full.
func awaitDoors(dir string, jobs []Job, deadline time.Time) error {
	for i, job := range jobs {
		if !waitForFile(filepath.Join(dir, "door-"+string(job.Task)), deadline) {
			return fmt.Errorf("promotion %d of %s into %s never reached its promotion before the phase budget ran out at %s",
				i, job.Capture, job.Task, deadline.Format(time.RFC3339Nano))
		}
	}
	return nil
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
		// A child killed as it created the file leaves it behind, and the next
		// attempt would adopt that signal before its own child opened anything.
		// The child is reaped, so only this removal can touch the file now.
		_ = os.Remove(ready)
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

	results, _ := promote(t, path, jobs, nil, nil)

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
//
// The promotions are queued, not just started together: the parent holds the
// store's exclusive lock while every child arrives, so dropping it is the only
// way any of them reaches the stamp check the guard's decision runs under. Each
// child also stamps the start and end of its promotion, which this requires to
// share an instant — supporting evidence that the wave raced at all, but not the
// proof: whole-call spans overlap even when a straggler starts after the winner's
// write, so a run can look overlapped and never have raced the guard.
// The wave also proves the promotion is a writer: the parent holds the store as
// a reader from before the release until every child has reached its promotion,
// so a promotion that takes the store's exclusive lock cannot run — let alone
// reach its stamp check — until all of its siblings are queued at the same place,
// and one that takes the shared lock finishes inside the hold. Whole-call spans
// are kept as supporting evidence that the wave raced, but they are not the
// proof: they overlap even when a straggler starts after the winner's write.
func SameCapture(t *testing.T, open Open, reader ReadLock, path string) {
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

	results, ranWhileRead := promote(t, path, jobs, reader, nil)

	if !overlapped(results) {
		t.Fatalf("no two of the %d promotions were ever in flight together, so this run never raced the guard: %s spanned %d..%d and %s spanned %d..%d (host clock, ns). A host that cannot run two promotions at once cannot test a cross-process one.",
			len(jobs), jobs[0].Task, results[0].Start, results[0].End, jobs[1].Task, results[1].Start, results[1].End)
	}

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
	if ranWhileRead != 0 {
		t.Fatalf("%d of the %d promotions finished while another handle held the store as a reader; a promotion's lookup and insert take the store's exclusive lock, so a reader must block every one of them until it lets go", ranWhileRead, len(jobs))
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
	results, _ := promote(t, path, jobs, nil, func(t *testing.T) {
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
