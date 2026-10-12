package up

import (
	"context"
	"testing"

	"github.com/khoinguyen/factotum/internal/groom"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

// TestControllerSequencesGroomReviewBuild proves the controller drives a real
// pipeline through every production stage in order: the groom runner runs a
// session and records its produced task, the review runner records an approving
// verdict, and the build runner drives the produced task to done; the controller
// then advances through the QA and rollout stages to done. Only the agent
// invocations (the groom session, the review, the build dispatch) and the
// not-yet-built QA/rollout stage runners are test doubles - the controller, the
// store, and the session manifest are real.
func TestControllerSequencesGroomReviewBuild(t *testing.T) {
	dataDir := t.TempDir()
	f := newFixture(t, nil)
	ctx := context.Background()

	session := func(ctx context.Context, p *core.Pipeline, sessionID string) (SessionResult, error) {
		task, err := f.tasks.Add(ctx, app.TicketInput{
			ProjectID: p.ProjectID,
			Kind:      core.KindTask,
			Title:     "produced from the capture",
		})
		if err != nil {
			return SessionResult{}, err
		}
		if _, err := f.tasks.AddDep(ctx, task.ID, p.CaptureID); err != nil {
			return SessionResult{}, err
		}
		if err := groom.WriteSession(dataDir, groom.SessionRecord{
			ID:        sessionID,
			Project:   string(p.ProjectID),
			Scope:     []groom.ScopeItem{{ID: string(p.CaptureID), Kind: "idea", Title: "capture"}},
			Produced:  []string{string(task.ID)},
			CreatedAt: f.clock.Now(),
		}); err != nil {
			return SessionResult{}, err
		}
		return SessionResult{Produced: []core.TicketID{task.ID}, Detail: "groomed"}, nil
	}
	qaCalls := 0
	qa := &QARunner{Backend: f.backend, QA: func(ctx context.Context, p *core.Pipeline) (QAOutcome, error) {
		qaCalls++
		if _, err := app.NewQAService(f.backend, f.clock, f.ids).Record(ctx, p.CaptureID, app.QAPass, "qa findings"); err != nil {
			return QAOutcome{}, err
		}
		return QAOutcome{Detail: "qa passed"}, nil
	}}
	buildCalls := 0
	f.controller = mustController(t, f, map[core.PipelineState]StageRunner{
		core.PipelineGrooming: &GroomRunner{DataDir: dataDir, IDs: f.ids, Session: session},
		core.PipelineReview:   &ReviewRunner{DataDir: dataDir, Review: recordVerdict(dataDir, groom.VerdictApprove)},
		core.PipelineBuilding: &BuildRunner{Backend: f.backend, Build: func(ctx context.Context, p *core.Pipeline) (BuildOutcome, error) {
			buildCalls++
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

	capture := f.capture(t, core.KindIdea)
	pipeline, err := f.controller.Enqueue(ctx, capture)
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if err := f.controller.Step(ctx); err != nil {
		t.Fatalf("Step() error = %v", err)
	}

	got := f.pipeline(t, pipeline.ID)
	if got.State != core.PipelineDone {
		t.Fatalf("pipeline state = %q, want done (groom -> review -> build -> qa -> rollout)", got.State)
	}
	if buildCalls != 1 {
		t.Fatalf("build dispatches = %d, want 1", buildCalls)
	}
	if qaCalls != 1 {
		t.Fatalf("qa dispatches = %d, want 1", qaCalls)
	}
	assertEventKinds(t, f,
		core.EventPipelineQueued, core.EventPipelineStageStarted,
		core.EventPipelineStageFinished, core.EventPipelineDone)
}

// TestControllerHoldsPipelineOnDesignRework proves the review gate blocks the
// build: a needs-rework verdict holds the pipeline in review with the blocking
// design-rework gate, and the build, QA, and rollout stages never run.
func TestControllerHoldsPipelineOnDesignRework(t *testing.T) {
	dataDir := t.TempDir()
	f := newFixture(t, nil)
	ctx := context.Background()

	session := func(ctx context.Context, p *core.Pipeline, sessionID string) (SessionResult, error) {
		if err := groom.WriteSession(dataDir, groom.SessionRecord{
			ID:        sessionID,
			Project:   string(p.ProjectID),
			Scope:     []groom.ScopeItem{{ID: string(p.CaptureID), Kind: "idea", Title: "capture"}},
			CreatedAt: f.clock.Now(),
		}); err != nil {
			return SessionResult{}, err
		}
		return SessionResult{Detail: "groomed"}, nil
	}
	build := &stubRunner{result: StageResult{Done: true}}
	qaDispatched := false
	qa := &QARunner{Backend: f.backend, QA: func(context.Context, *core.Pipeline) (QAOutcome, error) {
		qaDispatched = true
		return QAOutcome{}, nil
	}}
	f.controller = mustController(t, f, map[core.PipelineState]StageRunner{
		core.PipelineGrooming: &GroomRunner{DataDir: dataDir, IDs: f.ids, Session: session},
		core.PipelineReview:   &ReviewRunner{DataDir: dataDir, Review: recordVerdict(dataDir, groom.VerdictNeedsRework)},
		core.PipelineBuilding: build,
		core.PipelineQA:       qa,
		core.PipelineRollout:  RolloutRunner{},
	})

	capture := f.capture(t, core.KindIdea)
	pipeline, err := f.controller.Enqueue(ctx, capture)
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if err := f.controller.Step(ctx); err != nil {
		t.Fatalf("Step() error = %v", err)
	}

	got := f.pipeline(t, pipeline.ID)
	if got.State != core.PipelineReview {
		t.Fatalf("pipeline state = %q, want review (held, not advanced)", got.State)
	}
	if got.Gate != core.GateDesignRework {
		t.Fatalf("pipeline gate = %q, want design_rework", got.Gate)
	}
	if build.calls != 0 || qaDispatched {
		t.Fatalf("stages after a blocked review: build=%d qa-dispatched=%v; want none", build.calls, qaDispatched)
	}
	assertEventKinds(t, f, core.EventPipelineGateOpened)
}
