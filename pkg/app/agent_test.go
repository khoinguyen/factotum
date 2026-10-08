package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

func (f *messageFixture) claimInput(run core.RunID, actor *core.ActorID, task *core.TicketID) ClaimInput {
	return ClaimInput{ProjectID: "prj-1", RunID: run, ActorID: actor, TaskID: task}
}

func TestRegisterRunUpsertsAndIsStable(t *testing.T) {
	f := newMessageFixture(t)
	ctx := context.Background()
	actor := core.ActorID("act-agent")
	f.addActor(t, actor, core.ActorAgent, "agent")
	f.addTask(t, "t-1", &actor)

	in := RegisterRunInput{ProjectID: "prj-1", ActorID: actor, TaskID: taskPtr("t-1"), Harness: "opencode", Host: "host-1", PID: 4242}
	first, err := f.messages.RegisterRun(ctx, in)
	if err != nil {
		t.Fatalf("RegisterRun() error = %v", err)
	}
	if first.ID == "" {
		t.Fatal("RegisterRun().ID is empty")
	}
	if first.LeaseUntil.IsZero() {
		t.Fatal("RegisterRun().LeaseUntil is zero")
	}

	second, err := f.messages.RegisterRun(ctx, in)
	if err != nil {
		t.Fatalf("RegisterRun(repeat) error = %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("RegisterRun(repeat).ID = %q, want stable %q", second.ID, first.ID)
	}

	runs, err := f.messages.ListRuns(ctx, store.RunFilter{ProjectID: "prj-1"})
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("ListRuns() len = %d, want 1 (upsert)", len(runs))
	}

	if err := f.messages.DeregisterRun(ctx, first.ID); err != nil {
		t.Fatalf("DeregisterRun() error = %v", err)
	}
	runs, _ = f.messages.ListRuns(ctx, store.RunFilter{ProjectID: "prj-1"})
	if len(runs) != 0 {
		t.Fatalf("ListRuns() after deregister len = %d, want 0", len(runs))
	}
	if err := f.messages.DeregisterRun(ctx, core.RunID("run-missing")); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("DeregisterRun(missing) error = %v, want ErrNotFound", err)
	}
}

func TestRegisterRunRejectsMissingProject(t *testing.T) {
	f := newMessageFixture(t)
	if _, err := f.messages.RegisterRun(context.Background(), RegisterRunInput{ProjectID: "missing", ActorID: "act-1"}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("RegisterRun(missing project) error = %v, want ErrNotFound", err)
	}
}

func TestRegisterRunRequeuesExpiredLeases(t *testing.T) {
	f := newMessageFixture(t)
	ctx := context.Background()
	actor := core.ActorID("act-agent")
	f.addActor(t, actor, core.ActorAgent, "agent")

	expired := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	runID := core.RunID("run-old")
	if err := f.backend.Messages().Create(ctx, &core.Message{
		ID: "m-stuck", ProjectID: "prj-1", To: core.RunAddress(runID), Body: "hi",
		State: core.MessageDelivered, RunID: &runID, LeaseUntil: &expired, Attempts: 1,
		CreatedAt: f.clock.Now(), UpdatedAt: f.clock.Now(),
	}); err != nil {
		t.Fatalf("create delivered message: %v", err)
	}

	if _, err := f.messages.RegisterRun(ctx, RegisterRunInput{ProjectID: "prj-1", ActorID: actor, Host: "h", PID: 1}); err != nil {
		t.Fatalf("RegisterRun() error = %v", err)
	}
	got, err := f.backend.Messages().Get(ctx, "m-stuck")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.State != core.MessageQueued {
		t.Fatalf("expired message state = %q, want queued after register sweep", got.State)
	}
}

