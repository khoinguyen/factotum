package up

import (
	"context"
	"errors"
	"fmt"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// QAOutcome is what one QA dispatch did; Detail is a one-line summary for the
// event log.
type QAOutcome struct {
	Detail string
}

// QAFunc dispatches the feature QA and records its verdict. The production
// implementation runs the single-task-qa agent, which records the verdict with
// `ft qa record`. A test supplies a stub that records the verdict.
type QAFunc func(ctx context.Context, p *core.Pipeline) (QAOutcome, error)

// QARunner is the QA stage runner. It dispatches the feature QA over the
// pipeline's capture and advances only once a passing verdict is recorded, so a
// feature never reaches rollout unverified. A verdict already recorded for the
// capture is reused, so a restart does not QA twice.
type QARunner struct {
	// Backend reads the capture and its recorded QA verdict. Required.
	Backend store.Backend
	// QA dispatches the feature QA. Required unless a pass is already recorded.
	QA QAFunc
}

// Run implements StageRunner.
func (r *QARunner) Run(ctx context.Context, p *core.Pipeline) (StageResult, error) {
	if r.Backend == nil {
		return StageResult{}, errors.New("up: qa runner has no backend")
	}
	capture, err := r.Backend.Tickets().Get(ctx, p.CaptureID)
	if err != nil {
		return StageResult{}, err
	}
	qa := app.NewQAService(r.Backend, nil, nil)
	passed, err := qa.Passed(ctx, capture)
	if err != nil {
		return StageResult{}, err
	}
	if passed {
		return StageResult{Done: true, Detail: fmt.Sprintf("feature %s already passed QA", p.CaptureID)}, nil
	}
	if r.QA == nil {
		return StageResult{}, errors.New("up: qa runner has no qa function")
	}
	outcome, err := r.QA(ctx, p)
	if err != nil {
		return StageResult{}, err
	}
	passed, err = qa.Passed(ctx, capture)
	if err != nil {
		return StageResult{}, err
	}
	if !passed {
		return StageResult{}, fmt.Errorf("up: QA of capture %s did not pass", p.CaptureID)
	}
	detail := outcome.Detail
	if detail == "" {
		detail = fmt.Sprintf("feature %s passed QA", p.CaptureID)
	}
	return StageResult{Done: true, Detail: detail}, nil
}
