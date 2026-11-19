package app

import (
	"context"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// Terminal message retention. A read message is kept long enough to audit recent
// delivery; a failed one longer, because a failure is worth investigating.
// Queued and delivered messages never expire: an undelivered or in-flight
// message is outstanding work, not history.
const (
	ReadRetention   = 30 * 24 * time.Hour
	FailedRetention = 90 * 24 * time.Hour
)

// RequeueExpired reclaims delivered messages whose lease lapsed at the service
// clock, so a receiver that died mid-flight does not strand its message. Past
// the store's attempt cap the message becomes terminal failed instead. It is the
// crash-recovery sweep: a receiver registering runs it, and `ft serve` runs it on
// a timer so recovery does not depend on someone registering.
func (s *MessageService) RequeueExpired(ctx context.Context) (int, error) {
	return s.backend.Messages().RequeueExpired(ctx, s.clock.Now(), store.MaxMessageAttempts)
}

// PruneTerminal drops terminal messages past their retention (ReadRetention for
// read, FailedRetention for failed) and returns how many were removed. The
// service clock picks the cutoff, and the store keeps queued and delivered
// messages whatever the cutoff, so a sweep can never delete outstanding work.
func (s *MessageService) PruneTerminal(ctx context.Context) (int, error) {
	now := s.clock.Now()
	read, err := s.backend.Messages().Prune(ctx, now.Add(-ReadRetention), []core.MessageState{core.MessageRead})
	if err != nil {
		return read, err
	}
	failed, err := s.backend.Messages().Prune(ctx, now.Add(-FailedRetention), []core.MessageState{core.MessageFailed})
	return read + failed, err
}
