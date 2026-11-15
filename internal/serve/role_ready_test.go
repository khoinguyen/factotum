package serve

import (
	"context"
	"testing"

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

// TestBoardAndRenderAgreeOnStatusClasses pins the status-class parity between
// the web work board and the render view. The render classifier used to fold in
// progress into "blocked", so `ft graph render --format json` disagreed with
// the board; a status that either surface classifies on its own must not drift
// again. Statuses whose board class depends on the ready bucket (todo ->
// waiting) are out of scope: render has no waiting class yet.
func TestBoardAndRenderAgreeOnStatusClasses(t *testing.T) {
	tests := []struct {
		status core.TicketStatus
		want   string
	}{
		{core.StatusInProgress, "in-progress"},
		{core.StatusReadyForReview, "review"},
		{core.StatusDone, "done"},
		{core.StatusCancelled, "cancelled"},
		{core.StatusBlocked, "blocked"},
	}
	policy := core.DefaultResolutionPolicy()
	project := &core.Project{ID: "prj-1", Name: "Acme", Policy: policy}
	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			if got := classify(core.Ticket{Status: tc.status}, false, false, false); got != tc.want {
				t.Fatalf("board classify(%s) = %q, want %q", tc.status, got, tc.want)
			}
			ticket := core.Ticket{ID: "t-1", ProjectID: project.ID, Kind: core.KindTask, Title: "work", Status: tc.status}
			built, err := graph.New([]core.Ticket{ticket}, policy)
			if err != nil {
				t.Fatalf("graph.New() error = %v", err)
			}
			view := render.View{Project: project, Tasks: []*core.Ticket{&ticket}, Graph: built, Ready: built.ReadyByActor(nil)}
			if got := string(view.Classify(ticket)); got != tc.want {
				t.Fatalf("render Classify(%s) = %q, board says %q", tc.status, got, tc.want)
			}
		})
	}
}
