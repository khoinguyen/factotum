package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
)

func newIdea(t *testing.T, h *harness, project *core.Project, title string) *core.Task {
	t.Helper()
	idea, err := h.tasks.Add(context.Background(), TaskInput{ProjectID: project.ID, Kind: core.KindIdea, Title: title})
	if err != nil {
		t.Fatalf("Add(idea) error = %v", err)
	}
	return idea
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
	_, err = h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Kind: core.KindIdea, Title: "x", AssigneeID: &actor.ID})
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
	if _, err := h.tasks.Set(ctx, idea.ID, TaskSet{Description: &idea.Description, Labels: []string{"groomed"}}); err != nil {
		t.Fatalf("Set(idea) error = %v", err)
	}

	task, err := h.tasks.Promote(ctx, idea.ID)
	if err != nil {
		t.Fatalf("Promote() error = %v", err)
	}
	if task.Kind != core.KindTask {
		t.Fatalf("Promote().Kind = %q, want task", task.Kind)
	}
	if task.Title != "a spark" || task.Description != "context" {
		t.Fatalf("Promote() = %+v, want title/body copied from idea", task)
	}
	if !equalIDs(task.Deps, []core.TaskID{idea.ID}) {
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

func TestPromoteRejectsNonIdea(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	task, err := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Title: "already a task"})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := h.tasks.Promote(ctx, task.ID); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Promote(task) error = %v, want ErrInvalid", err)
	}
}
