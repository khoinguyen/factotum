package serve

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/graph"
	"github.com/khoinguyen/factotum/pkg/render"
)

// TestBoardAndRenderAgreeOnRoleAssignee pins the surface parity the actor rule
// needs: the web board and the render view classify from the same snapshot, so a
// task a loop role claimed with no Actor record must be agent-next in both. A
// regression that reclassifies only one surface fails here.
func TestBoardAndRenderAgreeOnRoleAssignee(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	project := f.addProject(t, "acme", "Acme")
	task, err := f.tasks.Add(ctx, app.TicketInput{
		ProjectID:          project.ID,
		Title:              "Role work",
		Groomed:            true,
		AcceptanceCriteria: []string{"works"},
	})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := f.tasks.Claim(ctx, task.ID, core.ActorID("builder-t-1"), false); err != nil {
		t.Fatalf("Claim(loop role) error = %v", err)
	}

	server, err := New(Options{Backend: f.backend, Clock: f.clock, Project: project.ID, Assets: testAssets()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	page, err := server.page(ctx)
	if err != nil {
		t.Fatalf("page() error = %v", err)
	}
	if !hasTask(page.NextAgent, task.ID) {
		t.Fatalf("NextAgent = %v, want role-claimed %s", taskIDs(page.NextAgent), task.ID)
	}
	if hasTask(page.NextHuman, task.ID) {
		t.Fatalf("NextHuman = %v, must not carry the role-claimed %s", taskIDs(page.NextHuman), task.ID)
	}
	boardView, _ := findTask(page.NextAgent, task.ID)
	if boardView.Class != "ready-agent" || boardView.Chip != "agent next" {
		t.Fatalf("board view = (class %q, chip %q), want (ready-agent, agent next)", boardView.Class, boardView.Chip)
	}

	snapshot, err := app.LoadSnapshot(ctx, f.backend, project.ID, f.clock.Now())
	if err != nil {
		t.Fatalf("LoadSnapshot() error = %v", err)
	}
	view := render.View{
		Project: snapshot.Project,
		Tasks:   snapshot.Tasks,
		Graph:   snapshot.Graph,
		Actors:  snapshot.Actors,
		Ready:   snapshot.Ready,
	}
	if got := view.Classify(*task); got != render.ClassReadyAgent {
		t.Fatalf("render Classify(%s) = %q, want %q (the board says %q)", task.ID, got, render.ClassReadyAgent, "ready-agent")
	}
}

// TestBoardAndRenderAgreeOnStatusClasses pins the class parity between the web
// work board and the render view. The render classifier used to fold in
// progress into "blocked", so `ft graph render --format json` disagreed with
// the board; a status that either surface classifies on its own must not drift
// again.
//
// A todo's class depends on readiness rather than status alone, so the table
// carries every readiness cause that leaves a todo waiting: an unresolved
// dependency, an active snooze, and a future not_before. Each case feeds both
// surfaces the same graph, so a rule that changes on one side only - say the
// board starts rendering a snoozed todo as blocked - fails here.
//
// "waiting" is also a named section of the web dashboard, so the string is a
// UI contract shared with the board template, not an incidental default. Do not
// rename it on one side without renaming the section on the other.
func TestBoardAndRenderAgreeOnStatusClasses(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	future := now.Add(48 * time.Hour)
	tests := []struct {
		name      string
		status    core.TicketStatus
		deps      []core.TicketID
		snooze    *core.Snooze
		notBefore *time.Time
		want      string
	}{
		{name: "todo waiting on a dependency", status: core.StatusTodo, deps: []core.TicketID{"t-blocker"}, want: "waiting"},
		{name: "todo snoozed indefinitely", status: core.StatusTodo, snooze: &core.Snooze{Indefinite: true}, want: "waiting"},
		{name: "todo not before a future date", status: core.StatusTodo, notBefore: &future, want: "waiting"},
		{name: "in progress", status: core.StatusInProgress, want: "in-progress"},
		{name: "ready for review", status: core.StatusReadyForReview, want: "review"},
		{name: "done", status: core.StatusDone, want: "done"},
		{name: "cancelled", status: core.StatusCancelled, want: "cancelled"},
		{name: "blocked", status: core.StatusBlocked, want: "blocked"},
	}
	policy := core.DefaultResolutionPolicy()
	project := &core.Project{ID: "prj-1", Name: "Acme", Policy: policy}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			blocker := core.Ticket{ID: "t-blocker", ProjectID: project.ID, Kind: core.KindTask, Title: "blocker", Status: core.StatusInProgress}
			ticket := core.Ticket{
				ID:        "t-1",
				ProjectID: project.ID,
				Kind:      core.KindTask,
				Title:     "work",
				Status:    tc.status,
				Deps:      tc.deps,
				Snooze:    tc.snooze,
				NotBefore: tc.notBefore,
			}
			built, err := graph.NewAt([]core.Ticket{blocker, ticket}, policy, now)
			if err != nil {
				t.Fatalf("graph.NewAt() error = %v", err)
			}
			// Both surfaces read the same readiness buckets: the board gets them
			// from the snapshot graph, the render view from the graph itself.
			bucket := built.ReadyByActor(nil)
			board := classify(ticket, slices.Contains(bucket.Agent, ticket.ID), slices.Contains(bucket.Human, ticket.ID), false)
			if board != tc.want {
				t.Fatalf("board classify(%s) = %q, want %q", tc.name, board, tc.want)
			}
			view := render.View{Project: project, Tasks: []*core.Ticket{&blocker, &ticket}, Graph: built, Ready: bucket}
			if got := string(view.Classify(ticket)); got != tc.want {
				t.Fatalf("render Classify(%s) = %q, board says %q", tc.name, got, tc.want)
			}
		})
	}
}
