package app

import (
	"context"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// addMessage stores a message directly with the given state and UpdatedAt, so a
// sweep test can place it exactly on one side of a retention or lease boundary.
func (f *messageFixture) addMessage(t *testing.T, id core.MessageID, state core.MessageState, updatedAt time.Time) {
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
		lease := updatedAt.Add(store.DefaultMessageLease)
		message.RunID = &runID
		message.LeaseUntil = &lease
	}
	if err := f.backend.Messages().Create(context.Background(), message); err != nil {
		t.Fatalf("create %s message: %v", id, err)
	}
}

// TestRequeueExpiredSweepsWithoutRegistration is the crash-recovery guarantee:
// a lapsed delivery lease returns to the queue on the sweep alone, with no
// receiver registration to trigger it. It also pins the injected clock: a lease
// that has not lapsed at the service clock is left delivered.
func TestRequeueExpiredSweepsWithoutRegistration(t *testing.T) {
	f := newMessageFixture(t)
	ctx := context.Background()
	now := f.clock.Now()

	f.addMessage(t, "m-expired", core.MessageDelivered, now.Add(-2*time.Minute))
	f.addMessage(t, "m-live", core.MessageDelivered, now.Add(time.Minute))

	moved, err := f.messages.RequeueExpired(ctx)
	if err != nil {
		t.Fatalf("RequeueExpired() error = %v", err)
	}
	if moved != 1 {
		t.Fatalf("RequeueExpired() moved = %d, want 1", moved)
	}
	expired, _ := f.backend.Messages().Get(ctx, "m-expired")
	if expired.State != core.MessageQueued || expired.RunID != nil {
		t.Fatalf("expired message = %+v, want queued with no run", expired)
	}
	if expired.Attempts != 1 {
		t.Fatalf("expired message attempts = %d, want the attempt retained (1)", expired.Attempts)
	}
	live, _ := f.backend.Messages().Get(ctx, "m-live")
	if live.State != core.MessageDelivered {
		t.Fatalf("live message state = %q, want delivered (lease not lapsed)", live.State)
	}
}

// TestRequeueExpiredParksAtAttemptCap pins that the service forwards the store's
// attempt cap, so a lease that keeps lapsing ends terminal failed rather than
// looping forever.
func TestRequeueExpiredParksAtAttemptCap(t *testing.T) {
	f := newMessageFixture(t)
	ctx := context.Background()
	f.addMessage(t, "m-exhausted", core.MessageDelivered, f.clock.Now().Add(-2*time.Minute))
	message, _ := f.backend.Messages().Get(ctx, "m-exhausted")
	message.Attempts = store.MaxMessageAttempts
	if err := f.backend.Messages().Update(ctx, message); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	if _, err := f.messages.RequeueExpired(ctx); err != nil {
		t.Fatalf("RequeueExpired() error = %v", err)
	}
	got, _ := f.backend.Messages().Get(ctx, "m-exhausted")
	if got.State != core.MessageFailed {
		t.Fatalf("state = %q, want failed at the attempt cap", got.State)
	}
}

// TestPruneTerminalHonorsRetention pins the documented retention: read after 30
// days, failed after 90, and queued/delivered never auto-pruned however old.
func TestPruneTerminalHonorsRetention(t *testing.T) {
	f := newMessageFixture(t)
	ctx := context.Background()
	now := f.clock.Now()
	day := 24 * time.Hour

	f.addMessage(t, "read-recent", core.MessageRead, now.Add(-29*day))
	f.addMessage(t, "read-old", core.MessageRead, now.Add(-31*day))
	f.addMessage(t, "failed-recent", core.MessageFailed, now.Add(-89*day))
	f.addMessage(t, "failed-old", core.MessageFailed, now.Add(-91*day))
	f.addMessage(t, "queued-old", core.MessageQueued, now.Add(-200*day))
	f.addMessage(t, "delivered-old", core.MessageDelivered, now.Add(-200*day))

	removed, err := f.messages.PruneTerminal(ctx)
	if err != nil {
		t.Fatalf("PruneTerminal() error = %v", err)
	}
	if removed != 2 {
		t.Fatalf("PruneTerminal() removed = %d, want 2", removed)
	}
	for id, want := range map[core.MessageID]bool{
		"read-recent":   true,
		"read-old":      false,
		"failed-recent": true,
		"failed-old":    false,
		"queued-old":    true,
		"delivered-old": true,
	} {
		_, err := f.backend.Messages().Get(ctx, id)
		if want && err != nil {
			t.Fatalf("message %s was pruned, want kept: %v", id, err)
		}
		if !want && err == nil {
			t.Fatalf("message %s was kept, want pruned", id)
		}
	}
}

// TestPruneTerminalUsesTerminalTime pins that retention measures the terminal
// transition (UpdatedAt), not creation: a read message created long ago but acked
// yesterday survives.
func TestPruneTerminalUsesTerminalTime(t *testing.T) {
	f := newMessageFixture(t)
	ctx := context.Background()
	now := f.clock.Now()
	f.addMessage(t, "acked-yesterday", core.MessageRead, now.Add(-24*time.Hour))
	message, _ := f.backend.Messages().Get(ctx, "acked-yesterday")
	message.CreatedAt = now.Add(-365 * 24 * time.Hour)
	if err := f.backend.Messages().Update(ctx, message); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	if removed, err := f.messages.PruneTerminal(ctx); err != nil || removed != 0 {
		t.Fatalf("PruneTerminal() = (%d, %v), want (0, nil)", removed, err)
	}
}
