package up

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

// The operator verbs are the durable, auditable way a human clears a pipeline's
// gate or holds, resumes, retries, or cancels it. Each acts on the stored
// Pipeline, so it works while the daemon runs or against a stopped store; a
// running daemon re-owns the pipeline on its next pass and resumes the phase it
// now has no gate for (docs/up/design.md §6/§10).

// Answer records the operator's answers to a pipeline's deferred groom questions
// as a note on the origin capture and clears the advisory groom_questions gate.
// Grooming is not restarted: the answers steer the work the groom session already
// produced.
func (c *Controller) Answer(ctx context.Context, id core.PipelineID, answers string) (*core.Pipeline, error) {
	if strings.TrimSpace(answers) == "" {
		return nil, fmt.Errorf("%w: answers are required", core.ErrInvalid)
	}
	pipeline, err := c.ValidateGate(ctx, id, core.GateGroomQuestions)
	if err != nil {
		return nil, err
	}
	tickets := app.NewTicketService(c.backend, c.clock, c.ids)
	if _, err := tickets.AddNote(ctx, pipeline.CaptureID, app.NoteInput{
		Body:   "Deferred grooming questions answered:\n\n" + strings.TrimSpace(answers),
		System: true,
	}); err != nil {
		return nil, err
	}
	return c.clearGate(ctx, pipeline, core.GateGroomQuestions)
}

// Approve accepts a design held at the design_rework gate: it clears the gate so
// the controller resumes the review phase. The approving re-review itself is
// recorded through `ft groom review` (reusing its existing block, design §5), so
// approve is the operator's gate-clearing counterpart, not a second reviewer.
func (c *Controller) Approve(ctx context.Context, id core.PipelineID) (*core.Pipeline, error) {
	pipeline, err := c.ValidateGate(ctx, id, core.GateDesignRework)
	if err != nil {
		return nil, err
	}
	return c.clearGate(ctx, pipeline, core.GateDesignRework)
}

// Merge records that the operator merged the pull requests a build stage left
// awaiting merge: every produced task awaiting review is marked done, then the
// blocking merge_approval gate is cleared so the resumed build stage sees the
// work resolved and advances.
func (c *Controller) Merge(ctx context.Context, id core.PipelineID) (*core.Pipeline, error) {
	pipeline, err := c.ValidateGate(ctx, id, core.GateMergeApproval)
	if err != nil {
		return nil, err
	}
	tickets := app.NewTicketService(c.backend, c.clock, c.ids)
	for _, taskID := range pipeline.Produced {
		task, err := tickets.Get(ctx, taskID)
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if task.Status != core.StatusReadyForReview {
			continue
		}
		if _, err := tickets.SetStatus(ctx, task.ID, core.StatusDone); err != nil {
			return nil, err
		}
	}
	return c.clearGate(ctx, pipeline, core.GateMergeApproval)
}

// Release closes a pipeline's release milestone (the release decision, design
// §9) and clears the blocking release gate so the rollout stage proceeds.
func (c *Controller) Release(ctx context.Context, id core.PipelineID) (*core.Pipeline, error) {
	pipeline, err := c.ValidateGate(ctx, id, core.GateRelease)
	if err != nil {
		return nil, err
	}
	if pipeline.Milestone == nil {
		return nil, fmt.Errorf("%w: pipeline %s has no release milestone to close", core.ErrInvalid, pipeline.ID)
	}
	if _, err := app.NewTicketService(c.backend, c.clock, c.ids).SetStatus(ctx, *pipeline.Milestone, core.StatusDone); err != nil {
		return nil, err
	}
	return c.clearGate(ctx, pipeline, core.GateRelease)
}

// Promote approves the rollout promotion and clears the blocking rollout gate so
// the rollout stage proceeds.
func (c *Controller) Promote(ctx context.Context, id core.PipelineID) (*core.Pipeline, error) {
	pipeline, err := c.ValidateGate(ctx, id, core.GateRollout)
	if err != nil {
		return nil, err
	}
	return c.clearGate(ctx, pipeline, core.GateRollout)
}

// Pause holds a pipeline at its current phase. The held phase is recorded so
// Resume returns to it; the controller does not auto-resume a paused pipeline
// (design §5).
func (c *Controller) Pause(ctx context.Context, id core.PipelineID) (*core.Pipeline, error) {
	pipeline, err := c.operatorPipeline(ctx, id)
	if err != nil {
		return nil, err
	}
	if pipeline.State.Terminal() {
		return nil, terminalPipeline(pipeline)
	}
	if pipeline.State == core.PipelinePaused {
		return nil, fmt.Errorf("%w: pipeline %s is already paused", core.ErrConflict, pipeline.ID)
	}
	pipeline.ResumeState = pipeline.State
	pipeline.State = core.PipelinePaused
	return c.persist(ctx, pipeline)
}

