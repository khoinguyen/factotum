package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

func newIdea(t *testing.T, h *harness, project *core.Project, title string) *core.Ticket {
	t.Helper()
	idea, err := h.tasks.Add(context.Background(), TicketInput{ProjectID: project.ID, Kind: core.KindIdea, Title: title})
	if err != nil {
		t.Fatalf("Add(idea) error = %v", err)
	}
	return idea
}

func newAgent(t *testing.T, h *harness, name string) *core.Actor {
	t.Helper()
	actor, err := h.actors.Add(context.Background(), core.ActorAgent, name)
	if err != nil {
		t.Fatalf("Add(agent %s) error = %v", name, err)
	}
	return actor
}

// groomedPromote is the grooming data a promotion needs to land agent-ready.
func groomedPromote(agent *core.Actor) PromoteInput {
	return PromoteInput{AssigneeID: &agent.ID, AcceptanceCriteria: []string{"it works"}}
}

func TestAddIdeaIsValid(t *testing.T) {
	h := newHarness(t)
	project := h.newProject(t)
	idea := newIdea(t, h, project, "a spark")
	if idea.Kind != core.KindIdea || idea.Status != core.StatusTodo {
		t.Fatalf("idea = %+v, want kind idea in todo", idea)
	}
}

func TestAddIdeaRejectsAssignee(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	actor, err := h.actors.Add(ctx, core.ActorHuman, "Khoi")
	if err != nil {
		t.Fatalf("Add(actor) error = %v", err)
	}
	_, err = h.tasks.Add(ctx, TicketInput{ProjectID: project.ID, Kind: core.KindIdea, Title: "x", AssigneeID: &actor.ID})
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Add(assigned idea) error = %v, want ErrInvalid", err)
	}
}

func TestIdeaIsNotAssignable(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	idea := newIdea(t, h, project, "a spark")
	actor, err := h.actors.Add(ctx, core.ActorAgent, "claude")
	if err != nil {
		t.Fatalf("Add(actor) error = %v", err)
	}

	if _, err := h.tasks.Assign(ctx, idea.ID, &actor.ID); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Assign(idea) error = %v, want ErrInvalid", err)
	}
	if _, err := h.tasks.Claim(ctx, idea.ID, actor.ID, false); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Claim(idea) error = %v, want ErrInvalid", err)
	}
}

func TestIdeaStatusesAreCaptureOnly(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	idea := newIdea(t, h, project, "a spark")

	if _, err := h.tasks.SetStatus(ctx, idea.ID, core.StatusInProgress); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("SetStatus(idea, in_progress) error = %v, want ErrInvalid", err)
	}
	if _, err := h.tasks.SetStatus(ctx, idea.ID, core.StatusDone); err != nil {
		t.Fatalf("SetStatus(idea, done) error = %v", err)
	}
}

func TestPromoteCreatesLinkedTaskAndKeepsIdea(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	idea := newIdea(t, h, project, "a spark")
	idea.Description = "context"
	if _, err := h.tasks.Set(ctx, idea.ID, TicketSet{Description: &idea.Description, Labels: []string{"groomed"}}); err != nil {
		t.Fatalf("Set(idea) error = %v", err)
	}
	agent := newAgent(t, h, "claude")

	task, err := h.tasks.Promote(ctx, idea.ID, groomedPromote(agent))
	if err != nil {
		t.Fatalf("Promote() error = %v", err)
	}
	if task.Kind != core.KindTask {
		t.Fatalf("Promote().Kind = %q, want task", task.Kind)
	}
	if task.Title != "a spark" || task.Description != "context" {
		t.Fatalf("Promote() = %+v, want title/body copied from idea", task)
	}
	if !equalIDs(task.Deps, []core.TicketID{idea.ID}) {
		t.Fatalf("Promote().Deps = %v, want origin edge to %s", task.Deps, idea.ID)
	}

	// The idea is retained unchanged (same kind and status) as history, with a
	// note recording the promotion.
	storedIdea, err := h.tasks.Get(ctx, idea.ID)
	if err != nil {
		t.Fatalf("Get(idea) error = %v", err)
	}
	if storedIdea.Kind != core.KindIdea || storedIdea.Status != core.StatusTodo {
		t.Fatalf("idea after promote = %+v, want unchanged idea in todo", storedIdea)
	}
	found := false
	for _, note := range storedIdea.Notes {
		if strings.Contains(note.Body, string(task.ID)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("idea notes = %+v, want a promotion note naming %s", storedIdea.Notes, task.ID)
	}
}

func TestKindMutationInvolvingIdeaIsRejected(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	idea := newIdea(t, h, project, "a spark")
	task, err := h.tasks.Add(ctx, TicketInput{ProjectID: project.ID, Title: "work"})
	if err != nil {
		t.Fatalf("Add(task) error = %v", err)
	}

	// Demoting executable work into a capture loses its graph position; only an
	// explicit create makes an idea.
	ideaKind := core.KindIdea
	if _, err := h.tasks.Update(ctx, task.ID, TicketUpdate{Kind: &ideaKind}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Update(task -> idea) error = %v, want ErrInvalid", err)
	}

	// Promoting must go through `task promote` so the origin edge and history
	// note are written, not a bare kind mutation.
	taskKind := core.KindTask
	if _, err := h.tasks.Update(ctx, idea.ID, TicketUpdate{Kind: &taskKind}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Update(idea -> task) error = %v, want ErrInvalid", err)
	}

	stored, err := h.tasks.Get(ctx, idea.ID)
	if err != nil {
		t.Fatalf("Get(idea) error = %v", err)
	}
	if stored.Kind != core.KindIdea {
		t.Fatalf("idea kind changed to %q, want it untouched", stored.Kind)
	}
}

func TestPromoteRejectsNonIdea(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	task, err := h.tasks.Add(ctx, TicketInput{ProjectID: project.ID, Title: "already a task"})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	agent := newAgent(t, h, "claude")
	if _, err := h.tasks.Promote(ctx, task.ID, groomedPromote(agent)); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Promote(task) error = %v, want ErrInvalid", err)
	}
}

