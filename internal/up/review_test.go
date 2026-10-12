package up

import (
	"context"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/internal/groom"
	"github.com/khoinguyen/factotum/pkg/core"
)

// reviewFixture seeds a data dir holding a groom session with the given
// produced tasks, so a review runner has a session to read.
func reviewFixture(t *testing.T, produced ...string) (string, *core.Pipeline) {
	t.Helper()
	dataDir := t.TempDir()
	record := groom.SessionRecord{
		ID:        "groom-1",
		Project:   "demo",
		CreatedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		Scope:     []groom.ScopeItem{{ID: "i-1", Kind: "idea", Title: "capture"}},
		Produced:  produced,
	}
	if err := groom.WriteSession(dataDir, record); err != nil {
		t.Fatalf("WriteSession() error = %v", err)
	}
	pipeline := &core.Pipeline{
		ID:        "pl-1",
		ProjectID: "demo",
		CaptureID: "i-1",
		Session:   "groom-1",
		State:     core.PipelineReview,
	}
	return dataDir, pipeline
}

// TestReviewRunnerAdvancesOnApproval proves an approving verdict advances the
// stage: the runner dispatches the review, reads the recorded verdict, and
// reports Done with no gate, so the controller moves on to build.
func TestReviewRunnerAdvancesOnApproval(t *testing.T) {
	dataDir, pipeline := reviewFixture(t)
	runner := &ReviewRunner{
		DataDir: dataDir,
		Review:  recordVerdict(dataDir, groom.VerdictApprove),
	}
	result, err := runner.Run(context.Background(), pipeline)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.Done {
		t.Fatal("Run().Done = false, want true")
	}
	if result.Gate != core.GateNone {
		t.Fatalf("Run().Gate = %q, want none", result.Gate)
	}
}

// TestReviewRunnerOpensReworkGate proves a needs-rework verdict opens the
// blocking design-rework gate, which holds the pipeline in review so the build
// does not run.
func TestReviewRunnerOpensReworkGate(t *testing.T) {
	dataDir, pipeline := reviewFixture(t)
	runner := &ReviewRunner{
		DataDir: dataDir,
		Review:  recordVerdict(dataDir, groom.VerdictNeedsRework),
	}
	result, err := runner.Run(context.Background(), pipeline)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.Done {
		t.Fatal("Run().Done = false, want true")
	}
	if result.Gate != core.GateDesignRework {
		t.Fatalf("Run().Gate = %q, want design_rework", result.Gate)
	}
	if !result.Gate.Blocking() {
		t.Fatal("design_rework must block the build")
	}
}

// TestReviewRunnerReusesRecordedVerdict proves idempotency: a session that
// already carries a verdict is not reviewed again on a restart, and the
// recorded verdict decides the gate.
func TestReviewRunnerReusesRecordedVerdict(t *testing.T) {
	dataDir, pipeline := reviewFixture(t)
	if err := groom.WriteSession(dataDir, groom.SessionRecord{
		ID:            "groom-1",
		Project:       "demo",
		ReviewVerdict: string(groom.VerdictNeedsRework),
	}); err != nil {
		t.Fatalf("WriteSession() error = %v", err)
	}
	dispatched := false
	runner := &ReviewRunner{
		DataDir: dataDir,
		Review: func(context.Context, *core.Pipeline, string) (ReviewOutcome, error) {
			dispatched = true
			return ReviewOutcome{}, nil
		},
	}
	result, err := runner.Run(context.Background(), pipeline)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if dispatched {
		t.Fatal("Run() dispatched a review despite a recorded verdict")
	}
	if result.Gate != core.GateDesignRework {
		t.Fatalf("Run().Gate = %q, want design_rework", result.Gate)
	}
}

// TestReviewRunnerNeedsSession proves the runner refuses a pipeline that never
// groomed: the review has no docs to read.
func TestReviewRunnerNeedsSession(t *testing.T) {
	runner := &ReviewRunner{DataDir: t.TempDir()}
	if _, err := runner.Run(context.Background(), &core.Pipeline{ID: "pl-1"}); err == nil {
		t.Fatal("Run() with no session error = nil, want an error")
	}
}

// TestReviewRunnerErrorsWhenNoVerdictRecorded proves the stage fails rather than
// advancing silently when the review recorded nothing: an unrecorded verdict
// must not let a feature past the build gate.
func TestReviewRunnerErrorsWhenNoVerdictRecorded(t *testing.T) {
	dataDir, pipeline := reviewFixture(t)
	runner := &ReviewRunner{
		DataDir: dataDir,
		Review:  func(context.Context, *core.Pipeline, string) (ReviewOutcome, error) { return ReviewOutcome{}, nil },
	}
	if _, err := runner.Run(context.Background(), pipeline); err == nil {
		t.Fatal("Run() with no recorded verdict error = nil, want an error")
	}
}

// recordVerdict returns a ReviewFunc stub that records the verdict on the
// session manifest and reports it, modelling what the architecture-reviewer
// does with `ft groom review --verdict`.
func recordVerdict(dataDir string, verdict groom.ReviewVerdict) ReviewFunc {
	return func(_ context.Context, _ *core.Pipeline, sessionID string) (ReviewOutcome, error) {
		rec, err := groom.ReadSession(dataDir, sessionID)
		if err != nil {
			return ReviewOutcome{}, err
		}
		rec.ReviewVerdict = string(verdict)
		if err := groom.WriteSession(dataDir, rec); err != nil {
			return ReviewOutcome{}, err
		}
		return ReviewOutcome{Verdict: verdict, Detail: "reviewed"}, nil
	}
}
