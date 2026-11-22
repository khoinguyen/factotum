package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/jsonfile"
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

func TestLoopRole(t *testing.T) {
	cases := []struct {
		ref  string
		want core.ActorID
		ok   bool
	}{
		{"builder-t-1", "builder-t-1", true},
		{"reviewer-t-1", "reviewer-t-1", true},
		{"qa-t-1", "qa-t-1", true},
		{"chief", "chief", true},
		{"t-1", "", false},
		{"actor:builder-t-1", "", false},
		{"builder-", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := LoopRole(tc.ref)
		if ok != tc.ok || got != tc.want {
			t.Errorf("LoopRole(%q) = (%q, %v), want (%q, %v)", tc.ref, got, ok, tc.want, tc.ok)
		}
	}
}

// TestLoopDefaultRole pins the grammar a configured default actor
// (FACTOTUM_DEFAULT_ACTOR / default_actor) must match to be trusted: a loop
// role name the wrapper exports, carrying a real ft id — a task (t-), an
// artifact (art-), or a grooming session (groom-). The loop's grooming-agent and
// architecture-reviewer roles are defaults only, so LoopRole rejects them while
// this accepts them. A token that merely starts with a role name is not a role.
func TestLoopDefaultRole(t *testing.T) {
	cases := []struct {
		ref  string
		want core.ActorID
		ok   bool
	}{
		{"chief", "chief", true},
		{"builder-t-1", "builder-t-1", true},
		{"reviewer-t-ww4tono3t6", "reviewer-t-ww4tono3t6", true},
		{"qa-t-1", "qa-t-1", true},
		{"grooming-agent-art-ilsnyg2h3a", "grooming-agent-art-ilsnyg2h3a", true},
		{"architecture-reviewer-groom-abcde", "architecture-reviewer-groom-abcde", true},
		{"architecture-reviewer-t-1", "architecture-reviewer-t-1", true},
		// A role name with a suffix that is not an ft id: a model, a tool, or a
		// truncated id. Trusting one would attribute to a mailbox nobody claims.
		{"builder-gpt-4", "", false},
		{"qa-bot", "", false},
		{"reviewer-tool-2", "", false},
		{"grooming-agent-tool", "", false},
		{"grooming-agent-", "", false},
		{"builder-", "", false},
		{"builder", "", false},
		{"builer-t-1", "", false},
		{"ghostrider", "", false},
		{"t-1", "", false},
		{"actor:builder-t-1", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := LoopDefaultRole(tc.ref)
		if ok != tc.ok || got != tc.want {
			t.Errorf("LoopDefaultRole(%q) = (%q, %v), want (%q, %v)", tc.ref, got, ok, tc.want, tc.ok)
		}
	}
}

func TestMessageSendResolvesRoleAddress(t *testing.T) {
	f := newMessageFixture(t)
	ctx := context.Background()

	for _, role := range []string{"builder-t-1", "reviewer-t-1", "qa-t-1", "chief"} {
		message, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: role, Body: "hi"})
		if err != nil {
			t.Fatalf("Send(%s) error = %v", role, err)
		}
		if want := core.ActorAddress(core.ActorID(role)); message.To != want {
			t.Fatalf("Send(%s).To = %q, want %q", role, message.To, want)
		}
		if message.TaskID != nil {
			t.Fatalf("Send(%s).TaskID = %v, want nil (a role is not a task)", role, message.TaskID)
		}
	}

	// A bare ticket-shaped target keeps the ticket sugar: a missing ticket is
	// not silently treated as an actor.
	if _, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: "t-missing", Body: "hi"}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Send(non-role bare target) error = %v, want ErrNotFound", err)
	}
}

// TestMessageSendRejectsUnresolvableBareTarget guards the silent-misroute bug:
// a bare token that is neither a loop role nor ticket-shaped (e.g. a garbled
// role like "grooming-agent-art-1", or an actor name missing its actor: prefix)
// must fail loudly as invalid, not be coerced into ticket sugar and reported as
// a confusing "task not found".
func TestMessageSendRejectsUnresolvableBareTarget(t *testing.T) {
	f := newMessageFixture(t)
	ctx := context.Background()

	for _, target := range []string{"grooming-agent-art-1", "claude", "nonsense", "t"} {
		_, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: target, Body: "hi"})
		if !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("Send(%q) error = %v, want ErrInvalid (an unresolvable bare target is not ticket sugar)", target, err)
		}
		if !strings.Contains(err.Error(), target) {
			t.Fatalf("Send(%q) error = %v, want the offending target named", target, err)
		}
	}
}

