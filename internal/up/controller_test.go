package up

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/memory"
)

type testClock struct{ t time.Time }

func (c *testClock) Now() time.Time { return c.t }

type seqIDs struct{ n int }

func (s *seqIDs) NewID(prefix string) string {
	s.n++
	return fmt.Sprintf("%s-%d", prefix, s.n)
}

// stubRunner is a StageRunner test double: it records each call and returns a
// scripted result, so a test can assert the controller drove (or did not drive)
// a stage.
type stubRunner struct {
	calls  int
	last   *core.Pipeline
	result StageResult
	err    error
}

func (r *stubRunner) Run(_ context.Context, p *core.Pipeline) (StageResult, error) {
	r.calls++
	clone := *p
	r.last = &clone
	if r.err != nil {
		return StageResult{}, r.err
	}
	return r.result, nil
}

type fixture struct {
	backend    store.Backend
	clock      *testClock
	ids        *seqIDs
	project    *core.Project
	projects   *app.ProjectService
	tasks      *app.TicketService
	actors     *app.ActorService
	messages   *app.MessageService
	controller *Controller
}

func newFixture(t *testing.T, stages map[core.PipelineState]StageRunner) *fixture {
	t.Helper()
	backend := memory.New()
	clock := &testClock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	ids := &seqIDs{}
	t.Cleanup(func() { _ = backend.Close() })

	projects := app.NewProjectService(backend, clock, ids)
	tasks := app.NewTicketService(backend, clock, ids)
	actors := app.NewActorService(backend, clock, ids)
	messages := app.NewMessageService(backend, clock, ids)

	project, err := projects.Create(context.Background(), "Acme", "demo", nil)
	if err != nil {
		t.Fatalf("Create(project) error = %v", err)
	}
	controller, err := New(Options{
		Backend: backend,
		Clock:   clock,
		IDs:     ids,
		Project: project.ID,
		Stages:  stages,
		Logf:    func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("New(controller) error = %v", err)
	}
	return &fixture{
		backend:    backend,
		clock:      clock,
		ids:        ids,
		project:    project,
		projects:   projects,
		tasks:      tasks,
		actors:     actors,
		messages:   messages,
		controller: controller,
	}
}

func (f *fixture) capture(t *testing.T, kind core.TicketKind, labels ...string) *core.Ticket {
	t.Helper()
	ticket, err := f.tasks.Add(context.Background(), app.TicketInput{
		ProjectID:   f.project.ID,
		Kind:        kind,
		Title:       "capture",
		Description: "a captured sentence",
		Labels:      labels,
	})
	if err != nil {
		t.Fatalf("Add(capture) error = %v", err)
	}
	return ticket
}

// seedPipeline creates a pipeline directly in state, so a test can drive a
// phase that the groom-only controller does not otherwise reach.
func (f *fixture) seedPipeline(t *testing.T, state core.PipelineState) *core.Pipeline {
	t.Helper()
	now := f.clock.Now()
	pipeline := &core.Pipeline{
		ID:        core.PipelineID(f.ids.NewID("pl")),
		ProjectID: f.project.ID,
		CaptureID: core.TicketID(f.ids.NewID("i")),
		State:     state,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := f.backend.Pipelines().Create(context.Background(), pipeline); err != nil {
		t.Fatalf("Create(%s) error = %v", state, err)
	}
	return pipeline
}

func (f *fixture) pipeline(t *testing.T, id core.PipelineID) *core.Pipeline {
	t.Helper()
	pipeline, err := f.backend.Pipelines().Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get(%s) error = %v", id, err)
	}
	return pipeline
}

// TestEnqueueSkipsExcludedCaptures covers the design §4 skip rules: an already
// executable ticket, a no-auto label, and an already-promoted capture each start
// no pipeline; a fresh idea gets one, and re-enqueuing is idempotent.
func TestEnqueueSkipsExcludedCaptures(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()

	if pipeline, err := f.controller.Enqueue(ctx, f.capture(t, core.KindTask)); err != nil || pipeline != nil {
		t.Fatalf("Enqueue(executable) = %v, %v; want nil, nil", pipeline, err)
	}
	if pipeline, err := f.controller.Enqueue(ctx, f.capture(t, core.KindIdea, noAutoLabel)); err != nil || pipeline != nil {
		t.Fatalf("Enqueue(no-auto) = %v, %v; want nil, nil", pipeline, err)
	}

	promoted := f.capture(t, core.KindIdea)
	task, err := f.tasks.Add(ctx, app.TicketInput{
		ProjectID: f.project.ID,
		Kind:      core.KindTask,
		Title:     "promoted task",
	})
	if err != nil {
		t.Fatalf("Add(promoted task) error = %v", err)
	}
	if _, err := f.tasks.AddDep(ctx, task.ID, promoted.ID); err != nil {
		t.Fatalf("AddDep() error = %v", err)
	}
	if pipeline, err := f.controller.Enqueue(ctx, promoted); err != nil || pipeline != nil {
		t.Fatalf("Enqueue(promoted) = %v, %v; want nil, nil", pipeline, err)
	}

	fresh := f.capture(t, core.KindIdea)
	first, err := f.controller.Enqueue(ctx, fresh)
	if err != nil || first == nil {
		t.Fatalf("Enqueue(fresh) = %v, %v; want a pipeline", first, err)
	}
	if first.State != core.PipelineQueued {
		t.Fatalf("Enqueue(fresh).State = %q, want queued", first.State)
	}
	second, err := f.controller.Enqueue(ctx, fresh)
	if err != nil || second == nil || second.ID != first.ID {
		t.Fatalf("Enqueue(fresh) again = %v, %v; want the same pipeline %s", second, err, first.ID)
	}
}

// TestStepGroomsQueuedCapture drives a stored capture through the controller to
// the groom stage: the queued pipeline is claimed, the groom runner runs, and
// the pipeline advances to review with the session and produced tasks recorded.
func TestStepGroomsQueuedCapture(t *testing.T) {
	runner := &stubRunner{result: StageResult{
		Done:     true,
		Session:  "groom-42",
		Produced: []core.TicketID{"t-1", "t-2"},
		Detail:   "groomed",
	}}
	f := newFixture(t, map[core.PipelineState]StageRunner{core.PipelineGrooming: runner})
	ctx := context.Background()

	capture := f.capture(t, core.KindIdea)
	pipeline, err := f.controller.Enqueue(ctx, capture)
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	if err := f.controller.Step(ctx); err != nil {
		t.Fatalf("Step() error = %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}
	if runner.last.CaptureID != capture.ID {
		t.Fatalf("runner capture = %s, want %s", runner.last.CaptureID, capture.ID)
	}

	got := f.pipeline(t, pipeline.ID)
	if got.State != core.PipelineReview {
		t.Fatalf("pipeline state = %q, want review", got.State)
	}
	if got.Session != "groom-42" || len(got.Produced) != 2 || got.Produced[1] != "t-2" {
		t.Fatalf("pipeline = %+v, want session groom-42 and produced [t-1 t-2]", got)
	}

	assertEventKinds(t, f, core.EventPipelineQueued, core.EventPipelineStageStarted, core.EventPipelineStageFinished)
}

// TestStepReattachesLiveExternalRun proves the restart re-attach path against a
// real registered run: while the pipeline's recorded run is live, the
// controller does not re-dispatch the stage; once the run's lease lapses, it
// re-runs the stage. The run is a real core.Run in the store, not a fake.
func TestStepReattachesLiveExternalRun(t *testing.T) {
	runner := &stubRunner{result: StageResult{Done: true, Session: "groom-1"}}
	f := newFixture(t, map[core.PipelineState]StageRunner{core.PipelineGrooming: runner})
	ctx := context.Background()

	actor, err := f.actors.Add(ctx, core.ActorAgent, "groomer")
	if err != nil {
		t.Fatalf("Add(actor) error = %v", err)
	}
	run, err := f.messages.RegisterRun(ctx, app.RegisterRunInput{
		ProjectID: f.project.ID,
		ActorID:   actor.ID,
		Harness:   "opencode",
		TTL:       time.Minute,
	})
	if err != nil {
		t.Fatalf("RegisterRun() error = %v", err)
	}

	capture := f.capture(t, core.KindIdea)
	pipeline, err := f.controller.Enqueue(ctx, capture)
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	// Simulate a controller that died mid-groom: the pipeline is in grooming
	// with its external run recorded.
	claimed, err := f.backend.Pipelines().Claim(ctx, store.PipelineClaimRequest{ProjectID: f.project.ID, ID: &pipeline.ID})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	claimed.RunID = run.ID
	if err := f.backend.Pipelines().Update(ctx, claimed); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	// The run is live: the controller re-attaches, so the stage does not re-run.
	if err := f.controller.Step(ctx); err != nil {
		t.Fatalf("Step() error = %v", err)
	}
	if runner.calls != 0 {
		t.Fatalf("runner calls with a live run = %d, want 0 (re-attached)", runner.calls)
	}
	if got := f.pipeline(t, pipeline.ID); got.State != core.PipelineGrooming {
		t.Fatalf("pipeline state with a live run = %q, want grooming", got.State)
	}

	// The run's lease lapses: the next pass re-runs the stage.
	f.clock.t = f.clock.t.Add(2 * time.Minute)
	if err := f.controller.Step(ctx); err != nil {
		t.Fatalf("Step() after lease error = %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls after lease lapse = %d, want 1 (re-run)", runner.calls)
	}
	if got := f.pipeline(t, pipeline.ID); got.State != core.PipelineReview {
		t.Fatalf("pipeline state after re-run = %q, want review", got.State)
	}
}

// TestStepHoldsPhaseOnBlockingGate proves a blocking gate holds the phase: a
// stage that finished with a blocking gate leaves the pipeline in its phase
// (design §5), so clearing the gate resumes the same stage rather than the next
// one.
func TestStepHoldsPhaseOnBlockingGate(t *testing.T) {
	runner := &stubRunner{result: StageResult{Done: true, Gate: core.GateDesignRework}}
	f := newFixture(t, map[core.PipelineState]StageRunner{core.PipelineReview: runner})
	ctx := context.Background()

	pipeline := f.seedPipeline(t, core.PipelineReview)
	if err := f.controller.Step(ctx); err != nil {
		t.Fatalf("Step() error = %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}
	got := f.pipeline(t, pipeline.ID)
	if got.State != core.PipelineReview {
		t.Fatalf("state with a blocking gate = %q, want review (held, not advanced)", got.State)
	}
	if got.Gate != core.GateDesignRework {
		t.Fatalf("gate = %q, want design_rework", got.Gate)
	}
}

// TestStepAdvancesPastAdvisoryGate proves an advisory gate does not hold the
// phase: the pipeline advances while the question is surfaced.
func TestStepAdvancesPastAdvisoryGate(t *testing.T) {
	runner := &stubRunner{result: StageResult{
		Done:    true,
		Gate:    core.GateGroomQuestions,
		Session: "groom-1",
	}}
	f := newFixture(t, map[core.PipelineState]StageRunner{core.PipelineGrooming: runner})
	ctx := context.Background()

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
		t.Fatalf("state with an advisory gate = %q, want review (advanced)", got.State)
	}
	if got.Gate != core.GateGroomQuestions {
		t.Fatalf("gate = %q, want groom_questions", got.Gate)
	}
}

// TestStepRecordsExternalRunHandoff covers the !Done hand-off: the stage hands
// its work to an external agent run, which the controller records and leaves the
// pipeline in place for. A later pass re-runs the stage because no such run is
// live.
func TestStepRecordsExternalRunHandoff(t *testing.T) {
	runner := &stubRunner{result: StageResult{Done: false, RunID: "run-ext"}}
	f := newFixture(t, map[core.PipelineState]StageRunner{core.PipelineGrooming: runner})
	ctx := context.Background()

	capture := f.capture(t, core.KindIdea)
	pipeline, err := f.controller.Enqueue(ctx, capture)
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if err := f.controller.Step(ctx); err != nil {
		t.Fatalf("Step() error = %v", err)
	}
	got := f.pipeline(t, pipeline.ID)
	if got.State != core.PipelineGrooming || got.RunID != "run-ext" {
		t.Fatalf("after hand-off = %+v, want grooming with run-ext recorded", got)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}

	// No run-ext is live, so the next pass re-runs the stage.
	if err := f.controller.Step(ctx); err != nil {
		t.Fatalf("Step() error = %v", err)
	}
	if runner.calls != 2 {
		t.Fatalf("runner calls after a dead hand-off = %d, want 2 (re-run)", runner.calls)
	}
}

// TestStepLeavesInterruptedStageInFlight proves a stage interrupted by context
// cancellation is not parked failed: the pipeline stays in its in-flight phase
// so a restart re-owns it (design §3/§5). Parking it failed would strand it,
// since failed is not auto-resumed.
func TestStepLeavesInterruptedStageInFlight(t *testing.T) {
	runner := &ctxRunner{}
	f := newFixture(t, map[core.PipelineState]StageRunner{core.PipelineGrooming: runner})
	ctx := context.Background()

	capture := f.capture(t, core.KindIdea)
	pipeline, err := f.controller.Enqueue(ctx, capture)
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_ = f.controller.Step(canceled) // the interrupted stage returns ctx.Err()

	got := f.pipeline(t, pipeline.ID)
	if got.State == core.PipelineFailed {
		t.Fatal("an interrupted stage must not park the pipeline failed")
	}
	if got.State != core.PipelineGrooming {
		t.Fatalf("state after interruption = %q, want grooming (re-ownable)", got.State)
	}
}

// ctxRunner is a StageRunner that reports the context's error, so a test can
// simulate a stage interrupted by shutdown.
type ctxRunner struct{ calls int }

func (r *ctxRunner) Run(ctx context.Context, _ *core.Pipeline) (StageResult, error) {
	r.calls++
	return StageResult{}, ctx.Err()
}

// TestStepParksFailedStage proves a stage error parks the pipeline as failed
// with the reason, instead of aborting the supervisor.
func TestStepParksFailedStage(t *testing.T) {
	runner := &stubRunner{err: fmt.Errorf("harness exploded")}
	f := newFixture(t, map[core.PipelineState]StageRunner{core.PipelineGrooming: runner})
	ctx := context.Background()

	capture := f.capture(t, core.KindIdea)
	pipeline, err := f.controller.Enqueue(ctx, capture)
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if err := f.controller.Step(ctx); err != nil {
		t.Fatalf("Step() error = %v", err)
	}
	got := f.pipeline(t, pipeline.ID)
	if got.State != core.PipelineFailed {
		t.Fatalf("pipeline state = %q, want failed", got.State)
	}
	if got.Error != "harness exploded" {
		t.Fatalf("pipeline error = %q, want the stage error", got.Error)
	}
	assertEventKinds(t, f, core.EventPipelineFailed)
}

func assertEventKinds(t *testing.T, f *fixture, kinds ...core.EventKind) {
	t.Helper()
	events, err := f.backend.Events().List(context.Background(), store.EventFilter{ProjectID: f.project.ID})
	if err != nil {
		t.Fatalf("List(events) error = %v", err)
	}
	seen := map[core.EventKind]bool{}
	for _, event := range events {
		seen[event.Kind] = true
	}
	for _, kind := range kinds {
		if !seen[kind] {
			t.Errorf("event kind %q not recorded; got %v", kind, seen)
		}
	}
}
