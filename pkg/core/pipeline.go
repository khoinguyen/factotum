package core

import (
	"fmt"
	"time"
)

// PipelineID identifies one factory pipeline: the mutable, in-flight record
// beside an immutable capture (idea/bug).
type PipelineID string

func (id PipelineID) String() string { return string(id) }

// PipelineState is the factory phase a pipeline is in. A gate is orthogonal to
// the state; see GateKind.
type PipelineState string

const (
	// PipelineQueued is enqueued, not yet started.
	PipelineQueued PipelineState = "queued"
	// PipelineGrooming is running (or resuming) the groom session.
	PipelineGrooming PipelineState = "grooming"
	// PipelineReview is running the architecture review.
	PipelineReview PipelineState = "review"
	// PipelineBuilding is running the build loop.
	PipelineBuilding PipelineState = "building"
	// PipelineQA is running QA.
	PipelineQA PipelineState = "qa"
	// PipelineRollout is running the release/rollout stage.
	PipelineRollout PipelineState = "rollout"
	// PipelineDone is terminal success (immutable).
	PipelineDone PipelineState = "done"
	// PipelineCancelled is terminal, operator-cancelled (immutable).
	PipelineCancelled PipelineState = "cancelled"
	// PipelineFailed is parked after a stage failure; retryable.
	PipelineFailed PipelineState = "failed"
	// PipelinePaused is an operator hold; resumable.
	PipelinePaused PipelineState = "paused"
)

func (s PipelineState) Valid() bool {
	switch s {
	case PipelineQueued, PipelineGrooming, PipelineReview, PipelineBuilding,
		PipelineQA, PipelineRollout, PipelineDone, PipelineCancelled,
		PipelineFailed, PipelinePaused:
		return true
	default:
		return false
	}
}

// Terminal reports whether a state is final and immutable. Only done and
// cancelled are terminal; failed and paused are parked but reversible so
// retry/resume can move them.
func (s PipelineState) Terminal() bool {
	return s == PipelineDone || s == PipelineCancelled
}

// GateKind is the human action, if any, a pipeline is waiting on. It is
// orthogonal to PipelineState: a gated pipeline stays in its phase (or pauses
// as failed/paused) and resumes when the gate is cleared.
type GateKind string

const (
	// GateNone is no gate.
	GateNone GateKind = ""
	// GateGroomQuestions is deferred PO questions (advisory; does not block).
	GateGroomQuestions GateKind = "groom_questions"
	// GateDesignRework is a needs-rework verdict (blocking).
	GateDesignRework GateKind = "design_rework"
	// GateMergeApproval is an approved PR awaiting merge (blocking).
	GateMergeApproval GateKind = "merge_approval"
	// GateRelease is the release milestone to close (blocking).
	GateRelease GateKind = "release"
	// GateRollout is the deploy/promote approval (blocking).
	GateRollout GateKind = "rollout"
)

func (g GateKind) Valid() bool {
	switch g {
	case GateNone, GateGroomQuestions, GateDesignRework, GateMergeApproval,
		GateRelease, GateRollout:
		return true
	default:
		return false
	}
}

// Pipeline is the durable, mutable factory record for one captured idea or bug.
// It is keyed by the capture it is driving (one pipeline per capture) and moves
// through the phase state machine with an orthogonal human gate. See
// docs/up/design.md §5.
type Pipeline struct {
	ID        PipelineID
	ProjectID ProjectID
	// CaptureID is the origin idea/bug this pipeline drives.
	CaptureID TicketID
	// Milestone is the release gate this pipeline drives toward, when attached.
	Milestone *TicketID
	State     PipelineState
	Gate      GateKind
	// Session is the groom session id, once grooming has run.
	Session string
	// Produced are the tasks the groom session created.
	Produced []TicketID
	// Error is the last failure reason.
	Error    string
	Attempts int

	CreatedAt time.Time
	UpdatedAt time.Time
}

func (p Pipeline) Validate() error {
	if p.ID == "" {
		return fmt.Errorf("%w: pipeline id is required", ErrInvalid)
	}
	if p.ProjectID == "" {
		return fmt.Errorf("%w: pipeline project id is required", ErrInvalid)
	}
	if p.CaptureID == "" {
		return fmt.Errorf("%w: pipeline capture id is required", ErrInvalid)
	}
	if !p.State.Valid() {
		return fmt.Errorf("%w: unknown pipeline state %q", ErrInvalid, p.State)
	}
	if !p.Gate.Valid() {
		return fmt.Errorf("%w: unknown pipeline gate %q", ErrInvalid, p.Gate)
	}
	return nil
}
