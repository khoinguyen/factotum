package serve

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/memory"
)

// sweepClock is a clock a test moves by hand, so the retention and lease
// boundaries are exact instead of wall-clock dependent. The sweeps read it from
// a goroutine, so it is guarded.
type sweepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *sweepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *sweepClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// sweepFixture is the minimal serve wiring the sweeps need: a memory backend,
// a message service on a hand-driven clock, and the ids the service stamps.
type sweepFixture struct {
	backend  store.Backend
	clock    *sweepClock
	ids      *seqIDs
	messages *app.MessageService
}

func newSweepFixture(t *testing.T) *sweepFixture {
	t.Helper()
	backend := memory.New()
	t.Cleanup(func() { _ = backend.Close() })
	f := &sweepFixture{
		backend: backend,
		clock:   &sweepClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		ids:     &seqIDs{},
	}
	f.messages = app.NewMessageService(backend, f.clock, f.ids)
	return f
}

func (f *sweepFixture) addMessage(t *testing.T, id core.MessageID, state core.MessageState, updatedAt time.Time, leaseUntil time.Time) {
	t.Helper()
	message := &core.Message{
		ID:        id,
		ProjectID: "prj-1",
		To:        core.ActorAddress("act-agent"),
		Body:      "body",
		State:     state,
		Attempts:  1,
		CreatedAt: updatedAt,
		UpdatedAt: updatedAt,
	}
	if state == core.MessageDelivered {
		runID := core.RunID("run-1")
		message.RunID = &runID
		message.LeaseUntil = &leaseUntil
	}
	if err := f.backend.Messages().Create(context.Background(), message); err != nil {
		t.Fatalf("create %s message: %v", id, err)
	}
}

func (f *sweepFixture) state(t *testing.T, id core.MessageID) (core.MessageState, bool) {
	t.Helper()
	message, err := f.backend.Messages().Get(context.Background(), id)
	if err != nil {
		return "", false
	}
	return message.State, true
}

// startSweeps builds a server on the fixture and starts its sweeps.
func (f *sweepFixture) startSweeps(t *testing.T, opts Options) *Server {
	t.Helper()
	opts.Backend = f.backend
	opts.Clock = f.clock
	opts.Messages = f.messages
	opts.Project = "prj-1"
	server, err := New(opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	stop := server.StartSweeps(context.Background())
	t.Cleanup(stop)
	return server
}

// waitForState polls until the message reaches want, failing the test on timeout.
func waitForState(t *testing.T, f *sweepFixture, id core.MessageID, want core.MessageState, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if state, ok := f.state(t, id); ok && state == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	state, ok := f.state(t, id)
	t.Fatalf("message %s state = %q (found %v), want %q within %v", id, state, ok, want, timeout)
}

// TestStartSweepsRequeuesExpiredWithoutRegistration is the acceptance guarantee:
// a running serve recovers a lapsed delivery on its own timer, with no receiver
// registering to trigger it.
func TestStartSweepsRequeuesExpiredWithoutRegistration(t *testing.T) {
	f := newSweepFixture(t)
	f.addMessage(t, "m-stuck", core.MessageDelivered, f.clock.Now().Add(-time.Minute), f.clock.Now().Add(-time.Minute))
	f.startSweeps(t, Options{RequeueInterval: 5 * time.Millisecond, PruneInterval: time.Hour})

	waitForState(t, f, "m-stuck", core.MessageQueued, 2*time.Second)

	actor := core.ActorID("act-agent")
	claimed, err := f.messages.Claim(context.Background(), app.ClaimInput{
		ProjectID: "prj-1",
		RunID:     "run-1",
		ActorID:   &actor,
	})
	if err != nil {
		t.Fatalf("Claim() after the sweep error = %v, want the recovered message", err)
	}
	if claimed.ID != "m-stuck" {
		t.Fatalf("Claim().ID = %q, want %q", claimed.ID, "m-stuck")
	}

	runs, err := f.messages.ListRuns(context.Background(), store.RunFilter{ProjectID: "prj-1"})
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("registered runs = %d, want 0: the timer must recover with no registration", len(runs))
	}
}

