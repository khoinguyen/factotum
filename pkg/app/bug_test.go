package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
)

func newBug(t *testing.T, h *harness, project *core.Project, title string) *core.Ticket {
	t.Helper()
	bug, err := h.tasks.Add(context.Background(), TicketInput{ProjectID: project.ID, Kind: core.KindBug, Title: title})
	if err != nil {
		t.Fatalf("Add(bug) error = %v", err)
	}
	return bug
}

func TestAddBugIsValid(t *testing.T) {
	h := newHarness(t)
	project := h.newProject(t)
	bug := newBug(t, h, project, "it crashes")
	if bug.Kind != core.KindBug || bug.Status != core.StatusTodo {
		t.Fatalf("bug = %+v, want kind bug in todo", bug)
	}
}

func TestAddBugRejectsAssignee(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	actor, err := h.actors.Add(ctx, core.ActorHuman, "Khoi")
	if err != nil {
		t.Fatalf("Add(actor) error = %v", err)
	}
	_, err = h.tasks.Add(ctx, TicketInput{ProjectID: project.ID, Kind: core.KindBug, Title: "x", AssigneeID: &actor.ID})
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Add(assigned bug) error = %v, want ErrInvalid", err)
	}
}

func TestBugIsNotAssignable(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	bug := newBug(t, h, project, "it crashes")
	actor, err := h.actors.Add(ctx, core.ActorAgent, "claude")
	if err != nil {
		t.Fatalf("Add(actor) error = %v", err)
	}

	if _, err := h.tasks.Assign(ctx, bug.ID, &actor.ID); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Assign(bug) error = %v, want ErrInvalid", err)
	}
	if _, err := h.tasks.Claim(ctx, bug.ID, actor.ID, false); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Claim(bug) error = %v, want ErrInvalid", err)
	}
}

func TestBugStatusesAreCaptureOnly(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	bug := newBug(t, h, project, "it crashes")

	if _, err := h.tasks.SetStatus(ctx, bug.ID, core.StatusInProgress); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("SetStatus(bug, in_progress) error = %v, want ErrInvalid", err)
	}
	if _, err := h.tasks.SetStatus(ctx, bug.ID, core.StatusDone); err != nil {
		t.Fatalf("SetStatus(bug, done) error = %v", err)
	}
}

func TestPromoteBugCreatesLinkedTaskAndKeepsBug(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	bug := newBug(t, h, project, "it crashes")
	bug.Description = "steps to reproduce"
	if _, err := h.tasks.Set(ctx, bug.ID, TicketSet{Description: &bug.Description, Labels: []string{"triaged"}}); err != nil {
		t.Fatalf("Set(bug) error = %v", err)
	}
	agent := newAgent(t, h, "claude")

	task, err := h.tasks.Promote(ctx, bug.ID, groomedPromote(agent))
	if err != nil {
		t.Fatalf("Promote() error = %v", err)
	}
	if task.Kind != core.KindTask {
		t.Fatalf("Promote().Kind = %q, want task", task.Kind)
	}
	if task.Title != "it crashes" || task.Description != "steps to reproduce" {
		t.Fatalf("Promote() = %+v, want title/body copied from bug", task)
	}
	if !equalIDs(task.Deps, []core.TicketID{bug.ID}) {
		t.Fatalf("Promote().Deps = %v, want origin edge to %s", task.Deps, bug.ID)
	}

	storedBug, err := h.tasks.Get(ctx, bug.ID)
	if err != nil {
		t.Fatalf("Get(bug) error = %v", err)
	}
	if storedBug.Kind != core.KindBug || storedBug.Status != core.StatusTodo {
		t.Fatalf("bug after promote = %+v, want unchanged bug in todo", storedBug)
	}
	found := false
	for _, note := range storedBug.Notes {
		if strings.Contains(note.Body, string(task.ID)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("bug notes = %+v, want a promotion note naming %s", storedBug.Notes, task.ID)
	}
}

func TestKindMutationInvolvingBugIsRejected(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	bug := newBug(t, h, project, "it crashes")
	task, err := h.tasks.Add(ctx, TicketInput{ProjectID: project.ID, Title: "work"})
	if err != nil {
		t.Fatalf("Add(task) error = %v", err)
	}

	bugKind := core.KindBug
	if _, err := h.tasks.Update(ctx, task.ID, TicketUpdate{Kind: &bugKind}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Update(task -> bug) error = %v, want ErrInvalid", err)
	}
	taskKind := core.KindTask
	if _, err := h.tasks.Update(ctx, bug.ID, TicketUpdate{Kind: &taskKind}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Update(bug -> task) error = %v, want ErrInvalid", err)
	}

	stored, err := h.tasks.Get(ctx, bug.ID)
	if err != nil {
		t.Fatalf("Get(bug) error = %v", err)
	}
	if stored.Kind != core.KindBug {
		t.Fatalf("bug kind changed to %q, want it untouched", stored.Kind)
	}
}