func TestClaimDeliversOldestClaimable(t *testing.T) {
	f := newMessageFixture(t)
	ctx := context.Background()
	actor := core.ActorID("act-agent")
	f.addActor(t, actor, core.ActorAgent, "agent")

	if _, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: "actor:act-agent", Body: "one"}); err != nil {
		t.Fatalf("Send(one) error = %v", err)
	}
	if _, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: "actor:act-agent", Body: "two"}); err != nil {
		t.Fatalf("Send(two) error = %v", err)
	}
	f.clock.t = f.clock.t.Add(time.Second) // ensure distinct CreatedAt

	got, err := f.messages.Claim(ctx, f.claimInput("run-1", &actor, nil))
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if got.Body != "one" {
		t.Fatalf("Claim().Body = %q, want oldest %q", got.Body, "one")
	}
	if got.State != core.MessageDelivered || got.RunID == nil || *got.RunID != "run-1" {
		t.Fatalf("Claim() = %+v, want delivered to run-1", got)
	}
	if got.Attempts != 1 {
		t.Fatalf("Claim().Attempts = %d, want 1", got.Attempts)
	}

	events, err := f.backend.Events().List(ctx, store.EventFilter{ProjectID: "prj-1", Kinds: []core.EventKind{core.EventMessageClaimed}})
	if err != nil {
		t.Fatalf("List(events) error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("message.claimed events = %d, want 1", len(events))
	}
}

func TestClaimRespectsRecipient(t *testing.T) {
	f := newMessageFixture(t)
	ctx := context.Background()
	actor := core.ActorID("act-agent")
	f.addActor(t, actor, core.ActorAgent, "agent")
	f.addTask(t, "t-1", nil)

	if _, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: "actor:act-other", Body: "not mine"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if _, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: "task:t-1", Body: "mine"}); err != nil {
		t.Fatalf("Send(task) error = %v", err)
	}

	got, err := f.messages.Claim(ctx, f.claimInput("run-1", &actor, taskPtr("t-1")))
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if got.Body != "mine" {
		t.Fatalf("Claim().Body = %q, want the task message", got.Body)
	}
}

func TestClaimLongPollReturnsNotFound(t *testing.T) {
	f := newMessageFixture(t)
	actor := core.ActorID("act-agent")
	in := f.claimInput("run-1", &actor, nil)
	in.Wait = 30 * time.Millisecond
	if _, err := f.messages.Claim(context.Background(), in); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Claim(empty) error = %v, want ErrNotFound after the wait", err)
	}
}

func TestAckReadIsTerminalAndIdempotent(t *testing.T) {
	f := newMessageFixture(t)
	ctx := context.Background()
	actor := core.ActorID("act-agent")
	f.addActor(t, actor, core.ActorAgent, "agent")
	if _, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: "actor:act-agent", Body: "hi"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	claimed, err := f.messages.Claim(ctx, f.claimInput("run-1", &actor, nil))
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}

	if err := f.messages.Ack(ctx, AckInput{ID: claimed.ID, RunID: "run-1", State: core.MessageRead}); err != nil {
		t.Fatalf("Ack(read) error = %v", err)
	}
	got, _ := f.backend.Messages().Get(ctx, claimed.ID)
	if got.State != core.MessageRead {
		t.Fatalf("state = %q, want read", got.State)
	}
	if err := f.messages.Ack(ctx, AckInput{ID: claimed.ID, RunID: "run-1", State: core.MessageRead}); err != nil {
		t.Fatalf("Ack(read) repeat error = %v", err)
	}
	events, _ := f.backend.Events().List(ctx, store.EventFilter{ProjectID: "prj-1", Kinds: []core.EventKind{core.EventMessageRead}})
	if len(events) != 1 {
		t.Fatalf("message.read events = %d, want 1 (idempotent)", len(events))
	}
}

func TestAckFailedRecordsError(t *testing.T) {
	f := newMessageFixture(t)
	ctx := context.Background()
	actor := core.ActorID("act-agent")
	f.addActor(t, actor, core.ActorAgent, "agent")
	if _, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: "actor:act-agent", Body: "hi"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	claimed, err := f.messages.Claim(ctx, f.claimInput("run-1", &actor, nil))
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := f.messages.Ack(ctx, AckInput{ID: claimed.ID, RunID: "run-1", State: core.MessageFailed, Error: "boom"}); err != nil {
		t.Fatalf("Ack(failed) error = %v", err)
	}
	got, _ := f.backend.Messages().Get(ctx, claimed.ID)
	if got.State != core.MessageFailed || got.Error != "boom" {
		t.Fatalf("state/error = %q/%q, want failed/boom", got.State, got.Error)
	}
	events, _ := f.backend.Events().List(ctx, store.EventFilter{ProjectID: "prj-1", Kinds: []core.EventKind{core.EventMessageFailed}})
	if len(events) != 1 {
		t.Fatalf("message.failed events = %d, want 1", len(events))
	}
}

