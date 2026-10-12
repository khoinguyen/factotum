package up

import (
	"context"
	"errors"
	"fmt"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// BuildOutcome is what one build dispatch did; Detail is a one-line summary for
// the event log.
type BuildOutcome struct {
	Detail string
}

// BuildFunc drives a pipeline's produced tasks toward done and returns when the
// build loop stops or the external build run finishes. The production
// implementation runs the build loop (or dispatches the chief), which sets each
// built task ready_for_review for a human to merge. A test supplies a stub that
// sets the task statuses the runner reads back.
type BuildFunc func(ctx context.Context, p *core.Pipeline) (BuildOutcome, error)

// BuildRunner is the build stage runner. It dispatches the build, then reads the
// durable task state to decide the stage's outcome, preserving the merge gate:
// it advances to QA only once every produced task is done, holds the phase with
// the blocking merge_approval gate while tasks await a merge, and fails the
// stage (for a human) when a produced task is still open and no merge is
// pending. It never closes tasks or merges pull requests itself.
type BuildRunner struct {
	// Backend reads the produced tasks' durable status. Required.
	Backend store.Backend
	// Build drives the produced tasks. Required.
	Build BuildFunc
}

// Run implements StageRunner.
func (r *BuildRunner) Run(ctx context.Context, p *core.Pipeline) (StageResult, error) {
	if len(p.Produced) == 0 {
		return StageResult{}, fmt.Errorf("up: build needs produced tasks; pipeline %s has none", p.ID)
	}
	if r.Build == nil {
		return StageResult{}, errors.New("up: build runner has no build function")
	}
	if r.Backend == nil {
		return StageResult{}, errors.New("up: build runner has no backend")
	}
	outcome, err := r.Build(ctx, p)
	if err != nil {
		return StageResult{}, err
	}

	var resolved, awaitingMerge, open int
	for _, id := range p.Produced {
		task, err := r.Backend.Tickets().Get(ctx, id)
		if err != nil {
			if errors.Is(err, core.ErrNotFound) {
				open++
				continue
			}
			return StageResult{}, err
		}
		switch task.Status {
		case core.StatusDone, core.StatusCancelled:
			resolved++
		case core.StatusReadyForReview:
			awaitingMerge++
		default:
			open++
		}
	}

	detail := outcome.Detail
	switch {
	case open > 0:
		return StageResult{}, fmt.Errorf("up: build of pipeline %s stalled: %d of %d produced task(s) are neither done nor awaiting merge", p.ID, open, len(p.Produced))
	case awaitingMerge > 0:
		if detail == "" {
			detail = fmt.Sprintf("%d task(s) awaiting merge", awaitingMerge)
		}
		return StageResult{Done: true, Gate: core.GateMergeApproval, Detail: detail}, nil
	default:
		if detail == "" {
			detail = fmt.Sprintf("all %d produced task(s) done", resolved)
		}
		return StageResult{Done: true, Detail: detail}, nil
	}
}