func TestMessageRoleDeliveryRoundTrip(t *testing.T) {
	f := newMessageFixture(t)
	ctx := context.Background()
	role := core.ActorID("reviewer-t-1")

	if _, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: string(role), Body: "please review"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	run, err := f.messages.RegisterRun(ctx, RegisterRunInput{ProjectID: "prj-1", ActorID: role, Harness: "opencode", Host: "h", PID: 1})
	if err != nil {
		t.Fatalf("RegisterRun() error = %v", err)
	}
	claimed, err := f.messages.Claim(ctx, ClaimInput{ProjectID: "prj-1", RunID: run.ID, ActorID: &role})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claimed.Body != "please review" {
		t.Fatalf("Claim().Body = %q, want the role message", claimed.Body)
	}
	if err := f.messages.Ack(ctx, AckInput{ID: claimed.ID, RunID: run.ID, State: core.MessageRead}); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}
	got, err := f.messages.Get(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.State != core.MessageRead {
		t.Fatalf("message state = %q, want read after ack", got.State)
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

	t.Run("many runs one actor uses the mailbox", func(t *testing.T) {
		f := newMessageFixture(t)
		agent := core.ActorID("act-agent")
		f.addActor(t, agent, core.ActorAgent, "agent")
		f.addTask(t, "t-1", &agent)
		lease := f.clock.Now().Add(time.Hour)
		// Two sessions of the same actor: addressing either run would be an
		// arbitrary pick, so ticket sugar settles on the actor's mailbox, which
		// both runs can claim.
		for i, id := range []core.RunID{"run-1", "run-2"} {
			if _, err := f.backend.Runs().Register(ctx, &core.Run{ID: id, ProjectID: "prj-1", ActorID: agent, TaskID: taskPtr("t-1"), Host: "h", PID: i + 1, CreatedAt: f.clock.Now(), SeenAt: f.clock.Now(), LeaseUntil: lease}); err != nil {
				t.Fatalf("register run: %v", err)
			}
		}
		message, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: "t-1", Body: "hi"})
		if err != nil {
			t.Fatalf("Send() error = %v", err)
		}
		if message.To != core.ActorAddress(agent) {
			t.Fatalf("Send().To = %q, want %q (not a run picked by id order)", message.To, core.ActorAddress(agent))
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

// TestMessageSendRefusesAmbiguousTaskSugar guards the silent misroute this task
// fixed: the normal chief-loop state has a builder and a reviewer run live for
// one task, and ticket sugar used to pick one of them by run-id order. It must
// refuse instead, naming the candidate actor:<role> addresses, so the sender
// re-addresses the peer it meant and nothing is stored against a guess.
func TestMessageSendRefusesAmbiguousTaskSugar(t *testing.T) {
	f := newMessageFixture(t)
	ctx := context.Background()
	f.addTask(t, "t-1", nil)
	lease := f.clock.Now().Add(time.Hour)
	for i, actor := range []core.ActorID{"builder-t-1", "reviewer-t-1"} {
		run := &core.Run{
			ID:         core.RunID("run-" + strconv.Itoa(i+1)),
			ProjectID:  "prj-1",
			ActorID:    actor,
			TaskID:     taskPtr("t-1"),
			Host:       "h",
			PID:        i + 1,
			CreatedAt:  f.clock.Now(),
			SeenAt:     f.clock.Now(),
			LeaseUntil: lease,
		}
		if _, err := f.backend.Runs().Register(ctx, run); err != nil {
			t.Fatalf("register run: %v", err)
		}
	}

	_, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: "t-1", Body: "hi"})
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Send(t-1) error = %v, want ErrInvalid for an ambiguous ticket", err)
	}
	for _, want := range []string{"actor:builder-t-1", "actor:reviewer-t-1", "ft msg send actor:"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Send(t-1) error = %v, want it to name %q", err, want)
		}
	}

	messages, err := f.messages.Inbox(ctx, InboxQuery{ProjectID: "prj-1"})
	if err != nil {
		t.Fatalf("Inbox() error = %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("an ambiguous ticket send stored a message: %+v", messages)
	}
}

