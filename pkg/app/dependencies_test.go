package app

import (
	"context"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
)

// TestDependenciesSplitsCapturesFromBlockers proves a task's dependency edges
// are classified by target kind: executable targets (task, milestone) gate the
// task, while capture targets (idea, bug) are non-blocking provenance edges.
func TestDependenciesSplitsCapturesFromBlockers(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	agent := newAgent(t, h, "claude")

	idea := newIdea(t, h, project, "origin idea")
	umbrella := newIdea(t, h, project, "umbrella idea")
	blocker, err := h.tasks.Add(ctx, TicketInput{ProjectID: project.ID, Title: "blocker"})
	if err != nil {
		t.Fatalf("Add(blocker) error = %v", err)
	}

	task, err := h.tasks.Promote(ctx, idea.ID, groomedPromote(agent))
	if err != nil {
		t.Fatalf("Promote() error = %v", err)
	}
	if _, err := h.tasks.AddDep(ctx, task.ID, umbrella.ID); err != nil {
		t.Fatalf("AddDep(umbrella) error = %v", err)
	}
	if _, err := h.tasks.AddDep(ctx, task.ID, blocker.ID); err != nil {
		t.Fatalf("AddDep(blocker) error = %v", err)
	}
	task, err = h.tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	deps, captures, err := h.tasks.Dependencies(ctx, task)
	if err != nil {
		t.Fatalf("Dependencies() error = %v", err)
	}
	if len(deps) != 1 || deps[0].ID != blocker.ID {
		t.Fatalf("deps = %v, want only the blocker %s", ticketIDs(deps), blocker.ID)
	}
	if len(captures) != 2 || captures[0].ID != idea.ID || captures[1].ID != umbrella.ID {
		t.Fatalf("captures = %v, want [%s %s]", ticketIDs(captures), idea.ID, umbrella.ID)
	}
}

// TestDependenciesTreatsADanglingTargetAsBlocking proves an edge the store
// cannot resolve is a blocking dependency, matching the graph, which holds a
// task back on a missing edge.
func TestDependenciesTreatsADanglingTargetAsBlocking(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)

	blocker, err := h.tasks.Add(ctx, TicketInput{ProjectID: project.ID, Title: "blocker"})
	if err != nil {
		t.Fatalf("Add(blocker) error = %v", err)
	}
	task, err := h.tasks.Add(ctx, TicketInput{ProjectID: project.ID, Title: "work"})
	if err != nil {
		t.Fatalf("Add(work) error = %v", err)
	}
	if _, err := h.tasks.AddDep(ctx, task.ID, blocker.ID); err != nil {
		t.Fatalf("AddDep() error = %v", err)
	}
	if err := h.tasks.Delete(ctx, blocker.ID); err != nil {
		t.Fatalf("Delete(blocker) error = %v", err)
	}
	task, err = h.tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	deps, captures, err := h.tasks.Dependencies(ctx, task)
	if err != nil {
		t.Fatalf("Dependencies() error = %v", err)
	}
	if len(deps) != 1 || deps[0].ID != blocker.ID {
		t.Fatalf("deps = %v, want the dangling blocker %s", ticketIDs(deps), blocker.ID)
	}
	if len(captures) != 0 {
		t.Fatalf("captures = %v, want none for a dangling target", ticketIDs(captures))
	}
}

func ticketIDs(tasks []core.Ticket) []core.TicketID {
	out := make([]core.TicketID, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, task.ID)
	}
	return out
}
