package up

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/internal/groom"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

// parked creates a real capture and a pipeline already parked at a gate, with
// optional produced tasks and milestone, so an operator-verb test starts from a
// realistic durable state. It creates the pipeline in its final state directly,
// so terminal states are seedable too.
func (f *fixture) parked(t *testing.T, state core.PipelineState, gate core.GateKind, milestone *core.TicketID, produced ...core.TicketID) *core.Pipeline {
	t.Helper()
	return f.parkedOn(t, f.capture(t, core.KindIdea).ID, state, gate, milestone, produced...)
}

// parkedOn parks a pipeline for an existing capture.
func (f *fixture) parkedOn(t *testing.T, capture core.TicketID, state core.PipelineState, gate core.GateKind, milestone *core.TicketID, produced ...core.TicketID) *core.Pipeline {
	t.Helper()
	pipeline := &core.Pipeline{
		ID:        core.PipelineID(f.ids.NewID("pl")),
		ProjectID: f.project.ID,
		CaptureID: capture,
		Milestone: milestone,
		State:     state,
		Gate:      gate,
		Produced:  produced,
		CreatedAt: f.clock.Now(),
		UpdatedAt: f.clock.Now(),
	}
	if err := f.backend.Pipelines().Create(context.Background(), pipeline); err != nil {
		t.Fatalf("Create(parked %s/%s) error = %v", state, gate, err)
	}
	return pipeline
}

func TestAnswerRecordsAnswersAndClearsGate(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	pipeline := f.parked(t, core.PipelineReview, core.GateGroomQuestions, nil)

	got, err := f.controller.Answer(ctx, pipeline.ID, "Q: which entry point? A: the CLI.")
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	if got.Gate != core.GateNone {
		t.Fatalf("Answer() gate = %q, want none", got.Gate)
	}

	// The answers are durable on the origin capture.
	ticket := f.task(t, pipeline.CaptureID)
	found := false
	for _, note := range ticket.Notes {
		if strings.Contains(note.Body, "the CLI") {
			found = true
		}
	}
	if !found {
		t.Fatalf("answers not recorded as a note on %s: %+v", pipeline.CaptureID, ticket.Notes)
	}
	assertEventKinds(t, f, core.EventPipelineGateCleared)
}

func TestAnswerRejectsEmptyAndWrongGate(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	pipeline := f.parked(t, core.PipelineReview, core.GateGroomQuestions, nil)

	if _, err := f.controller.Answer(ctx, pipeline.ID, "   "); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Answer(empty) error = %v, want ErrInvalid", err)
	}

	other := f.parked(t, core.PipelineReview, core.GateDesignRework, nil)
	if _, err := f.controller.Answer(ctx, other.ID, "answered"); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("Answer(wrong gate) error = %v, want ErrConflict", err)
	}
}

func TestApproveClearsDesignRework(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	pipeline := f.parked(t, core.PipelineReview, core.GateDesignRework, nil)

	got, err := f.controller.Approve(ctx, pipeline.ID)
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if got.Gate != core.GateNone || got.State != core.PipelineReview {
		t.Fatalf("Approve() = %s/%s, want review with no gate (resume the phase)", got.State, got.Gate)
	}
	assertEventKinds(t, f, core.EventPipelineGateCleared)
}

func TestMergeMarksProducedTasksDoneAndClearsGate(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	capture := f.capture(t, core.KindIdea)
	merged := f.producedTask(t, capture.ID, core.StatusReadyForReview)
	already := f.producedTask(t, capture.ID, core.StatusDone)
	pipeline := f.parked(t, core.PipelineBuilding, core.GateMergeApproval, nil, merged.ID, already.ID)

	got, err := f.controller.Merge(ctx, pipeline.ID)
	if err != nil {
		t.Fatalf("Merge() error = %v", err)
	}
	if got.Gate != core.GateNone {
		t.Fatalf("Merge() gate = %q, want none", got.Gate)
	}
	if status := f.task(t, merged.ID).Status; status != core.StatusDone {
		t.Fatalf("merged task status = %q, want done", status)
	}
	assertEventKinds(t, f, core.EventPipelineGateCleared)
}

func TestMergeRejectsWrongGate(t *testing.T) {
	f := newFixture(t, nil)
	pipeline := f.parked(t, core.PipelineReview, core.GateDesignRework, nil)
	if _, err := f.controller.Merge(context.Background(), pipeline.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("Merge(wrong gate) error = %v, want ErrConflict", err)
	}
}