func TestAckRejectsWrongRun(t *testing.T) {
	f := newMessageFixture(t)
	ctx := context.Background()
	actor := core.ActorID("act-agent")
	f.addActor(t, actor, core.ActorAgent, "agent")
	if _, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: "actor:act-agent", Body: "hi"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	claimed, err := f.messages.Claim(ctx, f.claimInput("run-1", &actor, nil))
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := f.messages.Ack(ctx, AckInput{ID: claimed.ID, RunID: "run-other", State: core.MessageRead}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Ack(wrong run) error = %v, want ErrInvalid", err)
	}
}

func TestNackRequeuesUnderMaxAndParksAtMax(t *testing.T) {
	ctx := context.Background()
	actor := core.ActorID("act-agent")

	t.Run("requeues under max", func(t *testing.T) {
		f := newMessageFixture(t)
		f.addActor(t, actor, core.ActorAgent, "agent")
		if _, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: "actor:act-agent", Body: "hi"}); err != nil {
			t.Fatalf("Send() error = %v", err)
		}
		claimed, _ := f.messages.Claim(ctx, f.claimInput("run-1", &actor, nil))
		if err := f.messages.Nack(ctx, claimed.ID, "run-1", "not now"); err != nil {
			t.Fatalf("Nack() error = %v", err)
		}
		got, _ := f.backend.Messages().Get(ctx, claimed.ID)
		if got.State != core.MessageQueued {
			t.Fatalf("state = %q, want queued", got.State)
		}
	})

	t.Run("parks at max", func(t *testing.T) {
		f := newMessageFixture(t)
		f.addActor(t, actor, core.ActorAgent, "agent")
		runID := core.RunID("run-1")
		if err := f.backend.Messages().Create(ctx, &core.Message{
			ID: "m-max", ProjectID: "prj-1", To: core.ActorAddress(actor), Body: "hi",
			State: core.MessageDelivered, RunID: &runID, Attempts: store.MaxMessageAttempts,
			CreatedAt: f.clock.Now(), UpdatedAt: f.clock.Now(),
		}); err != nil {
			t.Fatalf("create message: %v", err)
		}
		if err := f.messages.Nack(ctx, "m-max", "run-1", "gave up"); err != nil {
			t.Fatalf("Nack() error = %v", err)
		}
		got, _ := f.backend.Messages().Get(ctx, "m-max")
		if got.State != core.MessageFailed || got.Error != "gave up" {
			t.Fatalf("state/error = %q/%q, want failed/gave up", got.State, got.Error)
		}
		events, _ := f.backend.Events().List(ctx, store.EventFilter{ProjectID: "prj-1", Kinds: []core.EventKind{core.EventMessageFailed}})
		if len(events) != 1 {
			t.Fatalf("message.failed events = %d, want 1", len(events))
		}
	})
}

func TestHeartbeatRenewsLease(t *testing.T) {
	f := newMessageFixture(t)
	ctx := context.Background()
	actor := core.ActorID("act-agent")
	f.addActor(t, actor, core.ActorAgent, "agent")
	run, err := f.messages.RegisterRun(ctx, RegisterRunInput{ProjectID: "prj-1", ActorID: actor, Host: "h", PID: 1})
	if err != nil {
		t.Fatalf("RegisterRun() error = %v", err)
	}
	f.clock.t = f.clock.t.Add(time.Hour)
	if err := f.messages.HeartbeatRun(ctx, run.ID, 0); err != nil {
		t.Fatalf("HeartbeatRun() error = %v", err)
	}
	runs, _ := f.messages.ListRuns(ctx, store.RunFilter{ProjectID: "prj-1"})
	if len(runs) != 1 || !runs[0].SeenAt.Equal(f.clock.t) {
		t.Fatalf("SeenAt = %v, want the heartbeat clock %v", runs, f.clock.t)
	}
	if err := f.messages.HeartbeatRun(ctx, core.RunID("run-missing"), 0); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("HeartbeatRun(missing) error = %v, want ErrNotFound", err)
	}
}
