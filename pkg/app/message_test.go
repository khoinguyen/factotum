package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/memory"
)

type messageFixture struct {
	backend  store.Backend
	messages *MessageService
	clock    *stepClock
}

func newMessageFixture(t *testing.T) *messageFixture {
	t.Helper()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	backend := memory.New()
	t.Cleanup(func() { _ = backend.Close() })
	f := &messageFixture{backend: backend, clock: &stepClock{t: now}}
	f.messages = NewMessageService(backend, f.clock, &seqIDs{})
	ctx := context.Background()
	if err := backend.Projects().Create(ctx, &core.Project{ID: "prj-1", Name: "Acme", Policy: core.DefaultResolutionPolicy()}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return f
}

func (f *messageFixture) addActor(t *testing.T, id core.ActorID, kind core.ActorKind, name string) {
	t.Helper()
	ctx := context.Background()
	if err := f.backend.Actors().Create(ctx, &core.Actor{ID: id, Kind: kind, Name: name, Active: true}); err != nil {
		t.Fatalf("create actor: %v", err)
	}
}

func (f *messageFixture) addTask(t *testing.T, id core.TicketID, assignee *core.ActorID) {
	t.Helper()
	ctx := context.Background()
	task := &core.Ticket{ID: id, ProjectID: "prj-1", Kind: core.KindTask, Title: "work", Status: core.StatusTodo, AssigneeID: assignee}
	if err := f.backend.Tickets().Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
}

func TestMessageSendToActor(t *testing.T) {
	f := newMessageFixture(t)
	from := core.ActorID("act-sender")

	message, err := f.messages.Send(context.Background(), SendMessageInput{
		ProjectID: "prj-1",
		From:      &from,
		Target:    "actor:act-target",
		Body:      "hello",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if message.ID != "msg-1" {
		t.Fatalf("Send().ID = %q, want msg-1 (fixed ids)", message.ID)
	}
	if message.To != core.ActorAddress("act-target") {
		t.Fatalf("Send().To = %q, want actor:act-target", message.To)
	}
	if message.TaskID != nil {
		t.Fatalf("Send().TaskID = %v, want nil", message.TaskID)
	}
	if message.State != core.MessageQueued {
		t.Fatalf("Send().State = %q, want queued", message.State)
	}
	if !message.CreatedAt.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("Send().CreatedAt = %v, want the fixed clock", message.CreatedAt)
	}

	events, err := f.backend.Events().List(context.Background(), store.EventFilter{ProjectID: "prj-1", Kinds: []core.EventKind{core.EventMessageSent}})
	if err != nil {
		t.Fatalf("List(events) error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("message.sent events = %d, want 1", len(events))
	}
}

func TestMessageSendResolvesTaskSugar(t *testing.T) {
	ctx := context.Background()

	t.Run("live run wins", func(t *testing.T) {
		f := newMessageFixture(t)
		agent := core.ActorID("act-agent")
		f.addActor(t, agent, core.ActorAgent, "agent")
		f.addTask(t, "t-1", &agent)
		lease := f.clock.Now().Add(time.Hour)
		if _, err := f.backend.Runs().Register(ctx, &core.Run{ID: "run-1", ProjectID: "prj-1", ActorID: agent, TaskID: taskPtr("t-1"), CreatedAt: f.clock.Now(), SeenAt: f.clock.Now(), LeaseUntil: lease}); err != nil {
			t.Fatalf("register run: %v", err)
		}
		message, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: "t-1", Body: "hi"})
		if err != nil {
			t.Fatalf("Send() error = %v", err)
		}
		if message.To != core.RunAddress("run-1") {
			t.Fatalf("Send().To = %q, want run:run-1", message.To)
		}
		if message.TaskID == nil || *message.TaskID != "t-1" {
			t.Fatalf("Send().TaskID = %v, want t-1 retained", message.TaskID)
		}
	})

	t.Run("agent assignee", func(t *testing.T) {
		f := newMessageFixture(t)
		agent := core.ActorID("act-agent")
		f.addActor(t, agent, core.ActorAgent, "agent")
		f.addTask(t, "t-1", &agent)
		message, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: "t-1", Body: "hi"})
		if err != nil {
			t.Fatalf("Send() error = %v", err)
		}
		if message.To != core.ActorAddress("act-agent") {
			t.Fatalf("Send().To = %q, want actor:act-agent", message.To)
		}
	})

	t.Run("human assignee stays task", func(t *testing.T) {
		f := newMessageFixture(t)
		human := core.ActorID("act-human")
		f.addActor(t, human, core.ActorHuman, "khoi")
		f.addTask(t, "t-1", &human)
		message, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: "t-1", Body: "hi"})
		if err != nil {
			t.Fatalf("Send() error = %v", err)
		}
		if message.To != core.TaskAddress("t-1") {
			t.Fatalf("Send().To = %q, want task:t-1", message.To)
		}
		if message.TaskID == nil || *message.TaskID != "t-1" {
			t.Fatalf("Send().TaskID = %v, want t-1", message.TaskID)
		}
	})
}

func TestMessageSendRejects(t *testing.T) {
	f := newMessageFixture(t)
	ctx := context.Background()

	if _, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: "actor:a", Body: strings.Repeat("x", store.DefaultMessageBodyMax+1)}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Send(oversize) error = %v, want ErrInvalid", err)
	}
	if _, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: "t-missing", Body: "hi"}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Send(missing task) error = %v, want ErrNotFound", err)
	}
	if _, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "missing", Target: "actor:a", Body: "hi"}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Send(missing project) error = %v, want ErrNotFound", err)
	}
	if _, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: "group:x", Body: "hi"}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Send(bad address) error = %v, want ErrInvalid", err)
	}
}

func TestMessageInboxAndRead(t *testing.T) {
	f := newMessageFixture(t)
	ctx := context.Background()

	for _, target := range []string{"actor:a", "actor:b"} {
		if _, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: target, Body: "hi"}); err != nil {
			t.Fatalf("Send(%s) error = %v", target, err)
		}
	}

	all, err := f.messages.Inbox(ctx, InboxQuery{ProjectID: "prj-1"})
	if err != nil {
		t.Fatalf("Inbox() error = %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("Inbox() len = %d, want 2", len(all))
	}

	queued, err := f.messages.Inbox(ctx, InboxQuery{ProjectID: "prj-1", States: []core.MessageState{core.MessageQueued}})
	if err != nil {
		t.Fatalf("Inbox(queued) error = %v", err)
	}
	if len(queued) != 2 {
		t.Fatalf("Inbox(queued) len = %d, want 2", len(queued))
	}

	read, err := f.messages.Read(ctx, all[0].ID)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if read.State != core.MessageRead || read.ReadAt == nil {
		t.Fatalf("Read() = %+v, want read with ReadAt", read)
	}
	// Idempotent.
	if _, err := f.messages.Read(ctx, all[0].ID); err != nil {
		t.Fatalf("Read() repeat error = %v", err)
	}

	queued, err = f.messages.Inbox(ctx, InboxQuery{ProjectID: "prj-1", States: []core.MessageState{core.MessageQueued}})
	if err != nil {
		t.Fatalf("Inbox(queued) error = %v", err)
	}
	if len(queued) != 1 {
		t.Fatalf("Inbox(queued) after read len = %d, want 1", len(queued))
	}

	events, err := f.backend.Events().List(ctx, store.EventFilter{ProjectID: "prj-1", Kinds: []core.EventKind{core.EventMessageRead}})
	if err != nil {
		t.Fatalf("List(events) error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("message.read events = %d, want 1", len(events))
	}
}

func taskPtr(id core.TicketID) *core.TicketID { return &id }