// TestMessageSendAmbiguousRefusalNamesThePeerFirst guards the guidance half of
// that refusal: the candidate the message tells the sender to copy must be the
// peer, never the sender. --from already says who is sending, so the sender
// sorts last and the example names its counterpart, whichever run registered
// first.
func TestMessageSendAmbiguousRefusalNamesThePeerFirst(t *testing.T) {
	ctx := context.Background()
	builder := core.ActorID("builder-t-1")
	reviewer := core.ActorID("reviewer-t-1")
	cases := []struct {
		name  string
		order []core.ActorID
		from  core.ActorID
		peer  core.ActorID
	}{
		{"builder sender, builder run first", []core.ActorID{builder, reviewer}, builder, reviewer},
		{"builder sender, reviewer run first", []core.ActorID{reviewer, builder}, builder, reviewer},
		{"reviewer sender, builder run first", []core.ActorID{builder, reviewer}, reviewer, builder},
		{"reviewer sender, reviewer run first", []core.ActorID{reviewer, builder}, reviewer, builder},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMessageFixture(t)
			f.addTask(t, "t-1", nil)
			lease := f.clock.Now().Add(time.Hour)
			for i, actor := range tc.order {
				run := &core.Run{
					ID:         core.RunID("run-" + strconv.Itoa(i+1)),
					ProjectID:  "prj-1",
					ActorID:    actor,
					TaskID:     taskPtr("t-1"),
					Host:       "h",
					PID:        i + 1,
					CreatedAt:  f.clock.Now(),
					SeenAt:     f.clock.Now(),
					LeaseUntil: lease,
				}
				if _, err := f.backend.Runs().Register(ctx, run); err != nil {
					t.Fatalf("register run: %v", err)
				}
			}

			from := tc.from
			_, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", From: &from, Target: "t-1", Body: "hi"})
			if !errors.Is(err, core.ErrInvalid) {
				t.Fatalf("Send(t-1) error = %v, want ErrInvalid", err)
			}
			if want := fmt.Sprintf("(%s, %s)", core.ActorAddress(tc.peer), core.ActorAddress(tc.from)); !strings.Contains(err.Error(), want) {
				t.Fatalf("Send(t-1) error = %v, want the candidates ordered %s (peer first)", err, want)
			}
			if want := fmt.Sprintf("you are %s", core.ActorAddress(tc.from)); !strings.Contains(err.Error(), want) {
				t.Fatalf("Send(t-1) error = %v, want it to say %q", err, want)
			}
			if want := fmt.Sprintf("ft msg send %s", core.ActorAddress(tc.peer)); !strings.Contains(err.Error(), want) {
				t.Fatalf("Send(t-1) error = %v, want the example to name the peer %q", err, want)
			}
			if self := fmt.Sprintf("ft msg send %s", core.ActorAddress(tc.from)); strings.Contains(err.Error(), self) {
				t.Fatalf("Send(t-1) error = %v, want the example not to name the sender %q", err, self)
			}
		})
	}
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

