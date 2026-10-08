package app

import (
	"context"
	"fmt"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// DefaultRunTTL is how long a registered receiver session stays live without a
// heartbeat. A receiver heartbeats at a third of it.
const DefaultRunTTL = 90 * time.Second

// RegisterRunInput announces a receiver session. Registration is upsert
// idempotent on (project, actor, host, pid), so a reconnect keeps the run id.
type RegisterRunInput struct {
	ProjectID core.ProjectID
	ActorID   core.ActorID
	TaskID    *core.TicketID
	Harness   string
	Host      string
	PID       int
	CanInject bool
	TTL       time.Duration
}

// RegisterRun creates or renews a receiver session and sweeps expired leases so
// a crashed receiver's in-flight message is not stuck. It resolves the run's
// actor address for the case the actor id is omitted.
func (s *MessageService) RegisterRun(ctx context.Context, in RegisterRunInput) (*core.Run, error) {
	if _, err := s.backend.Projects().Get(ctx, in.ProjectID); err != nil {
		return nil, fmt.Errorf("run project: %w", err)
	}
	if in.ActorID == "" {
		return nil, fmt.Errorf("%w: run actor is required", core.ErrInvalid)
	}
	ttl := in.TTL
	if ttl <= 0 {
		ttl = DefaultRunTTL
	}
	now := s.clock.Now()
	run := &core.Run{
		ID:         core.RunID(s.ids.NewID("run")),
		ProjectID:  in.ProjectID,
		ActorID:    in.ActorID,
		TaskID:     in.TaskID,
		Harness:    in.Harness,
		Host:       in.Host,
		PID:        in.PID,
		CanInject:  in.CanInject,
		CreatedAt:  now,
		SeenAt:     now,
		LeaseUntil: now.Add(ttl),
	}
	registered, err := s.backend.Runs().Register(ctx, run)
	if err != nil {
		return nil, err
	}
	// RequeueExpired is the crash-recovery sweep; a receiver registering is the
	// natural moment to run it, so a dead session's lease is redelivered.
	if _, err := s.backend.Messages().RequeueExpired(ctx, now, store.MaxMessageAttempts); err != nil {
		return nil, err
	}
	return registered, nil
}

// HeartbeatRun renews a run's lease from the clock. A non-positive lease uses
// DefaultRunTTL.
func (s *MessageService) HeartbeatRun(ctx context.Context, id core.RunID, lease time.Duration) error {
	if lease <= 0 {
		lease = DefaultRunTTL
	}
	return s.backend.Runs().Heartbeat(ctx, id, s.clock.Now(), lease)
}

// DeregisterRun removes a run on clean shutdown.
func (s *MessageService) DeregisterRun(ctx context.Context, id core.RunID) error {
	return s.backend.Runs().Delete(ctx, id)
}

// ListRuns lists registered runs (receiver liveness).
func (s *MessageService) ListRuns(ctx context.Context, filter store.RunFilter) ([]*core.Run, error) {
	return s.backend.Runs().List(ctx, filter)
}

// ClaimInput selects the next message for a run. ActorID and TaskID are the
// run's registered addresses; they widen claimability beyond the run's own
// address.
type ClaimInput struct {
	ProjectID core.ProjectID
	RunID     core.RunID
	ActorID   *core.ActorID
	TaskID    *core.TicketID
	Wait      time.Duration
	Lease     time.Duration
}

// Claim atomically takes the oldest message claimable by the run and marks it
// delivered, recording a message.claimed event. It returns core.ErrNotFound
// when nothing is claimable within the wait.
func (s *MessageService) Claim(ctx context.Context, in ClaimInput) (*core.Message, error) {
	recipient := store.ClaimRecipient{ActorID: in.ActorID, TaskID: in.TaskID}
	message, err := s.backend.Messages().Claim(ctx, store.ClaimRequest{
		ProjectID: in.ProjectID,
		RunID:     in.RunID,
		Recipient: recipient,
		Lease:     in.Lease,
		Wait:      in.Wait,
	})
	if err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: message.ProjectID,
		TicketID:  message.TaskID,
		Kind:      core.EventMessageClaimed,
		Summary:   fmt.Sprintf("claimed message %s", message.ID),
	}); err != nil {
		return nil, err
	}
	return message, nil
}

// AckInput finalizes a delivered message owned by a run.
type AckInput struct {
	ID    core.MessageID
	RunID core.RunID
	State core.MessageState
	Error string
}

// Ack finalizes a delivered message as read (terminal success) or failed
// (terminal failure), recording the matching event. Read is idempotent: a
// repeat ack of an already-read message succeeds without a second event.
func (s *MessageService) Ack(ctx context.Context, in AckInput) error {
	before, err := s.backend.Messages().Get(ctx, in.ID)
	if err != nil {
		return err
	}
	if err := s.backend.Messages().Ack(ctx, store.AckRequest{ID: in.ID, RunID: in.RunID, State: in.State, Error: in.Error}); err != nil {
		return err
	}
	// A terminal message is unchanged by a repeat ack, so only a state change
	// records an event.
	if before.State.Terminal() {
		return nil
	}
	kind := core.EventMessageRead
	summary := fmt.Sprintf("read message %s", in.ID)
	if in.State == core.MessageFailed {
		kind = core.EventMessageFailed
		summary = fmt.Sprintf("failed message %s: %s", in.ID, in.Error)
	}
	return appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: before.ProjectID,
		TicketID:  before.TaskID,
		Kind:      kind,
		Summary:   summary,
	})
}

// Nack requeues a delivered message, or parks it failed once it reaches the
// attempt cap.
func (s *MessageService) Nack(ctx context.Context, id core.MessageID, runID core.RunID, reason string) error {
	before, err := s.backend.Messages().Get(ctx, id)
	if err != nil {
		return err
	}
	if before.State.Terminal() {
		return nil
	}
	if err := s.backend.Messages().Nack(ctx, id, runID, reason); err != nil {
		return err
	}
	after, err := s.backend.Messages().Get(ctx, id)
	if err != nil {
		return err
	}
	if after.State != core.MessageFailed {
		return nil
	}
	return appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: after.ProjectID,
		TicketID:  after.TaskID,
		Kind:      core.EventMessageFailed,
		Summary:   fmt.Sprintf("failed message %s: %s", id, reason),
	})
}
