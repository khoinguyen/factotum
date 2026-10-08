package app

import (
	"context"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
)

func TestOriginResolvesTheCaptureRefinedFrom(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)

	idea := newIdea(t, h, project, "an idea")
	bug := newBug(t, h, project, "a bug")

	promotedFromIdea, err := h.tasks.Promote(ctx, idea.ID)
	if err != nil {
		t.Fatalf("Promote(idea) error = %v", err)
	}
	promotedFromBug, err := h.tasks.Promote(ctx, bug.ID)
	if err != nil {
		t.Fatalf("Promote(bug) error = %v", err)
	}
	plain, err := h.tasks.Add(ctx, TicketInput{ProjectID: project.ID, Title: "no origin"})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	blocker, err := h.tasks.Add(ctx, TicketInput{ProjectID: project.ID, Title: "blocker"})
	if err != nil {
		t.Fatalf("Add(blocker) error = %v", err)
	}
	if _, err := h.tasks.AddDep(ctx, promotedFromIdea.ID, blocker.ID); err != nil {
		t.Fatalf("AddDep() error = %v", err)
	}
	onlyDep, err := h.tasks.Add(ctx, TicketInput{ProjectID: project.ID, Title: "depends on work"})
	if err != nil {
		t.Fatalf("Add(onlyDep) error = %v", err)
	}
	if _, err := h.tasks.AddDep(ctx, onlyDep.ID, blocker.ID); err != nil {
		t.Fatalf("AddDep() error = %v", err)
	}

	cases := []struct {
		name string
		task *core.Ticket
		want core.TicketID
	}{
		{"promoted from an idea", promotedFromIdea, idea.ID},
		{"promoted from a bug", promotedFromBug, bug.ID},
		{"no dependencies", plain, ""},
		{"a later dependency does not shadow the origin", promotedFromIdea, idea.ID},
		{"a non-capture dependency is not an origin", onlyDep, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := h.tasks.Origin(ctx, tc.task)
			if err != nil {
				t.Fatalf("Origin() error = %v", err)
			}
			if tc.want == "" {
				if got != nil {
					t.Fatalf("Origin() = %+v, want nil", got)
				}
				return
			}
			if got == nil || got.ID != tc.want {
				t.Fatalf("Origin() = %+v, want %s", got, tc.want)
			}
		})
	}
}

func TestOriginSkipsADanglingCapture(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	idea := newIdea(t, h, project, "an idea")
	task, err := h.tasks.Promote(ctx, idea.ID)
	if err != nil {
		t.Fatalf("Promote() error = %v", err)
	}
	if err := h.tasks.Delete(ctx, idea.ID); err != nil {
		t.Fatalf("Delete(idea) error = %v", err)
	}

	got, err := h.tasks.Origin(ctx, task)
	if err != nil {
		t.Fatalf("Origin() error = %v", err)
	}
	if got != nil {
		t.Fatalf("Origin() = %+v, want nil for a deleted capture", got)
	}
}