// TestMessageSendRejectsDoubledAndEmptyActorAddress pins an actor target to the
// same reference parsing the actor flags use: actor:actor:chief and actor: name
// no actor, so they must be rejected instead of stored as a mailbox nobody can
// claim.
func TestMessageSendRejectsDoubledAndEmptyActorAddress(t *testing.T) {
	f := newMessageFixture(t)
	ctx := context.Background()

	for _, target := range []string{"actor:actor:chief", "actor:", "actor:run:r-1"} {
		if _, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: target, Body: "hi"}); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("Send(%q) error = %v, want ErrInvalid", target, err)
		}
	}
	messages, err := f.messages.Inbox(ctx, InboxQuery{ProjectID: "prj-1"})
	if err != nil {
		t.Fatalf("Inbox() error = %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("a rejected target was stored: %+v", messages)
	}

	// The canonical and role-sugar forms still name the same mailbox.
	for _, target := range []string{"actor:chief", "chief"} {
		message, err := f.messages.Send(ctx, SendMessageInput{ProjectID: "prj-1", Target: target, Body: "hi"})
		if err != nil {
			t.Fatalf("Send(%q) error = %v", target, err)
		}
		if want := core.ActorAddress("chief"); message.To != want {
			t.Fatalf("Send(%q).To = %q, want %q", target, message.To, want)
		}
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

func TestMessageReadLeavesFailedTerminal(t *testing.T) {
	f := newMessageFixture(t)
	ctx := context.Background()
	failed := &core.Message{
		ID:        "m-failed",
		ProjectID: "prj-1",
		To:        core.ActorAddress("act-1"),
		Body:      "boom",
		State:     core.MessageFailed,
		Error:     "boom",
		CreatedAt: f.clock.Now(),
		UpdatedAt: f.clock.Now(),
	}
	if err := f.backend.Messages().Create(ctx, failed); err != nil {
		t.Fatalf("create failed message: %v", err)
	}

	got, err := f.messages.Read(ctx, "m-failed")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got.State != core.MessageFailed || got.Error != "boom" {
		t.Fatalf("Read(failed) = %+v, want failed/boom unchanged", got)
	}
	events, err := f.backend.Events().List(ctx, store.EventFilter{ProjectID: "prj-1", Kinds: []core.EventKind{core.EventMessageRead}})
	if err != nil {
		t.Fatalf("List(events) error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("message.read events = %d, want 0 for a terminal message", len(events))
	}
}

func taskPtr(id core.TicketID) *core.TicketID { return &id }

// TestMessageReadRepairsLegacyLink proves a message written before the link
// validator covered Zl/Zp and bidi runes can still transition to read. Every
// backend revalidates a message on Update, so the service repairs the link it
// loads and Read persists the repair. The fixture is a hand-edited jsonfile
// document, the shape the reported repro used.
func TestMessageReadRepairsLegacyLink(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factotum.json")
	open := func() store.Backend {
		backend, err := jsonfile.Open(ctx, store.Config{Backend: "jsonfile", Options: map[string]string{"path": path}})
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		return backend
	}

	backend := open()
	clock := &stepClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ids := &seqIDs{}
	messages := NewMessageService(backend, clock, ids)
	if err := backend.Projects().Create(ctx, &core.Project{ID: "prj-1", Name: "Acme", Policy: core.DefaultResolutionPolicy()}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	message, err := messages.Send(ctx, SendMessageInput{
		ProjectID: "prj-1",
		Target:    "actor:bob",
		Body:      "see the doc",
		Links:     []core.Link{{Kind: core.LinkDoc, URL: "https://example.invalid/spec", Title: "Design doc"}},
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	legacy := bytes.Replace(data, []byte(`"Design doc"`), []byte(`"a`+"\u202e"+`b"`), 1)
	if bytes.Equal(legacy, data) {
		t.Fatal("fixture did not rewrite the stored title")
	}
	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	backend = open()
	t.Cleanup(func() { _ = backend.Close() })
	clock.t = clock.t.Add(time.Minute)
	messages = NewMessageService(backend, clock, ids)

	got, err := messages.Get(ctx, message.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Links[0].Title != "a b" {
		t.Fatalf("Get() title = %q, want the repaired %q", got.Links[0].Title, "a b")
	}

	read, err := messages.Read(ctx, message.ID)
	if err != nil {
		t.Fatalf("Read() error = %v; a legacy link must not wedge the read", err)
	}
	if read.State != core.MessageRead || read.Links[0].Title != "a b" {
		t.Fatalf("Read() = %+v, want read with the repaired title", read)
	}
	stored, err := backend.Messages().Get(ctx, message.ID)
	if err != nil {
		t.Fatalf("Get(stored) error = %v", err)
	}
	if stored.Links[0].Title != "a b" {
		t.Fatalf("stored title = %q, want Read to persist the repair", stored.Links[0].Title)
	}
}

// TestMessageReadRepairsLegacyBlankLinkTitle proves a title that is only
// whitespace is repaired too. It carries no refused rune, so the rune repair
// alone returns it unchanged, while Link.Validate calls it blank - and every
// backend revalidates a message on Update, so a queued row stored by an older
// ft would never reach read. The title is dropped (an untitled link is legal),
// which also keeps the printed link off a dangling " - ".
func TestMessageReadRepairsLegacyBlankLinkTitle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factotum.json")
	clock := &stepClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ids := &seqIDs{}

	backend := legacyStore(t, ctx, path)
	if err := backend.Projects().Create(ctx, &core.Project{ID: "prj-1", Name: "Acme", Policy: core.DefaultResolutionPolicy()}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	messages := NewMessageService(backend, clock, ids)
	message, err := messages.Send(ctx, SendMessageInput{
		ProjectID: "prj-1",
		Target:    "actor:bob",
		Body:      "see the doc",
		Links:     []core.Link{{Kind: core.LinkDoc, URL: "https://example.invalid/spec", Title: "Design doc"}},
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	rewriteStore(t, path, `"Design doc"`, `"   "`)

	backend = legacyStore(t, ctx, path)
	t.Cleanup(func() { _ = backend.Close() })
	clock.t = clock.t.Add(time.Minute)
	messages = NewMessageService(backend, clock, ids)

	got, err := messages.Get(ctx, message.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Links[0].Title != "" {
		t.Fatalf("Get() title = %q, want the blank title dropped", got.Links[0].Title)
	}

	read, err := messages.Read(ctx, message.ID)
	if err != nil {
		t.Fatalf("Read() error = %v; a blank legacy title must not wedge the read", err)
	}
	if read.State != core.MessageRead || read.Links[0].Title != "" {
		t.Fatalf("Read() = %+v, want read with the blank title dropped", read)
	}
	stored, err := backend.Messages().Get(ctx, message.ID)
	if err != nil {
		t.Fatalf("Get(stored) error = %v", err)
	}
	if stored.Links[0].Title != "" {
		t.Fatalf("stored title = %q, want Read to persist the dropped title", stored.Links[0].Title)
	}
}
