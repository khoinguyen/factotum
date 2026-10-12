package up

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/khoinguyen/factotum/internal/groom"
	"github.com/khoinguyen/factotum/pkg/core"
)

// ReviewOutcome is what one architecture review produced: the tech-design
// verdict and a one-line detail for the event log.
type ReviewOutcome struct {
	Verdict groom.ReviewVerdict
	Detail  string
}

// ReviewFunc runs one architecture review over a pipeline's groom session and
// records the verdict. The production implementation dispatches the
// architecture-reviewer over the session's documents; the reviewer records the
// verdict with `ft groom review --verdict`, so the durable session manifest is
// the source of truth. A test supplies a stub that records the verdict.
type ReviewFunc func(ctx context.Context, p *core.Pipeline, sessionID string) (ReviewOutcome, error)

// ReviewRunner is the review stage runner. It dispatches the architecture
// review over the groom session's documents, reads the recorded verdict, and
// maps it to the gate: a needs-rework verdict opens the blocking design-rework
// gate, holding the build; any approving verdict advances to build. It reuses a
// verdict already recorded for the session, so a restart between recording and
// advancing does not review twice.
type ReviewRunner struct {
	// DataDir is where the project's groom sessions live; the runner reads the
	// session manifest from it. Required.
	DataDir string
	// Review runs the architecture review. Required unless the session already
	// records a verdict.
	Review ReviewFunc
}

// Run implements StageRunner.
func (r *ReviewRunner) Run(ctx context.Context, p *core.Pipeline) (StageResult, error) {
	if p.Session == "" {
		return StageResult{}, fmt.Errorf("up: review needs a groom session; pipeline %s has none", p.ID)
	}
	record, err := groom.ReadSession(r.DataDir, p.Session)
	if err != nil {
		return StageResult{}, err
	}
	verdict := groom.ReviewVerdict(record.ReviewVerdict)

	if verdict == "" {
		if r.Review == nil {
			return StageResult{}, errors.New("up: review runner has no review function")
		}
		outcome, err := r.Review(ctx, p, p.Session)
		if err != nil {
			return StageResult{}, err
		}
		verdict = outcome.Verdict
		// The review records its verdict on the session; trust the durable
		// record over the returned value when both are present.
		if recorded, err := recordedVerdict(r.DataDir, p.Session); err != nil {
			return StageResult{}, err
		} else if recorded != "" {
			verdict = recorded
		}
	}
	if verdict == "" {
		return StageResult{}, fmt.Errorf("up: review of session %s recorded no verdict", p.Session)
	}

	gate := core.GateNone
	if verdict.BlocksBuild() {
		gate = core.GateDesignRework
	}
	return StageResult{
		Done:   true,
		Gate:   gate,
		Detail: fmt.Sprintf("architecture review: %s", verdict),
	}, nil
}

// recordedVerdict reads the verdict a review recorded on a session, or "" when
// none is recorded yet.
func recordedVerdict(dataDir, sessionID string) (groom.ReviewVerdict, error) {
	record, err := groom.ReadSession(dataDir, sessionID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return groom.ReviewVerdict(record.ReviewVerdict), nil
}
