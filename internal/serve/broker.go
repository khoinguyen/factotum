package serve

import (
	"context"
	"sync"
	"time"
)

// eventBroker is a single shared poller for every SSE client on a dashboard.
// One goroutine checks the event log each Poll interval and fans a changed event
// id out to all subscribers, so N clients cost one query per interval instead of
// N. It starts on the first subscriber and stops when the last one leaves, so an
// idle dashboard holds no goroutine.
type eventBroker struct {
	poll   time.Duration
	latest func(context.Context) (string, error)

	mu     sync.Mutex
	subs   map[chan string]struct{}
	cancel context.CancelFunc
	done   chan struct{}
}

func newEventBroker(poll time.Duration, latest func(context.Context) (string, error)) *eventBroker {
	return &eventBroker{
		poll:   poll,
		latest: latest,
		subs:   make(map[chan string]struct{}),
	}
}

// subscribe registers a client and returns the channel that receives each new
// event id, plus a cancel func that unregisters it. The first subscriber starts
// the poll goroutine; the last cancel stops it and returns once it has exited.
func (b *eventBroker) subscribe() (<-chan string, func()) {
	ch := make(chan string, 1)

	b.mu.Lock()
	b.subs[ch] = struct{}{}
	if b.cancel == nil {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		b.cancel = cancel
		b.done = done
		go func() {
			defer close(done)
			b.run(ctx)
		}()
	}
	b.mu.Unlock()

	return ch, func() { b.unsubscribe(ch) }
}

func (b *eventBroker) unsubscribe(ch chan string) {
	b.mu.Lock()
	if _, ok := b.subs[ch]; !ok {
		b.mu.Unlock()
		return
	}
	delete(b.subs, ch)
	if len(b.subs) > 0 || b.cancel == nil {
		b.mu.Unlock()
		return
	}
	cancel, done := b.cancel, b.done
	b.cancel, b.done = nil, nil
	b.mu.Unlock()

	cancel()
	<-done
}

// run polls the event log and broadcasts each changed id. last lives only here,
// so subscribers never race on it.
func (b *eventBroker) run(ctx context.Context) {
	ticker := time.NewTicker(b.poll)
	defer ticker.Stop()

	last, _ := b.latest(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			id, err := b.latest(ctx)
			if err != nil || id == last {
				continue
			}
			last = id
			b.broadcast(id)
		}
	}
}

// broadcast sends id to every subscriber without blocking; a subscriber that has
// not drained its single buffered slot refreshes from its last signal anyway.
func (b *eventBroker) broadcast(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- id:
		default:
		}
	}
}
