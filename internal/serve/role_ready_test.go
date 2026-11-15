package serve

import (
	"context"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
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