func TestPromoteCreatesGroomedAssignedTask(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	idea := newIdea(t, h, project, "a spark")
	agent := newAgent(t, h, "claude")

	task, err := h.tasks.Promote(ctx, idea.ID, PromoteInput{
		AssigneeID:         &agent.ID,
		AcceptanceCriteria: []string{"the widget renders", "the test passes"},
	})
	if err != nil {
		t.Fatalf("Promote() error = %v", err)
	}
	if !task.Groomed {
		t.Fatalf("Promote() Groomed = false, want a groomed task")
	}
	if task.AssigneeID == nil || *task.AssigneeID != agent.ID {
		t.Fatalf("Promote() AssigneeID = %v, want %s", task.AssigneeID, agent.ID)
	}
	if len(task.AcceptanceCriteria) != 2 {
		t.Fatalf("Promote() AcceptanceCriteria = %v, want both criteria", task.AcceptanceCriteria)
	}
	if task.Status != core.StatusTodo {
		t.Fatalf("Promote() Status = %q, want todo", task.Status)
	}
	if !equalIDs(task.Deps, []core.TicketID{idea.ID}) {
		t.Fatalf("Promote().Deps = %v, want origin edge to %s", task.Deps, idea.ID)
	}
}

func TestPromoteRequiresAcceptance(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	idea := newIdea(t, h, project, "a spark")
	agent := newAgent(t, h, "claude")

	_, err := h.tasks.Promote(ctx, idea.ID, PromoteInput{AssigneeID: &agent.ID})
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Promote(no acceptance) error = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "acceptance") {
		t.Fatalf("Promote(no acceptance) error = %q, want it to name the missing acceptance criterion", err)
	}
	// A rejected promotion must not leave a half-created task behind.
	tasks, err := h.tasks.List(ctx, store.TicketFilter{ProjectID: project.ID})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	for _, candidate := range tasks {
		if candidate.Kind == core.KindTask {
			t.Fatalf("Promote(no acceptance) created %s; want no task", candidate.ID)
		}
	}
}

func TestPromoteRequiresAssignee(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	idea := newIdea(t, h, project, "a spark")

	_, err := h.tasks.Promote(ctx, idea.ID, PromoteInput{AcceptanceCriteria: []string{"it works"}})
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Promote(no assignee) error = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "assignee") {
		t.Fatalf("Promote(no assignee) error = %q, want it to name the missing assignee", err)
	}
}

func TestPromoteRejectsNonAgentAssignee(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	idea := newIdea(t, h, project, "a spark")
	human, err := h.actors.Add(ctx, core.ActorHuman, "Khoi")
	if err != nil {
		t.Fatalf("Add(human) error = %v", err)
	}

	_, err = h.tasks.Promote(ctx, idea.ID, PromoteInput{
		AssigneeID:         &human.ID,
		AcceptanceCriteria: []string{"it works"},
	})
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Promote(human assignee) error = %v, want ErrInvalid", err)
	}
}

// errInjectedFault is the store error a test injects mid-promote.
var errInjectedFault = errors.New("injected store fault")

