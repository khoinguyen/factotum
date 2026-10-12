package up

import (
	"context"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

// buildPipeline seeds a pipeline whose produced tasks have the given statuses,
// and returns it alongside a no-op build dispatch.
func buildPipeline(t *testing.T, f *fixture, statuses ...core.TicketStatus) (*core.Pipeline, BuildFunc) {
	t.Helper()
	produced := make([]core.TicketID, 0, len(statuses))
	for i, status := range statuses {
		task, err := f.tasks.Add(context.Background(), app.TicketInput{
			ProjectID: f.project.ID,
			Kind:      core.KindTask,
			Title:     "built task",
		})
		if err != nil {
			t.Fatalf("Add(task %d) error = %v", i, err)
		}
		if status != core.StatusTodo {
			if _, err := f.tasks.SetStatus(context.Background(), task.ID, status); err != nil {
				t.Fatalf("SetStatus(%s) error = %v", status, err)
			}
		}
		produced = append(produced, task.ID)
	}
	pipeline := f.seedPipeline(t, core.PipelineBuilding)
	pipeline.Produced = produced
	return pipeline, func(context.Context, *core.Pipeline) (BuildOutcome, error) {
		return BuildOutcome{Detail: "built"}, nil
	}
}

// TestBuildRunnerAdvancesWhenTasksDone proves the build stage advances to QA
// once every produced task is done.
func TestBuildRunnerAdvancesWhenTasksDone(t *testing.T) {
	f := newFixture(t, nil)
	pipeline, build := buildPipeline(t, f, core.StatusDone, core.StatusDone)
	runner := &BuildRunner{Backend: f.backend, Build: build}
	result, err := runner.Run(context.Background(), pipeline)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.Done || result.Gate != core.GateNone {
		t.Fatalf("Run() = %+v, want done with no gate", result)
	}
}

// TestBuildRunnerHoldsOnMergeGate proves the build stage preserves the merge
// gate: a produced task that is ready_for_review (its PR open, unmerged) holds
// the phase with the blocking merge_approval gate instead of advancing to QA.
func TestBuildRunnerHoldsOnMergeGate(t *testing.T) {
	f := newFixture(t, nil)
	pipeline, build := buildPipeline(t, f, core.StatusDone, core.StatusReadyForReview)
	runner := &BuildRunner{Backend: f.backend, Build: build}
	result, err := runner.Run(context.Background(), pipeline)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.Done {
		t.Fatal("Run().Done = false, want true (the stage finished, the gate holds it)")
	}
	if result.Gate != core.GateMergeApproval {
		t.Fatalf("Run().Gate = %q, want merge_approval", result.Gate)
	}
	if !result.Gate.Blocking() {
		t.Fatal("merge_approval must block the advance to QA")
	}
}

// TestBuildRunnerStallsOnOpenTask proves the build stage fails (parks, for a
// human) rather than advancing when a produced task is neither done nor
// awaiting merge: the loop could not drive it.
func TestBuildRunnerStallsOnOpenTask(t *testing.T) {
	f := newFixture(t, nil)
	pipeline, build := buildPipeline(t, f, core.StatusTodo)
	runner := &BuildRunner{Backend: f.backend, Build: build}
	if _, err := runner.Run(context.Background(), pipeline); err == nil {
		t.Fatal("Run() with an open task error = nil, want an error")
	}
}

// TestBuildRunnerNeedsProduced proves a pipeline with no produced tasks is
// refused: there is nothing to build.
func TestBuildRunnerNeedsProduced(t *testing.T) {
	f := newFixture(t, nil)
	called := false
	runner := &BuildRunner{
		Backend: f.backend,
		Build: func(context.Context, *core.Pipeline) (BuildOutcome, error) {
			called = true
			return BuildOutcome{}, nil
		},
	}
	if _, err := runner.Run(context.Background(), f.seedPipeline(t, core.PipelineBuilding)); err == nil {
		t.Fatal("Run() with no produced tasks error = nil, want an error")
	}
	if called {
		t.Fatal("Run() dispatched a build despite no produced tasks")
	}
}
