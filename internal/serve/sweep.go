package serve

import (
	"context"
	"sync"
	"time"
)

// Message-maintenance cadence. The serve app is the hub, so it owns both sweeps
// rather than leaving them to whichever receiver happens to register: a lapsed
// delivery lease is recovered on the lease scale, and terminal history is
// trimmed on a slow timer. The retentions themselves live with the message
// service (app.ReadRetention, app.FailedRetention).
const (
	// defaultRequeueInterval is deliberately lease-scale. The default claim lease
	// is 30s and a run TTL 90s, so a crashed receiver's in-flight message is
	// claimable again about a minute after its lease lapsed.
	defaultRequeueInterval = time.Minute
	// defaultPruneInterval is slow because retention is measured in days.
	defaultPruneInterval = time.Hour
)

// StartSweeps starts the message-maintenance sweeps and returns a function that
// stops them and waits for the loop to exit. It runs one sweep synchronously
// before returning, so a restarted hub recovers lapsed deliveries and trims
// expired history before it answers. The sweep runs both duties on one
// goroutine, so two sweeps never overlap even when a sweep outlives its
// interval; a failed sweep is reported through Options.Logf and never stops the
// timer. With no message service (a read-only server) it is a no-op.
func (s *Server) StartSweeps(ctx context.Context) func() {
	if s.options.Messages == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	// The startup sweep is synchronous by design: a test (and an operator
	// restarting the hub) can rely on expired leases being recovered before the
	// first tick.
	s.sweep(ctx, true, true)
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.sweepLoop(ctx)
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			wg.Wait()
		})
	}
}

// sweepLoop runs the requeue and prune sweeps on their own timers. One goroutine
// owns both, and each sweep runs inline, so no two sweeps run at once and a
// backlog of ticks collapses into one pending tick per ticker.
func (s *Server) sweepLoop(ctx context.Context) {
	requeue := time.NewTicker(s.options.RequeueInterval)
	prune := time.NewTicker(s.options.PruneInterval)
	defer requeue.Stop()
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-requeue.C:
			s.sweep(ctx, true, false)
		case <-prune.C:
			s.sweep(ctx, false, true)
		}
	}
}

// sweep runs the requested duties in order. Each is independent: one failing
// does not skip the other, and both are idempotent store sweeps, so a retry next
// tick is always safe.
func (s *Server) sweep(ctx context.Context, requeue, prune bool) {
	if requeue {
		if _, err := s.options.Messages.RequeueExpired(ctx); err != nil && s.options.Logf != nil {
			s.options.Logf("serve: lease sweep: %v", err)
		}
	}
	if prune {
		if _, err := s.options.Messages.PruneTerminal(ctx); err != nil && s.options.Logf != nil {
			s.options.Logf("serve: retention prune: %v", err)
		}
	}
}
