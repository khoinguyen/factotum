// Package up hosts the factory controller behind `ft up`: the pipeline state
// machine that turns a stored capture into a feature in flight. The controller
// accepts captures from the serve write path (serve.CaptureController), owns
// each pipeline's phase, runs one stage at a time, and parks on human gates. It
// depends on a small StageRunner port, so the machine is testable with fakes and
// the phases stay pluggable. See docs/up/design.md §3/§5/§8.
package up

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// noAutoLabel marks a capture the operator never wants auto-started. The
// controller skips it and records why.
const noAutoLabel = "no-auto"

// DefaultPoll is how often the controller looks for claimable work. It is short
// enough to feel immediate and long enough not to spin.
const DefaultPoll = 500 * time.Millisecond

// StageResult reports what one stage did. Done false means the stage handed its
// work to an externally dispatched agent run (RunID) and the controller should
// wait rather than advance. Gate opens a human gate (empty = none).
type StageResult struct {
	Done   bool
	Gate   core.GateKind
	Detail string
	// RunID identifies an externally dispatched agent run, so a restart can
	// re-attach to it instead of dispatching a duplicate.
	RunID core.RunID
	// Session and Produced are the groom stage's durable outputs.
	Session  string
	Produced []core.TicketID
}

// StageRunner runs one phase of one pipeline. A production runner dispatches a
// harness/agent run or an ft command; the controller only sequences runners.
type StageRunner interface {
	Run(ctx context.Context, p *core.Pipeline) (StageResult, error)
}

// Options configures the controller.
type Options struct {
	// Backend is the store the controller reads and writes. Required.
	Backend store.Backend
	// Clock supplies timestamps. Defaults to the system clock.
	Clock app.Clock
	// IDs mints pipeline ids. Defaults to the random generator.
	IDs app.IDGen
	// Project scopes the controller: it only owns this project's pipelines,
	// mirroring capture's single target (design §3). Required.
	Project core.ProjectID
	// Stages maps a phase to the runner that advances it. A phase with no runner
	// is simply not advanced, which is what stops the pipeline at the phases
	// later steps add. Required (may be empty).
	Stages map[core.PipelineState]StageRunner
	// Poll is how often Start looks for work. Defaults to DefaultPoll.
	Poll time.Duration
	// Logf, when set, receives non-fatal notices (a skipped capture, a failed
	// stage). It never receives fatal errors, which Step returns.
	Logf func(format string, args ...any)
}

// Controller is the factory state machine: it accepts captures, owns pipelines,
// and drives each phase to its next gate. It implements serve.CaptureController.
type Controller struct {
	backend store.Backend
	clock   app.Clock
	ids     app.IDGen
	project core.ProjectID
	stages  map[core.PipelineState]StageRunner
	poll    time.Duration
	logf    func(string, ...any)
}