// Resume returns a paused pipeline to the phase it held (design §5).
func (c *Controller) Resume(ctx context.Context, id core.PipelineID) (*core.Pipeline, error) {
	pipeline, err := c.operatorPipeline(ctx, id)
	if err != nil {
		return nil, err
	}
	if pipeline.State != core.PipelinePaused {
		return nil, fmt.Errorf("%w: pipeline %s is not paused", core.ErrConflict, pipeline.ID)
	}
	pipeline.State = heldPhase(pipeline.ResumeState)
	pipeline.ResumeState = ""
	return c.persist(ctx, pipeline)
}

// Retry returns a failed pipeline to the phase it failed in, clearing the
// failure reason; the controller resumes that stage (design §5).
func (c *Controller) Retry(ctx context.Context, id core.PipelineID) (*core.Pipeline, error) {
	pipeline, err := c.operatorPipeline(ctx, id)
	if err != nil {
		return nil, err
	}
	if pipeline.State != core.PipelineFailed {
		return nil, fmt.Errorf("%w: pipeline %s is not failed", core.ErrConflict, pipeline.ID)
	}
	pipeline.State = heldPhase(pipeline.ResumeState)
	pipeline.ResumeState = ""
	pipeline.Error = ""
	return c.persist(ctx, pipeline)
}

// Cancel terminates a pipeline from any non-terminal state. cancelled is
// immutable, like done (design §5).
func (c *Controller) Cancel(ctx context.Context, id core.PipelineID) (*core.Pipeline, error) {
	pipeline, err := c.operatorPipeline(ctx, id)
	if err != nil {
		return nil, err
	}
	if pipeline.State.Terminal() {
		return nil, terminalPipeline(pipeline)
	}
	pipeline.State = core.PipelineCancelled
	pipeline.Gate = core.GateNone
	pipeline.ResumeState = ""
	return c.persist(ctx, pipeline)
}

// ValidateGate reports whether pipeline id is owned by the controller and is
// currently holding want, without mutating it. A verb that performs a side
// effect before clearing the gate (approve re-records the review) validates
// first, so a wrong-state call is side-effect-free; the clearing verb still
// re-checks, so ValidateGate is an early guard, not the authority.
func (c *Controller) ValidateGate(ctx context.Context, id core.PipelineID, want core.GateKind) (*core.Pipeline, error) {
	pipeline, err := c.operatorPipeline(ctx, id)
	if err != nil {
		return nil, err
	}
	if pipeline.State.Terminal() {
		return nil, terminalPipeline(pipeline)
	}
	if pipeline.Gate != want {
		return nil, notAtGate(pipeline, want)
	}
	return pipeline, nil
}

// operatorPipeline loads a pipeline the controller owns. A missing id or one in
// another project is ErrNotFound, so a verb never acts across the project scope.
func (c *Controller) operatorPipeline(ctx context.Context, id core.PipelineID) (*core.Pipeline, error) {
	pipeline, err := c.backend.Pipelines().Get(ctx, id)
	if errors.Is(err, core.ErrNotFound) {
		return nil, fmt.Errorf("%w: pipeline %s", core.ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	if pipeline.ProjectID != c.project {
		return nil, fmt.Errorf("%w: pipeline %s is not in project %s", core.ErrNotFound, id, c.project)
	}
	return pipeline, nil
}

// clearGate requires pipeline to be holding want, clears it, saves, and records
// the clearing event. Clearing leaves the phase unchanged, so the controller
// resumes the same stage (design §5).
func (c *Controller) clearGate(ctx context.Context, pipeline *core.Pipeline, want core.GateKind) (*core.Pipeline, error) {
	gate := pipeline.Gate
	pipeline.Gate = core.GateNone
	pipeline.UpdatedAt = c.clock.Now()
	if err := c.save(ctx, pipeline); err != nil {
		return nil, err
	}
	c.emit(ctx, pipeline, core.EventPipelineGateCleared,
		fmt.Sprintf("gate %s cleared", gate),
		map[string]any{"gate": string(gate)})
	return pipeline, nil
}

// persist saves an operator mutation, stamping it.
func (c *Controller) persist(ctx context.Context, pipeline *core.Pipeline) (*core.Pipeline, error) {
	pipeline.UpdatedAt = c.clock.Now()
	if err := c.save(ctx, pipeline); err != nil {
		return nil, err
	}
	return pipeline, nil
}

// heldPhase is the phase a parked pipeline returns to, or queued when none was
// recorded (a pipeline parked before the phase was tracked).
func heldPhase(state core.PipelineState) core.PipelineState {
	if state != "" && !state.Terminal() {
		return state
	}
	return core.PipelineQueued
}

// notAtGate reports a verb aimed at the wrong gate.
func notAtGate(pipeline *core.Pipeline, want core.GateKind) error {
	return fmt.Errorf("%w: pipeline %s is not at the %s gate (state %s, gate %q)",
		core.ErrConflict, pipeline.ID, want, pipeline.State, pipeline.Gate)
}

// terminalPipeline reports an operator verb that cannot move a terminal state.
func terminalPipeline(pipeline *core.Pipeline) error {
	return fmt.Errorf("%w: pipeline %s is %s and immutable", core.ErrConflict, pipeline.ID, pipeline.State)
}
