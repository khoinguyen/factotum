package app

import (
	"context"
	"errors"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
)

// roleTask adds an unassigned task to the harness project.
func roleTask(t *testing.T, h *harness, project *core.Project, title string) *core.Ticket {
	t.Helper()
	task, err := h.tasks.Add(context.Background(), TicketInput{ProjectID: project.ID, Title: title})
	if err != nil {
		t.Fatalf("Add(task) error = %v", err)
	}
	return task
}

// TestAssignAcceptsLoopRole pins assign to the same identity rule claim uses: a
// loop role is a mailbox, not an Actor record, so it can hold work with no
// registration.
func TestAssignAcceptsLoopRole(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	task := roleTask(t, h, project, "role work")
	role := core.ActorID("builder-t-1")

	assigned, err := h.tasks.Assign(ctx, task.ID, &role)
	if err != nil {
		t.Fatalf("Assign(loop role) error = %v, want no error", err)
	}
	if assigned.AssigneeID == nil || *assigned.AssigneeID != role {
		t.Fatalf("assignee = %v, want %s", assigned.AssigneeID, role)
	}
}

// TestAssignRejectsRoleLookalike pins the other half: a token that merely starts
// with a role name is not a role and still needs an Actor record.
func TestAssignRejectsRoleLookalike(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	task := roleTask(t, h, project, "role work")
	lookalike := core.ActorID("builder-gpt-4")

	if _, err := h.tasks.Assign(ctx, task.ID, &lookalike); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Assign(lookalike) error = %v, want ErrNotFound", err)
	}
}

// TestAssignPrefersRegisteredRoleShapedRecord pins record-before-grammar: a
// registered human whose id looks role-shaped resolves as the record, and
// promotion still refuses a human, so the role grammar never overrides a record.
func TestAssignPrefersRegisteredRoleShapedRecord(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	human, err := h.actors.Add(ctx, core.ActorHuman, "builder-t-1")
	if err != nil {
		t.Fatalf("Add(human) error = %v", err)
	}

	task := roleTask(t, h, project, "role work")
	if _, err := h.tasks.Assign(ctx, task.ID, &human.ID); err != nil {
		t.Fatalf("Assign(registered role-shaped human) error = %v, want no error", err)
	}

	idea := newIdea(t, h, project, "a spark")
	_, err = h.tasks.Promote(ctx, idea.ID, PromoteInput{
		AssigneeID:         &human.ID,
		AcceptanceCriteria: []string{"it works"},
	})
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Promote(registered role-shaped human) error = %v, want ErrInvalid", err)
	}
}

// TestPromoteAcceptsLoopRole pins promotion to the identity rule: a loop role is
// an active agent, so a capture can be groomed straight to it.
func TestPromoteAcceptsLoopRole(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	idea := newIdea(t, h, project, "a spark")
	role := core.ActorID("reviewer-t-9")

	task, err := h.tasks.Promote(ctx, idea.ID, PromoteInput{
		AssigneeID:         &role,
		AcceptanceCriteria: []string{"it works"},
	})
	if err != nil {
		t.Fatalf("Promote(loop role) error = %v, want no error", err)
	}
	if task.AssigneeID == nil || *task.AssigneeID != role {
		t.Fatalf("promoted assignee = %v, want %s", task.AssigneeID, role)
	}
}

// TestSetWaitingOnAcceptsLoopRole pins the waiting-on list to the same rule, so
// a task can wait on a loop role that has no Actor record.
func TestSetWaitingOnAcceptsLoopRole(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	task := roleTask(t, h, project, "waiting task")
	role := core.ActorID("chief")

	updated, err := h.tasks.SetWaitingOn(ctx, task.ID, []core.ActorID{role})
	if err != nil {
		t.Fatalf("SetWaitingOn(loop role) error = %v, want no error", err)
	}
	if len(updated.WaitingOn) != 1 || updated.WaitingOn[0] != role {
		t.Fatalf("waiting on = %v, want [%s]", updated.WaitingOn, role)
	}
}