func TestReleaseClosesMilestoneAndClearsGate(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	milestone := f.milestone(t, "v1")
	pipeline := f.parked(t, core.PipelineRollout, core.GateRelease, &milestone.ID)

	got, err := f.controller.Release(ctx, pipeline.ID)
	if err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if got.Gate != core.GateNone {
		t.Fatalf("Release() gate = %q, want none", got.Gate)
	}
	if status := f.task(t, milestone.ID).Status; status != core.StatusDone {
		t.Fatalf("milestone status = %q, want done", status)
	}
	assertEventKinds(t, f, core.EventPipelineGateCleared)
}

func TestReleaseRequiresMilestone(t *testing.T) {
	f := newFixture(t, nil)
	pipeline := f.parked(t, core.PipelineRollout, core.GateRelease, nil)
	if _, err := f.controller.Release(context.Background(), pipeline.ID); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Release(no milestone) error = %v, want ErrInvalid", err)
	}
}

func TestPromoteClearsRolloutGate(t *testing.T) {
	f := newFixture(t, nil)
	pipeline := f.parked(t, core.PipelineRollout, core.GateRollout, nil)

	got, err := f.controller.Promote(context.Background(), pipeline.ID)
	if err != nil {
		t.Fatalf("Promote() error = %v", err)
	}
	if got.Gate != core.GateNone {
		t.Fatalf("Promote() gate = %q, want none", got.Gate)
	}
}

func TestPauseResumeRestoresPhase(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	pipeline := f.parked(t, core.PipelineBuilding, core.GateNone, nil)

	paused, err := f.controller.Pause(ctx, pipeline.ID)
	if err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	if paused.State != core.PipelinePaused || paused.ResumeState != core.PipelineBuilding {
		t.Fatalf("Pause() = %s/%s, want paused holding building", paused.State, paused.ResumeState)
	}

	resumed, err := f.controller.Resume(ctx, pipeline.ID)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if resumed.State != core.PipelineBuilding || resumed.ResumeState != "" {
		t.Fatalf("Resume() = %s/%s, want building with no resume state", resumed.State, resumed.ResumeState)
	}
}

func TestPauseResumeRejectWrongState(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	active := f.parked(t, core.PipelineBuilding, core.GateNone, nil)
	if _, err := f.controller.Resume(ctx, active.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("Resume(not paused) error = %v, want ErrConflict", err)
	}

	done := f.parked(t, core.PipelineDone, core.GateNone, nil)
	if _, err := f.controller.Pause(ctx, done.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("Pause(terminal) error = %v, want ErrConflict", err)
	}
}

func TestRetryRestoresFailedPhase(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	pipeline := f.parked(t, core.PipelineFailed, core.GateNone, nil)
	pipeline.ResumeState = core.PipelineBuilding
	pipeline.Error = "harness exploded"
	if err := f.backend.Pipelines().Update(ctx, pipeline); err != nil {
		t.Fatalf("Update(failed) error = %v", err)
	}

	got, err := f.controller.Retry(ctx, pipeline.ID)
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if got.State != core.PipelineBuilding || got.ResumeState != "" || got.Error != "" {
		t.Fatalf("Retry() = %s/%s/%q, want building with no resume state and no error", got.State, got.ResumeState, got.Error)
	}
}

func TestRetryRejectsNonFailed(t *testing.T) {
	f := newFixture(t, nil)
	pipeline := f.parked(t, core.PipelineBuilding, core.GateNone, nil)
	if _, err := f.controller.Retry(context.Background(), pipeline.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("Retry(not failed) error = %v, want ErrConflict", err)
	}
}

func TestCancelFromNonTerminalState(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	pipeline := f.parked(t, core.PipelineGrooming, core.GateNone, nil)

	got, err := f.controller.Cancel(ctx, pipeline.ID)
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if got.State != core.PipelineCancelled || got.Gate != core.GateNone {
		t.Fatalf("Cancel() = %s/%s, want cancelled with no gate", got.State, got.Gate)
	}

	if _, err := f.controller.Cancel(ctx, pipeline.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("Cancel(terminal) error = %v, want ErrConflict", err)
	}
}

func TestOperatorRejectsUnknownAndForeignPipeline(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	if _, err := f.controller.Cancel(ctx, core.PipelineID("pl-missing")); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Cancel(missing) error = %v, want ErrNotFound", err)
	}

	foreign := &core.Pipeline{
		ID:        "pl-foreign",
		ProjectID: "prj-other",
		CaptureID: "i-foreign",
		State:     core.PipelineBuilding,
		CreatedAt: f.clock.Now(),
		UpdatedAt: f.clock.Now(),
	}
	if err := f.backend.Pipelines().Create(ctx, foreign); err != nil {
		t.Fatalf("Create(foreign) error = %v", err)
	}
	if _, err := f.controller.Cancel(ctx, foreign.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Cancel(foreign project) error = %v, want ErrNotFound", err)
	}
}

