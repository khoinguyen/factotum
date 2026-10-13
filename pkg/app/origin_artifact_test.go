package app

import (
	"context"
	"errors"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
)

// newPromptDoc stores a document artifact the way the web prompt box does: a
// free-form capture's origin, not attached to any ticket.
func newPromptDoc(t *testing.T, h *harness, project *core.Project, title string) *core.Artifact {
	t.Helper()
	artifact, err := h.artifacts.Add(context.Background(), ArtifactInput{
		ProjectID: project.ID,
		Kind:      core.ArtifactDoc,
		Title:     title,
		Body:      "the raw prompt",
	})
	if err != nil {
		t.Fatalf("Add(doc) error = %v", err)
	}
	return artifact
}

// TestAddCaptureRecordsOriginArtifact pins the grooming provenance: a capture
// produced from a prompt artifact carries that artifact as its origin.
func TestAddCaptureRecordsOriginArtifact(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	doc := newPromptDoc(t, h, project, "Add dark mode")

	capture, err := h.tasks.Add(ctx, TicketInput{
		ProjectID:      project.ID,
		Kind:           core.KindIdea,
		Title:          "Dark mode",
		OriginArtifact: &doc.ID,
	})
	if err != nil {
		t.Fatalf("Add(idea) error = %v", err)
	}
	if capture.OriginArtifact == nil || *capture.OriginArtifact != doc.ID {
		t.Fatalf("Add(idea).OriginArtifact = %v, want %s", capture.OriginArtifact, doc.ID)
	}

	stored, err := h.tasks.Get(ctx, capture.ID)
	if err != nil {
		t.Fatalf("Get(idea) error = %v", err)
	}
	if stored.OriginArtifact == nil || *stored.OriginArtifact != doc.ID {
		t.Fatalf("stored OriginArtifact = %v, want %s", stored.OriginArtifact, doc.ID)
	}
}

// TestAddBugRecordsOriginArtifact pins that the provenance is not idea-specific:
// a bug groomed from a prompt carries the artifact too.
func TestAddBugRecordsOriginArtifact(t *testing.T) {
	h := newHarness(t)
	project := h.newProject(t)
	doc := newPromptDoc(t, h, project, "Login is broken")

	bug, err := h.tasks.Add(context.Background(), TicketInput{
		ProjectID:      project.ID,
		Kind:           core.KindBug,
		Title:          "Login button does nothing",
		OriginArtifact: &doc.ID,
	})
	if err != nil {
		t.Fatalf("Add(bug) error = %v", err)
	}
	if bug.OriginArtifact == nil || *bug.OriginArtifact != doc.ID {
		t.Fatalf("Add(bug).OriginArtifact = %v, want %s", bug.OriginArtifact, doc.ID)
	}
}

// TestAddCaptureRejectsMissingOriginArtifact pins that a dangling provenance is
// refused rather than silently stored: the grooming agent must name a real
// prompt artifact.
func TestAddCaptureRejectsMissingOriginArtifact(t *testing.T) {
	h := newHarness(t)
	project := h.newProject(t)
	missing := core.ArtifactID("art-nope")

	_, err := h.tasks.Add(context.Background(), TicketInput{
		ProjectID:      project.ID,
		Kind:           core.KindIdea,
		Title:          "Dark mode",
		OriginArtifact: &missing,
	})
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Add(idea) error = %v, want ErrNotFound", err)
	}
}

// TestAddCaptureRejectsForeignOriginArtifact pins that provenance cannot cross
// projects: an artifact from another project is not a valid origin.
func TestAddCaptureRejectsForeignOriginArtifact(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	mine := h.newProject(t)
	other, err := h.projects.Create(ctx, "Other", "other", nil)
	if err != nil {
		t.Fatalf("Create(other) error = %v", err)
	}
	foreign := newPromptDoc(t, h, other, "foreign prompt")

	_, err = h.tasks.Add(ctx, TicketInput{
		ProjectID:      mine.ID,
		Kind:           core.KindIdea,
		Title:          "Dark mode",
		OriginArtifact: &foreign.ID,
	})
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Add(idea) error = %v, want ErrInvalid", err)
	}
}

// TestAddTaskRejectsOriginArtifact pins that only captures carry a prompt
// provenance: an executable task's origin stays its capture edge.
func TestAddTaskRejectsOriginArtifact(t *testing.T) {
	h := newHarness(t)
	project := h.newProject(t)
	doc := newPromptDoc(t, h, project, "Add dark mode")

	_, err := h.tasks.Add(context.Background(), TicketInput{
		ProjectID:      project.ID,
		Kind:           core.KindTask,
		Title:          "Dark mode",
		OriginArtifact: &doc.ID,
	})
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Add(task) error = %v, want ErrInvalid", err)
	}
}
