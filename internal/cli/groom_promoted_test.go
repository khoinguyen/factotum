package cli

import (
	"context"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store/memory"
)

// TestItemHandledCountsOnlyAPromotedTask pins the capture branch of the
// unattended completion check to the same rule promotion uses: a capture is
// handled when the task promoted from it is agent-ready. A milestone gate that
// depends on the capture is not promoted work, so it cannot stand in for it.
func TestItemHandledCountsOnlyAPromotedTask(t *testing.T) {
	ctx := context.Background()
	backend := memory.New()
	clock := app.SystemClock{}
	ids := app.RandomIDGen{}
	tasks := app.NewTicketService(backend, clock, ids)
	actors := app.NewActorService(backend, clock, ids)
	projects := app.NewProjectService(backend, clock, ids)

	project, err := projects.Create(ctx, "Acme", "demo", nil)
	if err != nil {
		t.Fatalf("Create(project) error = %v", err)
	}
	agent, err := actors.Add(ctx, core.ActorAgent, "claude")
	if err != nil {
		t.Fatalf("Add(agent) error = %v", err)
	}
	capture, err := tasks.Add(ctx, app.TicketInput{ProjectID: project.ID, Kind: core.KindIdea, Title: "an idea"})
	if err != nil {
		t.Fatalf("Add(idea) error = %v", err)
	}
	gate, err := tasks.Add(ctx, app.TicketInput{
		ProjectID:          project.ID,
		Kind:               core.KindMilestone,
		Title:              "v1 gate",
		Groomed:            true,
		AcceptanceCriteria: []string{"the release ships"},
		AssigneeID:         &agent.ID,
	})
	if err != nil {
		t.Fatalf("Add(milestone) error = %v", err)
	}
	if _, err := tasks.AddDep(ctx, gate.ID, capture.ID); err != nil {
		t.Fatalf("AddDep(milestone) error = %v", err)
	}

	var cached []*core.Ticket
	if handled, err := itemHandled(ctx, tasks, actors, project.ID, capture, &cached); err != nil {
		t.Fatalf("itemHandled(gate only) error = %v", err)
	} else if handled {
		t.Fatal("itemHandled(gate only) = true; a milestone is not the capture's promoted work")
	}

	if _, err := tasks.Promote(ctx, capture.ID, app.PromoteInput{
		AssigneeID:         &agent.ID,
		AcceptanceCriteria: []string{"it works"},
	}); err != nil {
		t.Fatalf("Promote() error = %v", err)
	}
	var refreshed []*core.Ticket
	if handled, err := itemHandled(ctx, tasks, actors, project.ID, capture, &refreshed); err != nil {
		t.Fatalf("itemHandled(promoted) error = %v", err)
	} else if !handled {
		t.Fatal("itemHandled(promoted) = false; an agent-ready promoted task handles the capture")
	}
}
