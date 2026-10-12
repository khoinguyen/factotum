package serve

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestBrokerPollsOnceForManyClients guards the scaling residual from PR #116:
// the per-client SSE poll cost one event-log query per client per interval. The
// broker must poll once and fan the change out to every subscriber, so the poll
// count over a window is independent of the client count.
func TestBrokerPollsOnceForManyClients(t *testing.T) {
	var mu sync.Mutex
	polls, seq := 0, 0
	latest := func(context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		polls++
		seq++
		return fmt.Sprintf("ev-%d", seq), nil
	}

	const clients = 12
	broker := newEventBroker(10*time.Millisecond, latest)
	subs := make([]<-chan string, clients)
	cancels := make([]func(), clients)
	for i := range subs {
		subs[i], cancels[i] = broker.subscribe()
	}
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()

	for i, ch := range subs {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatalf("subscriber %d never received an update", i)
		}
	}
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	got := polls
	mu.Unlock()
	if got > clients {
		t.Fatalf("event log polled %d times for %d clients; want one shared poll", got, clients)
	}
}

// TestBrokerDeliversUpdateCommittedAfterSubscribe guards the lost-update race
// behind the SSE CI flake. The broker must establish its baseline before
// subscribe returns, so a mutation committed once a client is subscribed is
// always broadcast. When the baseline is captured lazily in the poll goroutine,
// a mutation that lands in the window between subscribe and that first poll is
// folded into the baseline and never fanned out — the client waits forever for
// an update that already happened.
func TestBrokerDeliversUpdateCommittedAfterSubscribe(t *testing.T) {
	var mu sync.Mutex
	id := ""
	latest := func(context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		return id, nil
	}

	broker := newEventBroker(5*time.Millisecond, latest)
	ch, cancel := broker.subscribe()
	defer cancel()

	// A mutation commits in the window after subscribe returns but before the
	// poll goroutine's first tick.
	mu.Lock()
	id = "evt-1"
	mu.Unlock()

	select {
	case got := <-ch:
		if got != "evt-1" {
			t.Fatalf("update = %q, want evt-1", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("update committed after subscribe was never delivered (lost update)")
	}
}

// TestBrokerStopsWhenIdle ensures the broker does not poll forever after the
// last subscriber leaves; otherwise every dashboard view leaks a goroutine.
func TestBrokerStopsWhenIdle(t *testing.T) {
	var mu sync.Mutex
	polls := 0
	latest := func(context.Context) (string, error) {
		mu.Lock()
		polls++
		mu.Unlock()
		return "", nil
	}

	broker := newEventBroker(5*time.Millisecond, latest)
	_, cancel := broker.subscribe()
	time.Sleep(20 * time.Millisecond)
	cancel()

	mu.Lock()
	before := polls
	mu.Unlock()

	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	after := polls
	mu.Unlock()
	if after != before {
		t.Fatalf("broker kept polling after the last subscriber left: %d -> %d", before, after)
	}
}