// New validates opts and returns a controller.
func New(opts Options) (*Controller, error) {
	if opts.Backend == nil {
		return nil, errors.New("up: backend is required")
	}
	if opts.Project == "" {
		return nil, errors.New("up: project is required")
	}
	if opts.Clock == nil {
		opts.Clock = app.SystemClock{}
	}
	if opts.IDs == nil {
		opts.IDs = app.RandomIDGen{}
	}
	if opts.Poll <= 0 {
		opts.Poll = DefaultPoll
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	return &Controller{
		backend: opts.Backend,
		clock:   opts.Clock,
		ids:     opts.IDs,
		project: opts.Project,
		stages:  opts.Stages,
		poll:    opts.Poll,
		logf:    opts.Logf,
	}, nil
}

// Submit is the serve write-path hook: it enqueues a pipeline for a stored
// capture. It runs synchronously on the request goroutine but only creates the
// queued record, so it never blocks on a stage. Errors are logged, not returned
// (the interface has no error), so a transient store fault never fails the
// capture write that already succeeded.
func (c *Controller) Submit(ctx context.Context, capture *core.Ticket) {
	if capture == nil {
		return
	}
	if _, err := c.Enqueue(ctx, capture); err != nil {
		c.logf("up: enqueue capture %s: %v", capture.ID, err)
	}
}

// Enqueue creates the queued pipeline for a capture, or returns the existing one.
// It is idempotent and applies the design §4 skip rules: an executable ticket, a
// no-auto label, or an already-promoted capture starts no pipeline. A nil
// pipeline with a nil error means the capture was deliberately skipped.
func (c *Controller) Enqueue(ctx context.Context, capture *core.Ticket) (*core.Pipeline, error) {
	if capture == nil {
		return nil, fmt.Errorf("%w: capture is required", core.ErrInvalid)
	}
	if capture.ProjectID != "" && capture.ProjectID != c.project {
		return nil, fmt.Errorf("%w: capture %s is not in project %s", core.ErrInvalid, capture.ID, c.project)
	}
	if capture.Kind.Executable() {
		return nil, nil
	}
	if hasLabel(capture, noAutoLabel) {
		c.logf("up: skipping capture %s: carries the %s label", capture.ID, noAutoLabel)
		return nil, nil
	}
	promoted, err := c.promoted(ctx, capture.ID)
	if err != nil {
		return nil, err
	}
	if promoted {
		return nil, nil
	}
	existing, err := c.backend.Pipelines().List(ctx, store.PipelineFilter{
		ProjectID: c.project,
		CaptureID: &capture.ID,
	})
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return existing[0], nil
	}
	now := c.clock.Now()
	pipeline := &core.Pipeline{
		ID:        core.PipelineID(c.ids.NewID("pl")),
		ProjectID: c.project,
		CaptureID: capture.ID,
		State:     core.PipelineQueued,
		Gate:      core.GateNone,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := c.backend.Pipelines().Create(ctx, pipeline); err != nil {
		// A concurrent enqueue of the same capture wins; one pipeline per
		// capture is the invariant.
		if errors.Is(err, core.ErrAlreadyExists) {
			return nil, nil
		}
		return nil, err
	}
	c.emit(ctx, pipeline, core.EventPipelineQueued,
		fmt.Sprintf("pipeline queued for capture %s", capture.ID),
		map[string]any{"capture": string(capture.ID)})
	return pipeline, nil
}

// Pipelines lists the controller's project's pipelines, oldest first.
func (c *Controller) Pipelines(ctx context.Context) ([]*core.Pipeline, error) {
	return c.backend.Pipelines().List(ctx, store.PipelineFilter{ProjectID: c.project})
}

// Start runs the supervisor loop until ctx is done. It is the daemon's worker:
// each pass resumes in-flight pipelines and takes one queued pipeline. A stage
// error parks the pipeline (it is not a controller error), so the loop keeps
// going; only store faults are logged.
func (c *Controller) Start(ctx context.Context) error {
	ticker := time.NewTicker(c.poll)
	defer ticker.Stop()
	for {
		if err := c.Step(ctx); err != nil && ctx.Err() == nil {
			c.logf("up: step: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Step is one supervisor pass: resume every in-flight pipeline whose external
// run is gone, then claim and drive one queued pipeline. It is exported so a
// test (and a future tick-driven caller) can advance the machine deterministically.
func (c *Controller) Step(ctx context.Context) error {
	if err := c.resumeInflight(ctx); err != nil {
		return err
	}
	pipeline, err := c.backend.Pipelines().Claim(ctx, store.PipelineClaimRequest{ProjectID: c.project})
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return c.drive(ctx, pipeline)
}

// inFlightStates are the phases a restart re-owns: a pipeline mid-work whose
// process may have died. queued is handled by the claim path, and failed/paused
// are deliberately not auto-resumed (design §5).
var inFlightStates = []core.PipelineState{
	core.PipelineGrooming, core.PipelineReview, core.PipelineBuilding,
	core.PipelineQA, core.PipelineRollout,
}

// resumeInflight re-owns every non-terminal pipeline in an in-flight phase. One
// whose recorded external run is still live is left alone (re-attached); one
// whose run is gone has its current stage re-run. The per-pipeline claim is what
// makes exactly one controller own it (design §5).
func (c *Controller) resumeInflight(ctx context.Context) error {
	for _, state := range inFlightStates {
		pipelines, err := c.backend.Pipelines().List(ctx, store.PipelineFilter{
			ProjectID: c.project,
			States:    []core.PipelineState{state},
		})
		if err != nil {
			return err
		}
		for _, candidate := range pipelines {
			id := candidate.ID
			pipeline, err := c.backend.Pipelines().Claim(ctx, store.PipelineClaimRequest{
				ProjectID: c.project,
				ID:        &id,
			})
			if errors.Is(err, core.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if pipeline.RunID != "" {
				live, err := c.liveRun(ctx, pipeline.RunID)
				if err != nil {
					return err
				}
				if live {
					continue
				}
			}
			if err := c.drive(ctx, pipeline); err != nil {
				return err
			}
		}
	}
	return nil
}

// liveRun reports whether the run is still live in the store, so a restart can
// decide between re-attaching and re-running.
func (c *Controller) liveRun(ctx context.Context, id core.RunID) (bool, error) {
	live := true
	runs, err := c.backend.Runs().List(ctx, store.RunFilter{
		ProjectID: c.project,
		Live:      &live,
		Now:       c.clock.Now(),
	})
	if err != nil {
		return false, err
	}
	for _, run := range runs {
		if run.ID == id {
			return true, nil
		}
	}
	return false, nil
}

// drive advances one pipeline phase by phase until it gates, parks, hands off to
// an external run, or reaches a phase with no runner.
func (c *Controller) drive(ctx context.Context, pipeline *core.Pipeline) error {
	for {
		if pipeline.Gate.Blocking() {
			return nil
		}
		runner, ok := c.stages[pipeline.State]
		if !ok {
			return nil
		}
		c.emit(ctx, pipeline, core.EventPipelineStageStarted,
			fmt.Sprintf("stage %s started", pipeline.State),
			map[string]any{"state": string(pipeline.State)})

		result, err := runner.Run(ctx, pipeline)
		if err != nil {
			pipeline.State = core.PipelineFailed
			pipeline.Error = err.Error()
			pipeline.UpdatedAt = c.clock.Now()
			if saveErr := c.save(ctx, pipeline); saveErr != nil {
				return saveErr
			}
			c.emit(ctx, pipeline, core.EventPipelineFailed, err.Error(),
				map[string]any{"state": string(pipeline.State), "error": err.Error()})
			return nil
		}

		if result.RunID != "" {
			pipeline.RunID = result.RunID
		}
		if !result.Done {
			// The stage handed its work to an external agent run: record the
			// run and wait for a later pass, when the run is gone, to re-run.
			pipeline.UpdatedAt = c.clock.Now()
			return c.save(ctx, pipeline)
		}
		if result.Session != "" {
			pipeline.Session = result.Session
		}
		if len(result.Produced) > 0 {
			pipeline.Produced = result.Produced
		}
		pipeline.RunID = ""
		pipeline.Attempts++
		pipeline.Gate = result.Gate
		pipeline.State = nextState(pipeline.State)
		pipeline.UpdatedAt = c.clock.Now()
		if err := c.save(ctx, pipeline); err != nil {
			return err
		}
		detail := result.Detail
		if detail == "" {
			detail = fmt.Sprintf("stage finished; next %s", pipeline.State)
		}
		c.emit(ctx, pipeline, core.EventPipelineStageFinished, detail,
			map[string]any{"state": string(pipeline.State)})
		if result.Gate != core.GateNone {
			c.emit(ctx, pipeline, core.EventPipelineGateOpened, string(result.Gate),
				map[string]any{"gate": string(result.Gate)})
		}
		if pipeline.State == core.PipelineDone {
			c.emit(ctx, pipeline, core.EventPipelineDone, "pipeline done", nil)
			return nil
		}
		if result.Gate.Blocking() {
			return nil
		}
	}
}

// nextState advances a pipeline's phase to the phase that follows it.
func nextState(state core.PipelineState) core.PipelineState {
	switch state {
	case core.PipelineQueued:
		return core.PipelineGrooming
	case core.PipelineGrooming:
		return core.PipelineReview
	case core.PipelineReview:
		return core.PipelineBuilding
	case core.PipelineBuilding:
		return core.PipelineQA
	case core.PipelineQA:
		return core.PipelineRollout
	case core.PipelineRollout:
		return core.PipelineDone
	default:
		return state
	}
}

// save writes a pipeline, ignoring nothing: a store fault is returned so the
// caller can log it. A terminal pipeline cannot be saved, but drive never tries:
// it stops when it reaches a terminal state.
func (c *Controller) save(ctx context.Context, pipeline *core.Pipeline) error {
	return c.backend.Pipelines().Update(ctx, pipeline)
}

// promoted reports whether an executable task already depends on the capture
// (the origin edge), so the capture is already groomed into work.
func (c *Controller) promoted(ctx context.Context, capture core.TicketID) (bool, error) {
	dependsOn := capture
	tasks, err := c.backend.Tickets().List(ctx, store.TicketFilter{
		ProjectID: c.project,
		DependsOn: &dependsOn,
	})
	if err != nil {
		return false, err
	}
	for _, task := range tasks {
		if task.Kind.Executable() {
			return true, nil
		}
	}
	return false, nil
}

// hasLabel reports whether the ticket carries label.
func hasLabel(ticket *core.Ticket, label string) bool {
	for _, have := range ticket.Labels {
		if have == label {
			return true
		}
	}
	return false
}

// emit appends a pipeline event, attributed to the capture so it surfaces on the
// capture's page and through the broker. A failure to append never aborts the
// transition it records: the pipeline is already durable, and the event log is
// a summary, not the state.
func (c *Controller) emit(ctx context.Context, pipeline *core.Pipeline, kind core.EventKind, summary string, data map[string]any) {
	capture := pipeline.CaptureID
	event := &core.Event{
		ID:        core.EventID(c.ids.NewID("ev")),
		ProjectID: pipeline.ProjectID,
		TicketID:  &capture,
		Kind:      kind,
		Summary:   summary,
		Data:      data,
		CreatedAt: c.clock.Now(),
	}
	if err := event.Validate(); err != nil {
		c.logf("up: pipeline event %s: %v", kind, err)
		return
	}
	if err := c.backend.Events().Append(ctx, event); err != nil {
		c.logf("up: append %s event: %v", kind, err)
	}
}
