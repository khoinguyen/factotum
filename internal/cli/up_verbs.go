package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/internal/groom"
	"github.com/khoinguyen/factotum/internal/up"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

// upOperator is the controller method a simple operator verb invokes.
type upOperator func(ctx context.Context, c *up.Controller, id core.PipelineID) (*core.Pipeline, error)

// newUpOperatorCommand builds a verb that takes one pipeline id and calls op on
// the controller. It is the shared shape of approve/merge/release/promote and
// pause/resume/retry/cancel.
func newUpOperatorCommand(deps *Deps, use, short, long string, op upOperator) *cobra.Command {
	var projectID string
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long,
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return deps.runUpOperator(cmd, projectID, args[0], op)
		},
	}
	cmd.Flags().StringVarP(&projectID, "project", "p", "", "project id (defaults to the configured project)")
	return cmd
}

// runUpOperator opens the project's store, builds a controller over its durable
// state, and applies one operator verb. It works while the daemon runs or
// against a stopped store: the verb writes the pipeline, and a running daemon
// re-owns it on its next pass (design §10).
func (d *Deps) runUpOperator(cmd *cobra.Command, projectID, pipelineID string, op upOperator) error {
	ctx := cmd.Context()
	return d.withUpController(cmd, projectID, func(controller *up.Controller, _ groomTarget, _ core.ProjectID) error {
		pipeline, err := op(ctx, controller, core.PipelineID(pipelineID))
		if err != nil {
			return err
		}
		return d.emitUpPipeline(pipeline)
	})
}

// withUpController resolves the project, opens its store, and hands a controller
// bound to that store, its target, and the resolved project id to fn. The target
// is closed after fn returns.
func (d *Deps) withUpController(cmd *cobra.Command, projectID string, fn func(*up.Controller, groomTarget, core.ProjectID) error) error {
	ctx := cmd.Context()
	project := d.resolveProject(projectID)
	if err := requireProject(cmd, project); err != nil {
		return err
	}
	target, err := d.openGroomTarget(ctx, project)
	if err != nil {
		return err
	}
	defer target.close()
	controller, err := up.New(up.Options{Backend: target.backend, Clock: d.Clock, IDs: d.IDs, Project: project})
	if err != nil {
		return err
	}
	return fn(controller, target, project)
}

// newUpAnswerCommand answers a pipeline's deferred groom questions from a file
// and clears the advisory groom_questions gate.
func newUpAnswerCommand(deps *Deps) *cobra.Command {
	var projectID, bodyFile string
	cmd := &cobra.Command{
		Use:   "answer <pipeline> -f <file>",
		Short: "Answer a pipeline's deferred groom questions",
		Long: "Record the operator's answers to a pipeline's deferred product questions (the\n" +
			"advisory groom_questions gate) and clear the gate. The answers are stored as a\n" +
			"note on the origin capture, so the decision is auditable.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireFlags(cmd, "body-file"); err != nil {
				return err
			}
			answers, err := os.ReadFile(bodyFile)
			if err != nil {
				return usageError(cmd, "read answers file: %v", err)
			}
			return deps.withUpController(cmd, projectID, func(controller *up.Controller, _ groomTarget, _ core.ProjectID) error {
				pipeline, err := controller.Answer(cmd.Context(), core.PipelineID(args[0]), string(answers))
				if err != nil {
					return err
				}
				return deps.emitUpPipeline(pipeline)
			})
		},
	}
	cmd.Flags().StringVarP(&projectID, "project", "p", "", "project id (defaults to the configured project)")
	cmd.Flags().StringVarP(&bodyFile, "body-file", "f", "", "read the answers from a file (required)")
	return cmd
}

// newUpApproveCommand accepts a design held at the design_rework gate. It
// re-records an approving review on the pipeline's groom session (so the resumed
// review stage advances instead of re-opening the gate) and clears the gate,
// reusing `ft groom review`'s block/unblock rather than a second reviewer.
func newUpApproveCommand(deps *Deps) *cobra.Command {
	var projectID string
	cmd := &cobra.Command{
		Use:   "approve <pipeline>",
		Short: "Accept a design held at the design_rework gate",
		Long: "Accept a feature's design after a rework cycle: re-record an approving tech-design\n" +
			"review on the pipeline's groom session and clear the blocking design_rework gate,\n" +
			"so the controller resumes the review phase and advances to build.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return deps.runUpApprove(cmd, projectID, args[0])
		},
	}
	cmd.Flags().StringVarP(&projectID, "project", "p", "", "project id (defaults to the configured project)")
	return cmd
}