// TestDriveRecordsFailedPhaseForRetry proves a stage failure parks the pipeline
// with the phase it failed in recorded, so `ft up retry` can return it there.
func TestDriveRecordsFailedPhaseForRetry(t *testing.T) {
	runner := &stubRunner{err: errors.New("harness exploded")}
	f := newFixture(t, map[core.PipelineState]StageRunner{core.PipelineBuilding: runner})
	ctx := context.Background()
	pipeline := f.parked(t, core.PipelineBuilding, core.GateNone, nil)

	if err := f.controller.Step(ctx); err != nil {
		t.Fatalf("Step() error = %v", err)
	}
	failed := f.pipeline(t, pipeline.ID)
	if failed.State != core.PipelineFailed || failed.ResumeState != core.PipelineBuilding {
		t.Fatalf("failed pipeline = %s/%s, want failed holding building", failed.State, failed.ResumeState)
	}
}

// TestMergeAdvancesParkedPipeline is the acceptance path: a pipeline live in the
// build stage is parked at merge_approval with a produced task awaiting merge;
// the operator's merge verb marks the task done and clears the gate, and the
// controller's next pass resumes the phase and drives the pipeline to done.
func TestMergeAdvancesParkedPipeline(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	capture := f.capture(t, core.KindIdea)
	task := f.producedTask(t, capture.ID, core.StatusReadyForReview)

	qa := &QARunner{Backend: f.backend, QA: func(ctx context.Context, p *core.Pipeline) (QAOutcome, error) {
		if _, err := app.NewQAService(f.backend, f.clock, f.ids).Record(ctx, p.CaptureID, app.QAPass, "findings"); err != nil {
			return QAOutcome{}, err
		}
		return QAOutcome{Detail: "passed"}, nil
	}}
	f.controller = mustController(t, f, map[core.PipelineState]StageRunner{
		core.PipelineBuilding: &BuildRunner{Backend: f.backend, Build: func(context.Context, *core.Pipeline) (BuildOutcome, error) {
			return BuildOutcome{Detail: "built"}, nil
		}},
		core.PipelineQA:      qa,
		core.PipelineRollout: RolloutRunner{},
	})

	pipeline := f.parkedOn(t, capture.ID, core.PipelineBuilding, core.GateMergeApproval, nil, task.ID)

	// The operator verb runs against the same durable store as the daemon, as
	// the CLI does; the daemon's next pass resumes the cleared phase.
	operator := mustController(t, f, f.controller.stages)
	if _, err := operator.Merge(ctx, pipeline.ID); err != nil {
		t.Fatalf("Merge() error = %v", err)
	}
	if err := f.controller.Step(ctx); err != nil {
		t.Fatalf("Step() after merge error = %v", err)
	}
	if got := f.pipeline(t, pipeline.ID); got.State != core.PipelineDone {
		t.Fatalf("pipeline after merge = %q, want done", got.State)
	}
}

