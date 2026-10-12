package app

import (
	"context"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// TestEventAttributedToCaller pins the attribution substrate: an event appended
// while a caller actor+session is on the context carries both, so work is
// attributable to the session that produced it.
func TestEventAttributedToCaller(t *testing.T) {
	h := newHarness(t)
	ctx := WithCaller(context.Background(), "chief", "sess-1")

	project, err := h.projects.Create(ctx, "Acme", "demo", nil)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	events, err := h.backend.Events().List(ctx, store.EventFilter{ProjectID: project.ID})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	got := events[0]
	if got.By == nil || *got.By != "chief" {
		t.Fatalf("event By = %v, want chief", got.By)
	}
	if got.Session != "sess-1" {
		t.Fatalf("event Session = %q, want sess-1", got.Session)
	}
}

// TestEventWithoutCallerHasNoAttribution pins that an unattributed append stays
// clean rather than inventing an actor.
func TestEventWithoutCallerHasNoAttribution(t *testing.T) {
	h := newHarness(t)
	project, err := h.projects.Create(context.Background(), "Acme", "demo", nil)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	events, err := h.backend.Events().List(context.Background(), store.EventFilter{ProjectID: project.ID})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if events[0].By != nil || events[0].Session != "" {
		t.Fatalf("event attribution = (%v, %q), want (nil, empty)", events[0].By, events[0].Session)
	}
}

func TestRegisterRunRecordsSession(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	messages := NewMessageService(h.backend, fixedClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, &seqIDs{})

	run, err := messages.RegisterRun(ctx, RegisterRunInput{
		ProjectID: project.ID,
		ActorID:   "builder-t-1",
		Session:   "sess-1",
		Host:      "host-a",
		PID:       1,
	})
	if err != nil {
		t.Fatalf("RegisterRun() error = %v", err)
	}
	if run.Session != "sess-1" {
		t.Fatalf("RegisterRun().Session = %q, want sess-1", run.Session)
	}
}

func TestSessionNewIDIsDistinct(t *testing.T) {
	h := newHarness(t)
	sessions := NewSessionService(h.backend, fixedClock{t: time.Now()}, &seqIDs{})
	first := sessions.NewID()
	second := sessions.NewID()
	if first == "" || second == "" || first == second {
		t.Fatalf("NewID() = %q then %q, want two distinct non-empty ids", first, second)
	}
}

func TestSessionStartAndRetire(t *testing.T) {
	h := newHarness(t)
	ctx := WithCaller(context.Background(), "chief", "sess-1")
	project := h.newProject(t)
	sessions := NewSessionService(h.backend, fixedClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, &seqIDs{})

	started, err := sessions.Start(ctx, project.ID, "sess-1")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if started.Kind != core.EventSessionStarted || started.Session != "sess-1" {
		t.Fatalf("Start() event = %+v, want session.started for sess-1", started)
	}
	if started.By == nil || *started.By != "chief" {
		t.Fatalf("Start() By = %v, want chief", started.By)
	}

	retired, err := sessions.Retire(ctx, project.ID, "sess-1", "sess-2")
	if err != nil {
		t.Fatalf("Retire() error = %v", err)
	}
	if retired.Kind != core.EventSessionRetired || retired.Session != "sess-1" {
		t.Fatalf("Retire() event = %+v, want session.retired for sess-1", retired)
	}
	if retired.Data["successor"] != "sess-2" {
		t.Fatalf("Retire() successor = %v, want sess-2", retired.Data["successor"])
	}
}