func (d *Deps) runUpApprove(cmd *cobra.Command, projectID, pipelineID string) error {
	ctx := cmd.Context()
	return d.withUpController(cmd, projectID, func(controller *up.Controller, target groomTarget, _ core.ProjectID) error {
		updated, err := approvePipeline(ctx, controller, target, core.PipelineID(pipelineID))
		if err != nil {
			return err
		}
		return d.emitUpPipeline(updated)
	})
}

// approvePipeline clears a design_rework gate. It validates the gate first, so a
// wrong-state call is side-effect-free: the review is re-recorded and the task
// gate released only once the pipeline is known to be parked at design_rework.
func approvePipeline(ctx context.Context, controller *up.Controller, target groomTarget, id core.PipelineID) (*core.Pipeline, error) {
	pipeline, err := controller.ValidateGate(ctx, id, core.GateDesignRework)
	if err != nil {
		return nil, err
	}
	if err := approveGroomSession(ctx, target, pipeline); err != nil {
		return nil, err
	}
	return controller.Approve(ctx, id)
}

// approveGroomSession re-records an approving tech-design review on a pipeline's
// groom session: it unblocks exactly the tasks a prior needs-rework review held
// and stores the approving verdict, so the resumed review stage reads approve
// and advances. A pipeline with no session yet (nothing to re-record) is left for
// the review stage to run.
func approveGroomSession(ctx context.Context, target groomTarget, pipeline *core.Pipeline) error {
	if pipeline.Session == "" {
		return nil
	}
	session, err := groom.ReadSession(target.dataDir, pipeline.Session)
	if err != nil {
		return err
	}
	if _, err := applyReviewGate(ctx, target.tasks, session, groom.VerdictApprove); err != nil {
		return err
	}
	session.ReviewVerdict = string(groom.VerdictApprove)
	session.Blocked = nil
	// session.Review is deliberately left pointing at the original review
	// artifact: the operator accepted the design, they did not author a new
	// review, so `ft groom show` keeps the needs-rework findings visible.
	return groom.WriteSession(target.dataDir, session)
}

// emitUpPipeline prints one operator verb's result in the repo's single-result
// shape, ending with project and repo.
func (d *Deps) emitUpPipeline(p *core.Pipeline) error {
	doc := upPipelineDocOf(p)
	doc.Repo = d.repoValue("")
	return d.emit(doc, func() {
		d.printFields(
			f("pipeline", doc.Pipeline),
			f("capture", doc.Capture),
			f("state", doc.State),
			f("gate", gateValue(doc.Gate)),
			f("project", doc.Project),
			f("repo", doc.Repo),
		)
	}, upOperatorHints(p)...)
}

// upOperatorHints names the next operator action a pipeline is waiting on, so a
// gated or parked pipeline surfaces its clearing command (design §6).
func upOperatorHints(p *core.Pipeline) []hint {
	// The gate-clearing command comes from app.GateCommand so the CLI and the
	// dashboard's /factory view name the same command (design §6).
	if command, ok := app.GateCommand(p); ok {
		switch p.Gate {
		case core.GateGroomQuestions:
			return []hint{{Command: command, About: "answer the deferred groom questions"}}
		case core.GateDesignRework:
			return []hint{
				{Command: fmt.Sprintf("ft groom review %s --verdict approve -f review.md", p.Session), About: "re-review the design"},
				{Command: command, About: "accept the design"},
			}
		case core.GateMergeApproval:
			return []hint{{Command: command, About: "clear merge_approval after merging the PRs"}}
		case core.GateRelease:
			return []hint{{Command: command, About: "close the release milestone"}}
		case core.GateRollout:
			return []hint{{Command: command, About: "approve the rollout promotion"}}
		}
	}
	switch {
	case p.State == core.PipelineFailed:
		return []hint{
			{Command: fmt.Sprintf("ft up retry %s", p.ID), About: "retry the failed stage"},
			{Command: fmt.Sprintf("ft up cancel %s", p.ID), About: "cancel the pipeline"},
		}
	case p.State == core.PipelinePaused:
		return []hint{{Command: fmt.Sprintf("ft up resume %s", p.ID), About: "resume the paused pipeline"}}
	case !p.State.Terminal():
		return []hint{{Command: fmt.Sprintf("ft up pause %s", p.ID), About: "hold the pipeline"}}
	}
	return nil
}

// upPipelineHints flattens the hints for a set of pipelines, so `ft up status`
// surfaces every open gate's clearing command.
func upPipelineHints(pipelines []*core.Pipeline) []hint {
	var hints []hint
	for _, pipeline := range pipelines {
		hints = append(hints, upOperatorHints(pipeline)...)
	}
	return hints
}
