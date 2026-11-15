package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store/memory"
)

// TestAgentReadyResolvesLoopRoles pins the grooming half of the actor rule: the
// agent-bucket check resolves an assignee through app.ActorService.Identity, so a
// loop role's claimed groomed task is agent-ready with no Actor record, while a
// registered human stays human and an unregistered non-role id still errors.
func TestAgentReadyResolvesLoopRoles(t *testing.T) {
	backend := memory.New()
	t.Cleanup(func() { _ = backend.Close() })
	clock := app.SystemClock{}
	ids := app.RandomIDGen{}
	projects := app.NewProjectService(backend, clock, ids)
	tasks := app.NewTicketService(backend, clock, ids)
	actors := app.NewActorService(backend, clock, ids)
	ctx := context.Background()

	project, err := projects.Create(ctx, "Acme", "", nil)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	groomed := func(title string) *core.Ticket {
		task, err := tasks.Add(ctx, app.TicketInput{
			ProjectID:          project.ID,
			Title:              title,
			Groomed:            true,
			AcceptanceCriteria: []string{"done"},
		})
		if err != nil {
			t.Fatalf("Add(%q) error = %v", title, err)
		}
		return task
	}

	roleTask := groomed("claimed by a loop role")
	if claimed, err := tasks.Claim(ctx, roleTask.ID, core.ActorID("builder-t-1"), false); err != nil {
		t.Fatalf("Claim(loop role) error = %v", err)
	} else {
		roleTask = claimed
	}
	agent, err := actors.Add(ctx, core.ActorAgent, "claude")
	if err != nil {
		t.Fatalf("Add(agent) error = %v", err)
	}
	registeredAgentTask := groomed("registered agent")
	if claimed, err := tasks.Assign(ctx, registeredAgentTask.ID, &agent.ID); err != nil {
		t.Fatalf("Assign(agent) error = %v", err)
	} else {
		registeredAgentTask = claimed
	}
	human, err := actors.Add(ctx, core.ActorHuman, "Khoi")
	if err != nil {
		t.Fatalf("Add(human) error = %v", err)
	}
	humanTask := groomed("human")
	if assigned, err := tasks.Assign(ctx, humanTask.ID, &human.ID); err != nil {
		t.Fatalf("Assign(human) error = %v", err)
	} else {
		humanTask = assigned
	}
	// Add stamps an assignee without a record check, the shape a deleted actor
	// leaves behind; Assign would reject it.
	typoID := core.ActorID("builer-t-1")
	stored, err := tasks.Add(ctx, app.TicketInput{
		ProjectID:          project.ID,
		Title:              "typo'd role",
		Groomed:            true,
		AcceptanceCriteria: []string{"done"},
		AssigneeID:         &typoID,
	})
	if err != nil {
		t.Fatalf("Add(typo assignee) error = %v", err)
	}

	for _, tc := range []struct {
		name    string
		task    *core.Ticket
		want    bool
		wantErr bool
	}{
		{"loop role with no record", roleTask, true, false},
		{"registered agent", registeredAgentTask, true, false},
		{"human", humanTask, false, false},
		{"unregistered non-role", stored, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ready, err := agentReady(ctx, actors, tc.task)
			if tc.wantErr {
				if !errors.Is(err, core.ErrNotFound) {
					t.Fatalf("agentReady() error = %v, want ErrNotFound", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("agentReady() error = %v", err)
			}
			if ready != tc.want {
				t.Fatalf("agentReady() = %v, want %v", ready, tc.want)
			}
		})
	}
}