// TestApproveAdvancesParkedPipeline proves the approve verb's whole effect: once
// the operator has re-recorded an approving review (the CLI's side effect), the
// controller clears the design_rework gate and resumes the review, which now
// reads approve and drives the pipeline on to done.
func TestApproveAdvancesParkedPipeline(t *testing.T) {
	dataDir := t.TempDir()
	f := newFixture(t, nil)
	ctx := context.Background()
	capture := f.capture(t, core.KindIdea)
	task := f.producedTask(t, capture.ID, core.StatusTodo)

	qa := &QARunner{Backend: f.backend, QA: func(ctx context.Context, p *core.Pipeline) (QAOutcome, error) {
		if _, err := app.NewQAService(f.backend, f.clock, f.ids).Record(ctx, p.CaptureID, app.QAPass, "findings"); err != nil {
			return QAOutcome{}, err
		}
		return QAOutcome{Detail: "passed"}, nil
	}}
	f.controller = mustController(t, f, map[core.PipelineState]StageRunner{
		core.PipelineReview: &ReviewRunner{DataDir: dataDir},
		core.PipelineBuilding: &BuildRunner{Backend: f.backend, Build: func(ctx context.Context, p *core.Pipeline) (BuildOutcome, error) {
			for _, id := range p.Produced {
				if _, err := f.tasks.SetStatus(ctx, id, core.StatusDone); err != nil {
					return BuildOutcome{}, err
				}
			}
			return BuildOutcome{Detail: "built"}, nil
		}},
		core.PipelineQA:      qa,
		core.PipelineRollout: RolloutRunner{},
	})

	// The re-recorded approving review (what `ft up approve` writes first).
	if err := groom.WriteSession(dataDir, groom.SessionRecord{
		ID:            "groom-1",
		Project:       string(f.project.ID),
		Scope:         []groom.ScopeItem{{ID: string(capture.ID), Kind: string(core.KindIdea), Title: "capture"}},
		Produced:      []string{string(task.ID)},
		ReviewVerdict: string(groom.VerdictApprove),
		CreatedAt:     f.clock.Now(),
	}); err != nil {
		t.Fatalf("WriteSession() error = %v", err)
	}

	pipeline := f.parkedOn(t, capture.ID, core.PipelineReview, core.GateDesignRework, nil, task.ID)
	pipeline.Session = "groom-1"
	if err := f.backend.Pipelines().Update(ctx, pipeline); err != nil {
		t.Fatalf("Update(session) error = %v", err)
	}

	if _, err := f.controller.Approve(ctx, pipeline.ID); err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if err := f.controller.Step(ctx); err != nil {
		t.Fatalf("Step() after approve error = %v", err)
	}
	if got := f.pipeline(t, pipeline.ID); got.State != core.PipelineDone {
		t.Fatalf("pipeline after approve = %q, want done", got.State)
	}
}

// TestMergeLeavesUnbuiltProducedTask proves merge marks only the tasks awaiting
// review: an unbuilt produced task is left open, so the resumed build stalls
// (parks failed) rather than merging unfinished work.
func TestMergeLeavesUnbuiltProducedTask(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	capture := f.capture(t, core.KindIdea)
	ready := f.producedTask(t, capture.ID, core.StatusReadyForReview)
	unbuilt := f.producedTask(t, capture.ID, core.StatusTodo)
	f.controller = mustController(t, f, map[core.PipelineState]StageRunner{
		core.PipelineBuilding: &BuildRunner{Backend: f.backend, Build: func(context.Context, *core.Pipeline) (BuildOutcome, error) {
			return BuildOutcome{Detail: "built"}, nil
		}},
	})
	pipeline := f.parkedOn(t, capture.ID, core.PipelineBuilding, core.GateMergeApproval, nil, ready.ID, unbuilt.ID)

	if _, err := f.controller.Merge(ctx, pipeline.ID); err != nil {
		t.Fatalf("Merge() error = %v", err)
	}
	if status := f.task(t, ready.ID).Status; status != core.StatusDone {
		t.Fatalf("awaiting-merge task status = %q, want done", status)
	}
	if status := f.task(t, unbuilt.ID).Status; status != core.StatusTodo {
		t.Fatalf("unbuilt task status = %q, want todo (left alone)", status)
	}

	if err := f.controller.Step(ctx); err != nil {
		t.Fatalf("Step() after merge error = %v", err)
	}
	if got := f.pipeline(t, pipeline.ID); got.State != core.PipelineFailed {
		t.Fatalf("pipeline with an unbuilt produced task = %q, want failed (build stalled)", got.State)
	}
}

func (f *fixture) producedTask(t *testing.T, capture core.TicketID, status core.TicketStatus) *core.Ticket {
	t.Helper()
	ctx := context.Background()
	task, err := f.tasks.Add(ctx, app.TicketInput{ProjectID: f.project.ID, Kind: core.KindTask, Title: "produced"})
	if err != nil {
		t.Fatalf("Add(produced) error = %v", err)
	}
	if _, err := f.tasks.AddDep(ctx, task.ID, capture); err != nil {
		t.Fatalf("AddDep(produced) error = %v", err)
	}
	if status != core.StatusTodo {
		if _, err := f.tasks.SetStatus(ctx, task.ID, status); err != nil {
			t.Fatalf("SetStatus(%s) error = %v", status, err)
		}
	}
	return f.task(t, task.ID)
}

func (f *fixture) task(t *testing.T, id core.TicketID) *core.Ticket {
	t.Helper()
	task, err := f.backend.Tickets().Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get(%s) error = %v", id, err)
	}
	return task
}

func (f *fixture) milestone(t *testing.T, title string) *core.Ticket {
	t.Helper()
	task, err := f.tasks.Add(context.Background(), app.TicketInput{ProjectID: f.project.ID, Kind: core.KindMilestone, Title: title})
	if err != nil {
		t.Fatalf("Add(milestone) error = %v", err)
	}
	return task
}
