package up

import (
	"context"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

// qaPipeline seeds a capture whose produced task is done, and returns the
// pipeline plus a QA dispatch stub that records a passing verdict.
func qaPipeline(t *testing.T, f *fixture, verdict app.QAVerdict) (*core.Pipeline, QAFunc) {
	t.Helper()
	ctx := context.Background()
	capture := f.capture(t, core.KindIdea)
	task, err := f.tasks.Add(ctx, app.TicketInput{
		ProjectID: f.project.ID,
		Kind:      core.KindTask,
		Title:     "built task",
	})
	if err != nil {
		t.Fatalf("Add(task) error = %v", err)
	}
	if _, err := f.tasks.AddDep(ctx, task.ID, capture.ID); err != nil {
		t.Fatalf("AddDep() error = %v", err)
	}
	if _, err := f.tasks.SetStatus(ctx, task.ID, core.StatusDone); err != nil {
		t.Fatalf("SetStatus(done) error = %v", err)
	}
	pipeline := &core.Pipeline{
		ID:        "pl-qa",
		ProjectID: f.project.ID,
		CaptureID: capture.ID,
		State:     core.PipelineQA,
	}
	if err := f.backend.Pipelines().Create(ctx, pipeline); err != nil {
		t.Fatalf("Create(pipeline) error = %v", err)
	}
	qa := func(ctx context.Context, p *core.Pipeline) (QAOutcome, error) {
		if _, err := app.NewQAService(f.backend, f.clock, f.ids).Record(ctx, p.CaptureID, verdict, "findings"); err != nil {
			return QAOutcome{}, err
		}
		return QAOutcome{Detail: "qa " + string(verdict)}, nil
	}
	return pipeline, qa
}

// TestQARunnerAdvancesOnPass proves the QA stage advances once a passing verdict
// is recorded for the capture.
func TestQARunnerAdvancesOnPass(t *testing.T) {
	f := newFixture(t, nil)
	pipeline, qa := qaPipeline(t, f, app.QAPass)
	runner := &QARunner{Backend: f.backend, QA: qa}
	result, err := runner.Run(context.Background(), pipeline)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.Done || result.Gate != core.GateNone {
		t.Fatalf("Run() = %+v, want done with no gate", result)
	}
}

// TestQARunnerReusesRecordedPass proves idempotency: a capture that already
// passed QA is not QA'd again on a restart.
func TestQARunnerReusesRecordedPass(t *testing.T) {
	f := newFixture(t, nil)
	pipeline, qa := qaPipeline(t, f, app.QAPass)
	if _, err := app.NewQAService(f.backend, f.clock, f.ids).Record(context.Background(), pipeline.CaptureID, app.QAPass, "pre-recorded"); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	dispatched := false
	runner := &QARunner{Backend: f.backend, QA: func(context.Context, *core.Pipeline) (QAOutcome, error) {
		dispatched = true
		return QAOutcome{}, nil
	}}
	result, err := runner.Run(context.Background(), pipeline)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if dispatched {
		t.Fatal("Run() dispatched QA despite a recorded pass")
	}
	if !result.Done {
		t.Fatalf("Run().Done = false, want true")
	}
	_ = qa
}

// TestQARunnerFailsWhenNotPassed proves the stage fails rather than advancing
// when no passing verdict is recorded: an unverified feature never reaches
// rollout.
func TestQARunnerFailsWhenNotPassed(t *testing.T) {
	f := newFixture(t, nil)
	pipeline, _ := qaPipeline(t, f, app.QAPass)
	runner := &QARunner{Backend: f.backend, QA: func(context.Context, *core.Pipeline) (QAOutcome, error) {
		return QAOutcome{}, nil
	}}
	if _, err := runner.Run(context.Background(), pipeline); err == nil {
		t.Fatal("Run() with no recorded pass error = nil, want an error")
	}
}

// TestRolloutRunnerIsANoop proves the rollout stage advances to done with no
// rollout configured (design §9).
func TestRolloutRunnerIsANoop(t *testing.T) {
	result, err := RolloutRunner{}.Run(context.Background(), &core.Pipeline{ID: "pl-1"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.Done || result.Gate != core.GateNone {
		t.Fatalf("Run() = %+v, want done with no gate", result)
	}
}
