package app

import (
	"context"
	"errors"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
)

// promoteTask creates an executable task linked to the capture as its origin,
// the shape every groomed feature task has.
func promoteTask(t *testing.T, h *harness, capture *core.Ticket, agent *core.Actor) *core.Ticket {
	t.Helper()
	task, err := h.tasks.Promote(context.Background(), capture.ID, groomedPromote(agent))
	if err != nil {
		t.Fatalf("Promote(%s) error = %v", capture.ID, err)
	}
	return task
}

func qaReadyIDs(t *testing.T, h *harness, project *core.Project) []core.TicketID {
	t.Helper()
	ready, err := h.qa.Ready(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("Ready error = %v", err)
	}
	ids := make([]core.TicketID, 0, len(ready))
	for _, capture := range ready {
		ids = append(ids, capture.ID)
	}
	return ids
}

func TestQAReadyListsFeatureWithEveryTaskResolved(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	agent := newAgent(t, h, "claude")
	idea := newIdea(t, h, project, "a feature")
	first := promoteTask(t, h, idea, agent)
	second := promoteTask(t, h, idea, agent)

	if ready := qaReadyIDs(t, h, project); len(ready) != 0 {
		t.Fatalf("Ready with open tasks = %v, want none", ready)
	}
	for _, task := range []*core.Ticket{first, second} {
		if _, err := h.tasks.SetStatus(ctx, task.ID, core.StatusDone); err != nil {
			t.Fatalf("SetStatus(%s) error = %v", task.ID, err)
		}
	}
	if ready := qaReadyIDs(t, h, project); len(ready) != 1 || ready[0] != idea.ID {
		t.Fatalf("Ready with all tasks resolved = %v, want [%s]", ready, idea.ID)
	}
}

func TestQAReadyExcludesPassedFeature(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	agent := newAgent(t, h, "claude")
	idea := newIdea(t, h, project, "a feature")
	task := promoteTask(t, h, idea, agent)
	if _, err := h.tasks.SetStatus(ctx, task.ID, core.StatusDone); err != nil {
		t.Fatalf("SetStatus error = %v", err)
	}
	if _, err := h.qa.Record(ctx, idea.ID, QAPass, "all green"); err != nil {
		t.Fatalf("Record(pass) error = %v", err)
	}
	if ready := qaReadyIDs(t, h, project); len(ready) != 0 {
		t.Fatalf("Ready after a pass = %v, want none", ready)
	}
}

func TestQAReadyExcludesCaptureWithoutTasks(t *testing.T) {
	h := newHarness(t)
	project := h.newProject(t)
	_ = newIdea(t, h, project, "a bare spark")
	if ready := qaReadyIDs(t, h, project); len(ready) != 0 {
		t.Fatalf("Ready for a capture with no tasks = %v, want none", ready)
	}
}

func TestQARecordPassRequiresEveryTaskDone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	agent := newAgent(t, h, "claude")
	idea := newIdea(t, h, project, "a feature")
	task := promoteTask(t, h, idea, agent)

	if _, err := h.qa.Record(ctx, idea.ID, QAPass, "looks good"); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("Record(pass) with open task error = %v, want ErrConflict", err)
	}
	_ = task
}

func TestQARecordFailRecordsButDoesNotPass(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	agent := newAgent(t, h, "claude")
	idea := newIdea(t, h, project, "a feature")
	_ = promoteTask(t, h, idea, agent)

	if _, err := h.qa.Record(ctx, idea.ID, QAFail, "crashes on submit"); err != nil {
		t.Fatalf("Record(fail) error = %v", err)
	}
	passed, err := h.qa.Passed(ctx, idea)
	if err != nil {
		t.Fatalf("Passed error = %v", err)
	}
	if passed {
		t.Fatal("a failing QA must not read as passed")
	}
}

func TestQARecordRejectsNonCapture(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	task, err := h.tasks.Add(ctx, TicketInput{ProjectID: project.ID, Title: "work"})
	if err != nil {
		t.Fatalf("Add(task) error = %v", err)
	}
	if _, err := h.qa.Record(ctx, task.ID, QAPass, "n/a"); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Record on a task error = %v, want ErrInvalid", err)
	}
}

func TestAcceptCaptureRequiresAPassingQA(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	agent := newAgent(t, h, "claude")
	idea := newIdea(t, h, project, "a feature")
	task := promoteTask(t, h, idea, agent)
	if _, err := h.tasks.SetStatus(ctx, task.ID, core.StatusDone); err != nil {
		t.Fatalf("SetStatus error = %v", err)
	}

	if _, err := h.tasks.SetStatus(ctx, idea.ID, core.StatusDone); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("accept without QA error = %v, want ErrConflict", err)
	}
	if _, err := h.qa.Record(ctx, idea.ID, QAPass, "verified"); err != nil {
		t.Fatalf("Record(pass) error = %v", err)
	}
	accepted, err := h.tasks.SetStatus(ctx, idea.ID, core.StatusDone)
	if err != nil {
		t.Fatalf("accept with QA error = %v", err)
	}
	if accepted.Status != core.StatusDone {
		t.Fatalf("accepted status = %q, want done", accepted.Status)
	}
}

func TestAcceptCaptureRejectsUnresolvedTaskEvenWithAStalePass(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	agent := newAgent(t, h, "claude")
	idea := newIdea(t, h, project, "a feature")
	_ = promoteTask(t, h, idea, agent)

	// A pass recorded earlier cannot mask a task that reopened afterwards.
	if _, err := h.artifacts.Add(ctx, ArtifactInput{
		ProjectID: project.ID,
		TicketID:  &idea.ID,
		Kind:      core.ArtifactQA,
		Title:     "QA pass " + string(idea.ID),
		Brief:     string(QAPass),
	}); err != nil {
		t.Fatalf("Add(qa artifact) error = %v", err)
	}
	if _, err := h.tasks.SetStatus(ctx, idea.ID, core.StatusDone); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("accept with an unresolved task error = %v, want ErrConflict", err)
	}
}

func TestAcceptCaptureWithoutTasksIsUnchanged(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	idea := newIdea(t, h, project, "nothing to build")
	accepted, err := h.tasks.SetStatus(ctx, idea.ID, core.StatusDone)
	if err != nil {
		t.Fatalf("accept a capture with no tasks error = %v", err)
	}
	if accepted.Status != core.StatusDone {
		t.Fatalf("status = %q, want done", accepted.Status)
	}
}
