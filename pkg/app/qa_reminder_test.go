package app

import (
	"context"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// chiefCtx is the caller context a chief uses when it transitions a task.
func chiefCtx() context.Context {
	return WithCaller(context.Background(), core.ActorID("chief"), "sess-1")
}

// remindersTo returns the messages queued to actor for the project.
func remindersTo(t *testing.T, h *harness, project *core.Project, actor core.ActorID) []*core.Message {
	t.Helper()
	address := core.ActorAddress(actor)
	messages, err := h.backend.Messages().List(context.Background(), store.MessageFilter{ProjectID: project.ID, To: &address})
	if err != nil {
		t.Fatalf("List(messages) error = %v", err)
	}
	return messages
}

func TestReminderFiresWhenLastTaskResolvesCaptureWithoutQA(t *testing.T) {
	h := newHarness(t)
	project := h.newProject(t)
	agent := newAgent(t, h, "claude")
	idea := newIdea(t, h, project, "a feature")
	first := promoteTask(t, h, idea, agent)
	second := promoteTask(t, h, idea, agent)
	ctx := chiefCtx()

	if _, err := h.tasks.SetStatus(ctx, first.ID, core.StatusDone); err != nil {
		t.Fatalf("SetStatus(first) error = %v", err)
	}
	if msgs := remindersTo(t, h, project, "chief"); len(msgs) != 0 {
		t.Fatalf("reminder after a non-final task = %v, want none", msgs)
	}

	if _, err := h.tasks.SetStatus(ctx, second.ID, core.StatusDone); err != nil {
		t.Fatalf("SetStatus(second) error = %v", err)
	}
	msgs := remindersTo(t, h, project, "chief")
	if len(msgs) != 1 {
		t.Fatalf("reminders after the final task = %d, want 1", len(msgs))
	}
	if msgs[0].To != core.ActorAddress("chief") {
		t.Fatalf("reminder To = %q, want %q", msgs[0].To, core.ActorAddress("chief"))
	}
	if !strings.Contains(msgs[0].Body, string(idea.ID)) {
		t.Fatalf("reminder body %q does not name capture %s", msgs[0].Body, idea.ID)
	}
	if !strings.Contains(msgs[0].Body, "ft qa") {
		t.Fatalf("reminder body %q does not point at the QA surface", msgs[0].Body)
	}
}

func TestNoReminderWhenCaptureAlreadyPassedQA(t *testing.T) {
	h := newHarness(t)
	project := h.newProject(t)
	agent := newAgent(t, h, "claude")
	idea := newIdea(t, h, project, "a feature")
	first := promoteTask(t, h, idea, agent)
	second := promoteTask(t, h, idea, agent)
	ctx := chiefCtx()

	if _, err := h.tasks.SetStatus(ctx, first.ID, core.StatusDone); err != nil {
		t.Fatalf("SetStatus(first) error = %v", err)
	}
	if _, err := h.tasks.SetStatus(ctx, second.ID, core.StatusDone); err != nil {
		t.Fatalf("SetStatus(second) error = %v", err)
	}
	if _, err := h.qa.Record(ctx, idea.ID, QAPass, "green"); err != nil {
		t.Fatalf("Record(pass) error = %v", err)
	}
	// The capture completes again, but a passing QA already covers it.
	if _, err := h.tasks.SetStatus(ctx, first.ID, core.StatusTodo); err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	if _, err := h.tasks.SetStatus(ctx, first.ID, core.StatusDone); err != nil {
		t.Fatalf("re-done error = %v", err)
	}
	if msgs := remindersTo(t, h, project, "chief"); len(msgs) != 1 {
		t.Fatalf("reminders = %d, want 1 (only the first completion)", len(msgs))
	}
}

func TestNoReminderOnNonCompletingTransition(t *testing.T) {
	h := newHarness(t)
	project := h.newProject(t)
	agent := newAgent(t, h, "claude")
	idea := newIdea(t, h, project, "a feature")
	first := promoteTask(t, h, idea, agent)
	promoteTask(t, h, idea, agent)
	ctx := chiefCtx()

	if _, err := h.tasks.SetStatus(ctx, first.ID, core.StatusReadyForReview); err != nil {
		t.Fatalf("SetStatus(review) error = %v", err)
	}
	if _, err := h.tasks.SetStatus(ctx, first.ID, core.StatusDone); err != nil {
		t.Fatalf("SetStatus(done) error = %v", err)
	}
	if msgs := remindersTo(t, h, project, "chief"); len(msgs) != 0 {
		t.Fatalf("reminder while the capture still has open work = %v, want none", msgs)
	}
}

func TestNoReminderWithoutACaller(t *testing.T) {
	h := newHarness(t)
	project := h.newProject(t)
	agent := newAgent(t, h, "claude")
	idea := newIdea(t, h, project, "a feature")
	task := promoteTask(t, h, idea, agent)

	if _, err := h.tasks.SetStatus(context.Background(), task.ID, core.StatusDone); err != nil {
		t.Fatalf("SetStatus error = %v", err)
	}
	if msgs := remindersTo(t, h, project, "chief"); len(msgs) != 0 {
		t.Fatalf("reminder without a caller = %v, want none", msgs)
	}
}

func TestNoReminderForTaskWithoutCapture(t *testing.T) {
	h := newHarness(t)
	project := h.newProject(t)
	task, err := h.tasks.Add(chiefCtx(), TicketInput{ProjectID: project.ID, Title: "standalone"})
	if err != nil {
		t.Fatalf("Add error = %v", err)
	}
	if _, err := h.tasks.SetStatus(chiefCtx(), task.ID, core.StatusDone); err != nil {
		t.Fatalf("SetStatus error = %v", err)
	}
	if msgs := remindersTo(t, h, project, "chief"); len(msgs) != 0 {
		t.Fatalf("reminder for a task with no capture = %v, want none", msgs)
	}
}

func TestReminderFiresWhenLastTaskIsCancelled(t *testing.T) {
	h := newHarness(t)
	project := h.newProject(t)
	agent := newAgent(t, h, "claude")
	idea := newIdea(t, h, project, "a feature")
	task := promoteTask(t, h, idea, agent)

	if _, err := h.tasks.SetStatus(chiefCtx(), task.ID, core.StatusCancelled); err != nil {
		t.Fatalf("SetStatus(cancelled) error = %v", err)
	}
	if msgs := remindersTo(t, h, project, "chief"); len(msgs) != 1 {
		t.Fatalf("reminders when the last task is cancelled = %d, want 1", len(msgs))
	}
}

func TestReminderIsNotRepeatedWhenAResolvedTaskReResolves(t *testing.T) {
	h := newHarness(t)
	project := h.newProject(t)
	agent := newAgent(t, h, "claude")
	idea := newIdea(t, h, project, "a feature")
	task := promoteTask(t, h, idea, agent)
	ctx := chiefCtx()

	if _, err := h.tasks.SetStatus(ctx, task.ID, core.StatusDone); err != nil {
		t.Fatalf("SetStatus(done) error = %v", err)
	}
	if _, err := h.tasks.SetStatus(ctx, task.ID, core.StatusCancelled); err != nil {
		t.Fatalf("SetStatus(cancelled) error = %v", err)
	}
	if msgs := remindersTo(t, h, project, "chief"); len(msgs) != 1 {
		t.Fatalf("reminders = %d, want 1 (a re-resolve is not a new completion)", len(msgs))
	}
}