// TestStartSweepsUsesServiceClock proves the sweep decides lapsed against the
// injected clock, not the wall clock: a lease ten minutes in the future survives
// repeated sweeps, and the sweep recovers it only once the service clock passes
// the lease.
func TestStartSweepsUsesServiceClock(t *testing.T) {
	f := newSweepFixture(t)
	now := f.clock.Now()
	f.addMessage(t, "m-live", core.MessageDelivered, now, now.Add(10*time.Minute))
	f.startSweeps(t, Options{RequeueInterval: 5 * time.Millisecond, PruneInterval: time.Hour})

	time.Sleep(50 * time.Millisecond)
	if state, _ := f.state(t, "m-live"); state != core.MessageDelivered {
		t.Fatalf("state = %q, want delivered while the service clock is inside the lease", state)
	}

	f.clock.advance(11 * time.Minute)
	waitForState(t, f, "m-live", core.MessageQueued, 2*time.Second)
}

// TestStartSweepsRunsOnceAtStartup proves the sweep runs before Serve starts
// serving: the interval is an hour, so only the startup sweep can have moved it.
func TestStartSweepsRunsOnceAtStartup(t *testing.T) {
	f := newSweepFixture(t)
	f.addMessage(t, "m-stuck", core.MessageDelivered, f.clock.Now().Add(-time.Minute), f.clock.Now().Add(-time.Minute))
	f.startSweeps(t, Options{RequeueInterval: time.Hour, PruneInterval: time.Hour})

	if state, ok := f.state(t, "m-stuck"); !ok || state != core.MessageQueued {
		t.Fatalf("state after StartSweeps = %q (found %v), want queued from the startup sweep", state, ok)
	}
}