// faultyTickets fails Update for the tickets reject selects, so a test can drive
// a mid-promote store fault without a real backend failure.
type faultyTickets struct {
	store.TicketRepo
	reject func(*core.Ticket) bool
}

func (r faultyTickets) Update(ctx context.Context, task *core.Ticket) error {
	if r.reject(task) {
		return errInjectedFault
	}
	return r.TicketRepo.Update(ctx, task)
}

// faultyBackend exposes the wrapped backend's repositories with a ticket update
// fault injected.
type faultyBackend struct {
	store.Backend
	reject func(*core.Ticket) bool
}

func (b faultyBackend) Tickets() store.TicketRepo {
	return faultyTickets{TicketRepo: b.Backend.Tickets(), reject: b.reject}
}

// assertNothingPromoted asserts a failed promotion left no task behind and the
// capture as it was, i.e. no half-promoted state.
func assertNothingPromoted(t *testing.T, h *harness, project *core.Project, capture *core.Ticket) {
	t.Helper()
	ctx := context.Background()
	stored, err := h.tasks.Get(ctx, capture.ID)
	if err != nil {
		t.Fatalf("Get(capture) error = %v", err)
	}
	if stored.Kind != capture.Kind || stored.Status != capture.Status {
		t.Fatalf("capture after a failed promote = kind %q status %q, want it unchanged", stored.Kind, stored.Status)
	}
	if len(stored.Notes) != 0 {
		t.Fatalf("capture notes after a failed promote = %+v, want none", stored.Notes)
	}
	tasks, err := h.tasks.List(ctx, store.TicketFilter{ProjectID: project.ID})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	for _, candidate := range tasks {
		if candidate.ID != capture.ID {
			t.Fatalf("a failed promote left %s behind; want only the capture", candidate.ID)
		}
	}
}

func TestPromoteRollsBackWhenLinkingFails(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	idea := newIdea(t, h, project, "a spark")
	agent := newAgent(t, h, "claude")

	// The task is created and then updated to add the origin edge; failing that
	// update is the first mid-promote fault.
	faulty := NewTicketService(faultyBackend{
		Backend: h.backend,
		reject:  func(task *core.Ticket) bool { return task.Kind == core.KindTask },
	}, h.clock, h.ids)

	if _, err := faulty.Promote(ctx, idea.ID, groomedPromote(agent)); !errors.Is(err, errInjectedFault) {
		t.Fatalf("Promote() error = %v, want the injected store fault", err)
	}
	assertNothingPromoted(t, h, project, idea)
}

func TestPromoteRollsBackWhenTheCaptureNoteFails(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	idea := newIdea(t, h, project, "a spark")
	agent := newAgent(t, h, "claude")

	// The note on the capture is the last write; failing it must undo the task
	// and leave the capture itself untouched.
	faulty := NewTicketService(faultyBackend{
		Backend: h.backend,
		reject:  func(task *core.Ticket) bool { return task.ID == idea.ID },
	}, h.clock, h.ids)

	if _, err := faulty.Promote(ctx, idea.ID, groomedPromote(agent)); !errors.Is(err, errInjectedFault) {
		t.Fatalf("Promote() error = %v, want the injected store fault", err)
	}
	assertNothingPromoted(t, h, project, idea)
}

func TestPromoteIsIdempotent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	idea := newIdea(t, h, project, "a spark")
	agent := newAgent(t, h, "claude")

	first, err := h.tasks.Promote(ctx, idea.ID, groomedPromote(agent))
	if err != nil {
		t.Fatalf("Promote() first error = %v", err)
	}
	second, err := h.tasks.Promote(ctx, idea.ID, groomedPromote(agent))
	if err != nil {
		t.Fatalf("Promote() second error = %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("Promote() second = %s, want the already-promoted %s", second.ID, first.ID)
	}

	// Exactly one task carries the origin edge, and the capture recorded the
	// promotion once.
	dependents, err := h.tasks.List(ctx, store.TicketFilter{ProjectID: project.ID, DependsOn: &idea.ID})
	if err != nil {
		t.Fatalf("List(dependents) error = %v", err)
	}
	if len(dependents) != 1 || dependents[0].ID != first.ID {
		t.Fatalf("dependents of the capture = %v, want only %s", dependents, first.ID)
	}
	stored, err := h.tasks.Get(ctx, idea.ID)
	if err != nil {
		t.Fatalf("Get(capture) error = %v", err)
	}
	notes := 0
	for _, note := range stored.Notes {
		if strings.Contains(note.Body, "Promoted to") {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("capture has %d promotion notes, want exactly 1: %+v", notes, stored.Notes)
	}
}