// TestStartSweepsPrunesByRetention pins the documented retention on the serve
// timer: read after 30 days, failed after 90, and never queued or delivered.
func TestStartSweepsPrunesByRetention(t *testing.T) {
	f := newSweepFixture(t)
	now := f.clock.Now()
	day := 24 * time.Hour
	lease := now.Add(time.Hour)

	f.addMessage(t, "read-recent", core.MessageRead, now.Add(-29*day), lease)
	f.addMessage(t, "read-old", core.MessageRead, now.Add(-31*day), lease)
	f.addMessage(t, "failed-recent", core.MessageFailed, now.Add(-89*day), lease)
	f.addMessage(t, "failed-old", core.MessageFailed, now.Add(-91*day), lease)
	f.addMessage(t, "queued-old", core.MessageQueued, now.Add(-200*day), lease)
	f.addMessage(t, "delivered-old", core.MessageDelivered, now.Add(-200*day), lease)

	f.startSweeps(t, Options{RequeueInterval: time.Hour, PruneInterval: 5 * time.Millisecond})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := f.state(t, "read-old"); !ok {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	for id, wantKept := range map[core.MessageID]bool{
		"read-recent":   true,
		"read-old":      false,
		"failed-recent": true,
		"failed-old":    false,
		"queued-old":    true,
		"delivered-old": true,
	} {
		_, ok := f.state(t, id)
		if ok != wantKept {
			t.Fatalf("message %s kept = %v, want %v", id, ok, wantKept)
		}
	}
}

// concurrencyMessages tracks how many sweeps run at once. A sweep sleeps inside
// the call, so an overlapping implementation would be observable.
type concurrencyMessages struct {
	store.MessageRepo
	delay time.Duration

	mu       sync.Mutex
	active   int
	peak     int
	requeues int
	prunes   int
}

func (r *concurrencyMessages) start() {
	r.mu.Lock()
	r.active++
	if r.active > r.peak {
		r.peak = r.active
	}
	r.mu.Unlock()
}

func (r *concurrencyMessages) done() {
	r.mu.Lock()
	r.active--
	r.mu.Unlock()
}

func (r *concurrencyMessages) RequeueExpired(ctx context.Context, now time.Time, maxAttempts int) (int, error) {
	r.start()
	defer r.done()
	time.Sleep(r.delay)
	r.mu.Lock()
	r.requeues++
	r.mu.Unlock()
	return r.MessageRepo.RequeueExpired(ctx, now, maxAttempts)
}

func (r *concurrencyMessages) Prune(ctx context.Context, before time.Time, states []core.MessageState) (int, error) {
	r.start()
	defer r.done()
	time.Sleep(r.delay)
	r.mu.Lock()
	r.prunes++
	r.mu.Unlock()
	return r.MessageRepo.Prune(ctx, before, states)
}

type concurrencyBackend struct {
	store.Backend
	messages *concurrencyMessages
}

func (b *concurrencyBackend) Messages() store.MessageRepo { return b.messages }

// TestStartSweepsNeverOverlap runs the sweeps faster than they take and proves
// they are serialized: at most one sweep is in flight, yet both timers fired.
func TestStartSweepsNeverOverlap(t *testing.T) {
	f := newSweepFixture(t)
	tracked := &concurrencyMessages{MessageRepo: f.backend.Messages(), delay: 20 * time.Millisecond}
	backend := &concurrencyBackend{Backend: f.backend, messages: tracked}

	server, err := New(Options{
		Backend:         backend,
		Clock:           f.clock,
		Messages:        app.NewMessageService(backend, f.clock, f.ids),
		Project:         "prj-1",
		RequeueInterval: time.Millisecond,
		PruneInterval:   time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	stop := server.StartSweeps(context.Background())
	time.Sleep(250 * time.Millisecond)
	stop()

	tracked.mu.Lock()
	defer tracked.mu.Unlock()
	if tracked.peak > 1 {
		t.Fatalf("peak concurrent sweeps = %d, want at most 1", tracked.peak)
	}
	if tracked.requeues < 2 || tracked.prunes < 2 {
		t.Fatalf("sweeps ran requeue=%d prune=%d, want both timers to fire repeatedly", tracked.requeues, tracked.prunes)
	}
}

// TestStartSweepsStopIsFinal proves the returned stop drains the loop: once it
// returns, no sweep runs again, so serve shutdown leaks neither work nor a
// goroutine.
func TestStartSweepsStopIsFinal(t *testing.T) {
	f := newSweepFixture(t)
	tracked := &concurrencyMessages{MessageRepo: f.backend.Messages()}
	backend := &concurrencyBackend{Backend: f.backend, messages: tracked}

	server, err := New(Options{
		Backend:         backend,
		Clock:           f.clock,
		Messages:        app.NewMessageService(backend, f.clock, f.ids),
		Project:         "prj-1",
		RequeueInterval: time.Millisecond,
		PruneInterval:   time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	stop := server.StartSweeps(context.Background())
	waitForCount(t, tracked, 2)
	stop()

	tracked.mu.Lock()
	before := tracked.requeues + tracked.prunes
	tracked.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	tracked.mu.Lock()
	after := tracked.requeues + tracked.prunes
	tracked.mu.Unlock()
	if after != before {
		t.Fatalf("sweeps after stop = %d, want 0 (before=%d after=%d)", after-before, before, after)
	}
}

func waitForCount(t *testing.T, tracked *concurrencyMessages, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tracked.mu.Lock()
		count := tracked.requeues + tracked.prunes
		tracked.mu.Unlock()
		if count >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("sweeps = %d, want at least %d", want, want)
}

// TestStartSweepsWithoutMessagesIsNoop keeps a read-only server valid: with no
// message service there is nothing to sweep and StartSweeps is a no-op.
func TestStartSweepsWithoutMessagesIsNoop(t *testing.T) {
	f := newSweepFixture(t)
	server, err := New(Options{Backend: f.backend, Clock: f.clock, Project: "prj-1"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	stop := server.StartSweeps(context.Background())
	stop()
}
