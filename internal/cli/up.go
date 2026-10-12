package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/internal/config"
	"github.com/khoinguyen/factotum/internal/groom"
	"github.com/khoinguyen/factotum/internal/serve"
	"github.com/khoinguyen/factotum/internal/up"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// upOptions are the per-invocation overrides of the `ft up` daemon.
type upOptions struct {
	bind        string
	project     string
	noAutoGroom bool
	unattended  bool
}

func newUpCommand(deps *Deps) *cobra.Command {
	var opts upOptions

	cmd := &cobra.Command{
		Use:   "up",
		Short: "Run the factory daemon: serve the dashboard and drive captures through the pipeline",
		Long: "Run the software-factory daemon: `ft serve` (the dashboard, the token-gated capture\n" +
			"write side, and the message transport) plus the controller that turns each stored\n" +
			"capture into a feature in flight. A capture stored through /api/capture starts a\n" +
			"pipeline automatically (config `up.auto_groom`, default on; disable with\n" +
			"--no-auto-groom). The controller runs each phase as a stage, stops at every human\n" +
			"gate, and reflects each transition into the store. After grooming it runs the\n" +
			"architecture review (a needs-rework verdict holds the pipeline at the design_rework\n" +
			"gate), then drives the produced tasks through build (a task awaiting merge holds at\n" +
			"the merge_approval gate) and QA; rollout is a no-op until its mechanics land. Groom\n" +
			"runs headless by default (config `up.unattended`, default on), deferring product\n" +
			"questions to the advisory groom_questions gate; on a terminal with\n" +
			"`up.unattended=false` it attaches you to the live groom session so you can answer\n" +
			"the grill. It is project-scoped: one project per process. `ft serve` is unchanged;\n" +
			"`ft up` is additive.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return deps.runUp(cmd, opts)
		},
	}
	cmd.Flags().StringVar(&opts.bind, "bind", defaultServeBind, "host:port to bind (loopback only by default)")
	cmd.Flags().StringVarP(&opts.project, "project", "p", "", "project id (defaults to the configured project)")
	cmd.Flags().BoolVar(&opts.noAutoGroom, "no-auto-groom", false, "do not start a pipeline when a capture is stored")
	cmd.Flags().BoolVar(&opts.unattended, "unattended", false, "groom and agent stages headless; without it a terminal attaches the PO to the groom session (overrides up.unattended)")
	cmd.AddCommand(
		newUpStatusCommand(deps), newUpListCommand(deps), newUpStartCommand(deps),
		newUpAnswerCommand(deps), newUpApproveCommand(deps),
		newUpOperatorCommand(deps, "merge <pipeline>",
			"Clear merge_approval after merging the pipeline's PRs",
			"Record that the operator merged the pull requests a build stage left awaiting\n"+
				"merge, then clear the blocking merge_approval gate. The produced tasks awaiting\n"+
				"review are marked done, so the resumed build stage sees the work resolved.",
			func(ctx context.Context, c *up.Controller, id core.PipelineID) (*core.Pipeline, error) {
				return c.Merge(ctx, id)
			}),
		newUpOperatorCommand(deps, "release <pipeline>",
			"Close the pipeline's release milestone and clear the release gate",
			"Close the release milestone that holds the pipeline at the blocking release gate\n"+
				"(the release decision, design §9) and clear the gate so the rollout stage proceeds.",
			func(ctx context.Context, c *up.Controller, id core.PipelineID) (*core.Pipeline, error) {
				return c.Release(ctx, id)
			}),
		newUpOperatorCommand(deps, "promote <pipeline>",
			"Approve the rollout promotion and clear the rollout gate",
			"Approve the rollout promotion and clear the blocking rollout gate so the rollout\n"+
				"stage proceeds.",
			func(ctx context.Context, c *up.Controller, id core.PipelineID) (*core.Pipeline, error) {
				return c.Promote(ctx, id)
			}),
		newUpOperatorCommand(deps, "pause <pipeline>", "Hold a pipeline at its current phase",
			"Hold a pipeline at the phase it is in. A paused pipeline is not auto-resumed by a\n"+
				"daemon restart; resume it with `ft up resume`.",
			func(ctx context.Context, c *up.Controller, id core.PipelineID) (*core.Pipeline, error) {
				return c.Pause(ctx, id)
			}),
		newUpOperatorCommand(deps, "resume <pipeline>", "Resume a paused pipeline",
			"Return a paused pipeline to the phase it held, so the controller resumes that\nstage.",
			func(ctx context.Context, c *up.Controller, id core.PipelineID) (*core.Pipeline, error) {
				return c.Resume(ctx, id)
			}),
		newUpOperatorCommand(deps, "retry <pipeline>", "Retry a failed pipeline",
			"Return a failed pipeline to the phase it failed in and clear the failure reason,\n"+
				"so the controller resumes that stage.",
			func(ctx context.Context, c *up.Controller, id core.PipelineID) (*core.Pipeline, error) {
				return c.Retry(ctx, id)
			}),
		newUpOperatorCommand(deps, "cancel <pipeline>", "Cancel a pipeline",
			"Terminate a pipeline from any non-terminal state. cancelled is immutable, like done.",
			func(ctx context.Context, c *up.Controller, id core.PipelineID) (*core.Pipeline, error) {
				return c.Cancel(ctx, id)
			}),
	)
	return cmd
}

// runUp starts the daemon: the serve server with the controller wired to the
// capture write path, and the controller's supervisor loop.
func (d *Deps) runUp(cmd *cobra.Command, opts upOptions) error {
	ctx := cmd.Context()
	d.warnIncompleteHub()
	projectID := d.resolveProject(opts.project)
	if err := requireProject(cmd, projectID); err != nil {
		return err
	}
	target, err := d.openGroomTarget(ctx, projectID)
	if err != nil {
		return err
	}
	defer target.close()
	backend := target.backend
	tasks := app.NewTicketService(backend, d.Clock, d.IDs)
	messages := app.NewMessageService(backend, d.Clock, d.IDs)

	autoGroom := d.Config.Up.AutoGroom
	if opts.noAutoGroom {
		autoGroom = false
	}
	unattended := d.Config.Up.Unattended
	if cmd.Flags().Changed("unattended") {
		unattended = opts.unattended
	}
	// Attach the PO only when the operator opted out of unattended mode and
	// `ft up` runs on a terminal. A headless daemon always grooms unattended,
	// deferring product questions to the advisory groom_questions gate rather
	// than waiting on a PO who is not there (design §3/§8/§11).
	interactive := d.runInteractive(cmd, unattended)

	// The agent stages (groom, review, QA) need a harness and sandbox. With them
	// configured, resolve the selection once so a bad name fails the daemon
	// immediately. Without them the daemon still serves and captures; the first
	// agent stage parks the pipeline failed with a clear message instead of
	// prompting on a goroutine.
	sel, err := d.upRunSelection(cmd)
	if err != nil {
		return err
	}
	controller, err := up.New(up.Options{
		Backend: backend,
		Clock:   d.Clock,
		IDs:     d.IDs,
		Project: projectID,
		Stages:  d.upStages(cmd, target, sel, interactive),
		Logf: func(format string, args ...any) {
			_, _ = fmt.Fprintf(d.Err, "ft up: "+format+"\n", args...)
		},
	})
	if err != nil {
		return err
	}

	ranker, err := d.Rankers.MustLookup("composite")
	if err != nil {
		return err
	}
	server, err := serve.New(serve.Options{
		Backend:    backend,
		Clock:      d.Clock,
		Ranker:     ranker,
		Project:    projectID,
		Token:      d.Config.Serve.Token,
		Tasks:      tasks,
		Messages:   messages,
		AutoGroom:  autoGroom,
		Controller: controller,
	})
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", opts.bind)
	if err != nil {
		return fmt.Errorf("up: listen %s: %w", opts.bind, err)
	}

	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	var worker sync.WaitGroup
	worker.Add(1)
	go func() {
		defer worker.Done()
		_ = controller.Start(runCtx)
	}()

	dashboard := &http.Server{Handler: server.Handler()}
	_, _ = fmt.Fprintf(d.Err, "factotum factory: http://%s (%s; auto-groom %s; Ctrl-C to stop)\n",
		listener.Addr().String(), serveMode(false, d.Config.Serve.Token), onOff(autoGroom))
	go func() {
		<-runCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = dashboard.Shutdown(shutdownCtx)
	}()
	if err := dashboard.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	// Drain the controller so an interrupted stage leaves the pipeline in its
	// in-flight state (resumable) before the process exits.
	worker.Wait()
	return nil
}

// upRunSelection resolves the run selection the daemon's agent stages share, or
// nil when no [run] harness and sandbox are configured. A nil selection is not
// fatal: the daemon still serves and captures, and each agent stage parks its
// pipeline failed with a clear message rather than prompting on a goroutine.
func (d *Deps) upRunSelection(cmd *cobra.Command) (*runSelection, error) {
	if d.Config.Run.Sandbox == "" || d.Config.Run.Harness == "" {
		return nil, nil
	}
	return d.prepareRun(cmd, runOptions{})
}

// upStages builds the daemon's stage runners: the agent stages (groom, review,
// QA) share one resolved run selection, the build stage drives the produced
// tasks through the run loop, and rollout is a no-op until its mechanics land
// (design §8/§9).
func (d *Deps) upStages(cmd *cobra.Command, target groomTarget, sel *runSelection, interactive bool) map[core.PipelineState]up.StageRunner {
	projectRoot := config.ProjectRoot(d.ProjectConfigPath)
	return map[core.PipelineState]up.StageRunner{
		core.PipelineGrooming: &up.GroomRunner{DataDir: target.dataDir, IDs: d.IDs, Session: d.upGroomSession(cmd, target, sel, interactive)},
		core.PipelineReview:   &up.ReviewRunner{DataDir: target.dataDir, Review: d.upReviewFunc(target, projectRoot, sel, interactive)},
		core.PipelineBuilding: &up.BuildRunner{Backend: target.backend, Build: d.upBuildFunc(target, sel)},
		core.PipelineQA:       &up.QARunner{Backend: target.backend, QA: d.upQAFunc(target, projectRoot, sel, interactive)},
		core.PipelineRollout:  up.RolloutRunner{},
	}
}

// upGroomSession builds the groom stage's session function: it scopes a groom
// session to the pipeline's capture, runs it through the shared groom path, and
// reports the produced tasks and whether product questions were deferred. When a
// PO is attached (interactive) the session runs with the PO's terminal; with no
// PO it runs unattended, deferring every product question.
func (d *Deps) upGroomSession(cmd *cobra.Command, target groomTarget, sel *runSelection, interactive bool) up.SessionFunc {
	return func(ctx context.Context, p *core.Pipeline, sessionID string) (up.SessionResult, error) {
		if sel == nil {
			return up.SessionResult{}, errUpNoRunConfig
		}
		project, err := target.projects.Get(ctx, p.ProjectID)
		if err != nil {
			return up.SessionResult{}, err
		}
		capture, err := target.tasks.Get(ctx, p.CaptureID)
		if err != nil {
			return up.SessionResult{}, err
		}
		promptBytes, err := os.ReadFile(groom.PromptPath)
		if err != nil {
			return up.SessionResult{}, fmt.Errorf("read session prompt %s: %w", groom.PromptPath, err)
		}
		record, err := d.groomSession(ctx, cmd, target, project, []*core.Ticket{capture}, sessionID, promptBytes, !interactive, sel, interactive)
		if err != nil {
			return up.SessionResult{}, err
		}
		return up.SessionResult{
			Produced: producedTicketIDs(record.Produced),
			Deferred: deferredQuestions(groom.DeferredQuestionsPath(target.dataDir, record.ID)),
			Detail:   "groom session " + record.ID,
		}, nil
	}
}

// deferredQuestions reports whether a session's deferred-questions file holds an
// open question (an unchecked box that is not the template placeholder). It
// decides the advisory groom_questions gate.
func deferredQuestions(path string) bool {
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- [ ]") && !strings.Contains(trimmed, "<question>") {
			return true
		}
	}
	return false
}

// producedTicketIDs converts a session record's produced ids into ticket ids.
func producedTicketIDs(ids []string) []core.TicketID {
	if len(ids) == 0 {
		return nil
	}
	out := make([]core.TicketID, 0, len(ids))
	for _, id := range ids {
		out = append(out, core.TicketID(id))
	}
	return out
}

// errUpNoRunConfig is the failure an agent stage reports when the daemon has no
// [run] harness and sandbox, so the operator knows exactly what to configure.
var errUpNoRunConfig = errors.New("up: run.sandbox and run.harness are required to run an agent stage; set them in the [run] config table")

// upAgentRunProject runs one project-scoped agent prompt through the run
// service, sharing the groom path's store env and workspace resolution so the
// dispatched agent reads and writes the same store. It is the shared dispatch
// for the review and QA stages; the build stage drives the run loop instead.
func (d *Deps) upAgentRunProject(ctx context.Context, target groomTarget, projectID core.ProjectID, sel *runSelection, prompt string, interactive bool) error {
	if sel == nil {
		return errUpNoRunConfig
	}
	_, runErr := app.NewRunService(target.backend, target.tasks, d.Clock, d.IDs).RunProject(ctx, app.ProjectRunInput{
		ProjectID:        projectID,
		Backend:          sel.backend,
		Harness:          sel.harness,
		WorkspaceRoot:    sel.workspace,
		RepoBase:         sel.repoBase,
		WorkspaceRefresh: sel.refresh,
		Model:            sel.model,
		Args:             sel.args,
		Prompt:           prompt,
		StoreEnv:         d.storeEnvFor(target.store),
		Interactive:      interactive,
		OnResolve:        d.warnLocalPlan,
	})
	return runErr
}

// upSkillPrompt reads a role skill's instructions from the project's skill tree
// (.agents/skills/<name>/SKILL.md), which the daemon dispatches as its agent
// prompt. It errors clearly when the skill is absent, so a missing reviewer is
// never dispatched as a no-op.
func upSkillPrompt(projectRoot, name string) (string, error) {
	if projectRoot == "" {
		return "", fmt.Errorf("up: cannot locate the project root to read the %s skill; run from the project or pass -c <project config>", name)
	}
	path := filepath.Join(projectRoot, ".agents", "skills", name, "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s skill %s: %w", name, path, err)
	}
	return string(data), nil
}

// upReviewFunc builds the review stage's dispatch: it runs the
// architecture-reviewer over the session's documents through the agent harness;
// the reviewer records its verdict with `ft groom review`, which the runner then
// reads back and maps to the gate. The runner is idempotent, so a restart does
// not re-review.
func (d *Deps) upReviewFunc(target groomTarget, projectRoot string, sel *runSelection, interactive bool) up.ReviewFunc {
	return func(ctx context.Context, p *core.Pipeline, sessionID string) (up.ReviewOutcome, error) {
		skill, err := upSkillPrompt(projectRoot, "architecture-reviewer")
		if err != nil {
			return up.ReviewOutcome{}, err
		}
		prompt := skill + "\n\n" + reviewKickoff(target.dataDir, sessionID)
		if err := d.upAgentRunProject(ctx, target, p.ProjectID, sel, prompt, interactive); err != nil {
			return up.ReviewOutcome{}, err
		}
		record, err := groom.ReadSession(target.dataDir, sessionID)
		if err != nil {
			return up.ReviewOutcome{}, err
		}
		return up.ReviewOutcome{
			Verdict: groom.ReviewVerdict(record.ReviewVerdict),
			Detail:  "architecture review of session " + sessionID,
		}, nil
	}
}

// reviewKickoff names the session, its documents, and the verdict-recording
// command, so the dispatched reviewer knows what to read and how to record its
// one verdict (which the runner reads from the session manifest).
func reviewKickoff(dataDir, sessionID string) string {
	return fmt.Sprintf("## Review kickoff\n\nSession: %s\n\nRead the feature documents for this session:\n- %s\n- %s\n- %s\n- %s\n\nRecord exactly one tech-design verdict (approve|approve-with-changes|needs-rework) once you have reviewed them:\n\nft groom review %s --verdict <verdict> -f <review-file>\n",
		sessionID,
		groom.SpecPath(dataDir, sessionID),
		groom.PlanPath(dataDir, sessionID),
		groom.TechDesignPath(dataDir, sessionID),
		groom.ReportPath(dataDir, sessionID),
		sessionID,
	)
}

// upQAFunc builds the QA stage's dispatch: it runs the single-task-qa agent over
// the pipeline's capture; the agent records its verdict with `ft qa record`,
// which the runner then reads back.
func (d *Deps) upQAFunc(target groomTarget, projectRoot string, sel *runSelection, interactive bool) up.QAFunc {
	return func(ctx context.Context, p *core.Pipeline) (up.QAOutcome, error) {
		skill, err := upSkillPrompt(projectRoot, "single-task-qa")
		if err != nil {
			return up.QAOutcome{}, err
		}
		prompt := skill + "\n\n" + qaKickoff(p.CaptureID)
		if err := d.upAgentRunProject(ctx, target, p.ProjectID, sel, prompt, interactive); err != nil {
			return up.QAOutcome{}, err
		}
		return up.QAOutcome{Detail: fmt.Sprintf("feature %s QA dispatched", p.CaptureID)}, nil
	}
}

// qaKickoff names the capture and the verdict-recording command, so the
// dispatched QA agent knows what to verify and how to record its verdict.
func qaKickoff(capture core.TicketID) string {
	return fmt.Sprintf("## QA kickoff\n\nVerify the feature captured as %s end to end, then record one verdict:\n\nft qa record %s --verdict pass|fail -f <findings-file>\n", capture, capture)
}

// upBuildFunc builds the build stage's dispatch: it drives every task the
// capture produced to done through the run loop, which sets each built task
// ready_for_review for a human to merge. The BuildRunner then reads the durable
// task state back and preserves the merge gate.
func (d *Deps) upBuildFunc(target groomTarget, sel *runSelection) up.BuildFunc {
	return func(ctx context.Context, p *core.Pipeline) (up.BuildOutcome, error) {
		if sel == nil {
			return up.BuildOutcome{}, errUpNoRunConfig
		}
		ranker, err := d.Rankers.MustLookup("composite")
		if err != nil {
			return up.BuildOutcome{}, err
		}
		svc := app.NewRunService(target.backend, target.tasks, d.Clock, d.IDs)
		runner := func(ctx context.Context, taskID core.TicketID) (*app.RunOutcome, error) {
			return svc.Run(ctx, app.RunInput{
				TicketID:         taskID,
				Backend:          sel.backend,
				Harness:          sel.harness,
				WorkspaceRoot:    sel.workspace,
				RepoBase:         sel.repoBase,
				WorkspaceRefresh: sel.refresh,
				Model:            sel.model,
				Args:             sel.args,
				MsgURL:           d.Config.Serve.URL,
				ServeToken:       d.Config.Serve.Token,
				StoreEnv:         d.storeEnvFor(target.store),
				OnResolve:        d.warnLocalPlan,
			})
		}
		outcome, runErr := app.NewRunLoopService(target.backend, ranker, d.Clock).Run(ctx, app.LoopInput{GoalID: p.CaptureID, Runner: runner})
		if outcome != nil {
			return up.BuildOutcome{Detail: fmt.Sprintf("build loop stopped: %s", outcome.Stop)}, runErr
		}
		return up.BuildOutcome{}, runErr
	}
}

// upPipelineDoc is the lossless structured shape of a pipeline row.
type upPipelineDoc struct {
	Pipeline string `json:"pipeline" yaml:"pipeline"`
	Capture  string `json:"capture" yaml:"capture"`
	State    string `json:"state" yaml:"state"`
	Gate     string `json:"gate" yaml:"gate"`
	// Error is the reason a failed pipeline parked, so a parked pipeline is
	// diagnosed from the read surface, not only the event log.
	Error    string   `json:"error,omitempty" yaml:"error,omitempty"`
	Session  string   `json:"session,omitempty" yaml:"session,omitempty"`
	Produced []string `json:"produced,omitempty" yaml:"produced,omitempty"`
	Project  string   `json:"project" yaml:"project"`
	Repo     string   `json:"repo" yaml:"repo"`
}

func newUpStatusCommand(deps *Deps) *cobra.Command {
	var projectID string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show each pipeline's phase and open gate",
		Long: "Show the project's pipelines: the origin capture, the phase, the open gate (when\n" +
			"one is waiting on a human), the groom session, and the tasks produced so far.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return deps.runUpStatus(cmd, projectID)
		},
	}
	cmd.Flags().StringVarP(&projectID, "project", "p", "", "project id (defaults to the configured project)")
	return cmd
}

func (d *Deps) runUpStatus(cmd *cobra.Command, projectID string) error {
	// A read: with no project resolved, fall back to the whole store (the
	// configured project's), like the other list commands.
	project := d.resolveProject(projectID)
	pipelines, err := d.upPipelines(cmd.Context(), project)
	if err != nil {
		return err
	}
	docs := make([]upPipelineDoc, 0, len(pipelines))
	for _, p := range pipelines {
		docs = append(docs, upPipelineDocOf(p))
	}
	return d.emit(docs, func() {
		if len(docs) == 0 {
			d.printf("no pipelines for project %s\n", project)
			return
		}
		for _, doc := range docs {
			fields := []field{
				f("pipeline", doc.Pipeline),
				f("capture", doc.Capture),
				f("state", doc.State),
				f("gate", gateValue(doc.Gate)),
				f("session", doc.Session),
				f("produced", strings.Join(doc.Produced, ", ")),
			}
			if doc.Error != "" {
				fields = append(fields, f("error", doc.Error))
			}
			fields = append(fields,
				f("project", doc.Project),
				f("repo", d.repoValue(doc.Repo)),
			)
			d.printFields(fields...)
			d.printf("\n")
		}
	}, upPipelineHints(pipelines)...)
}

func newUpListCommand(deps *Deps) *cobra.Command {
	var projectID string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the project's pipelines",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return deps.runUpList(cmd, projectID)
		},
	}
	cmd.Flags().StringVarP(&projectID, "project", "p", "", "project id (defaults to the configured project)")
	return cmd
}

func (d *Deps) runUpList(cmd *cobra.Command, projectID string) error {
	project := d.resolveProject(projectID)
	pipelines, err := d.upPipelines(cmd.Context(), project)
	if err != nil {
		return err
	}
	docs := make([]upPipelineDoc, 0, len(pipelines))
	for _, p := range pipelines {
		docs = append(docs, upPipelineDocOf(p))
	}
	return d.emit(docs, func() {
		rows := make([][]string, 0, len(docs))
		for _, doc := range docs {
			rows = append(rows, []string{doc.Pipeline, doc.Capture, doc.State, gateValue(doc.Gate), doc.Project})
		}
		d.printTable([]string{"PIPELINE", "CAPTURE", "STATE", "GATE", "PROJECT"}, rows)
	})
}

func newUpStartCommand(deps *Deps) *cobra.Command {
	var projectID string
	cmd := &cobra.Command{
		Use:   "start <capture>",
		Short: "Enqueue a pipeline for a stored capture by hand",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return deps.runUpStart(cmd, projectID, args[0])
		},
	}
	cmd.Flags().StringVarP(&projectID, "project", "p", "", "project id (defaults to the configured project)")
	return cmd
}

func (d *Deps) runUpStart(cmd *cobra.Command, projectID, captureID string) error {
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
	capture, err := target.tasks.Get(ctx, core.TicketID(captureID))
	if err != nil {
		return err
	}
	if capture.ProjectID != project {
		return usageError(cmd, "capture %s belongs to project %s, not %s", capture.ID, capture.ProjectID, project)
	}
	pipeline, err := controller.Enqueue(ctx, capture)
	if err != nil {
		return err
	}
	doc := upPipelineDoc{Project: string(project), Repo: d.repoValue("")}
	if pipeline != nil {
		doc = upPipelineDocOf(pipeline)
		doc.Repo = d.repoValue("")
	}
	return d.emit(doc, func() {
		if pipeline == nil {
			d.printf("pipeline: (none)\ncapture: %s\nproject: %s\nrepo: %s\n", capture.ID, project, d.repoValue(""))
			return
		}
		d.printFields(
			f("pipeline", doc.Pipeline),
			f("capture", doc.Capture),
			f("state", doc.State),
			f("project", doc.Project),
			f("repo", doc.Repo),
		)
	})
}

// upPipelines lists a project's pipelines, resolving the named project's store
// (the configured one, or another registered project's), so `-p` reads the
// project it names rather than the caller's.
func (d *Deps) upPipelines(ctx context.Context, project core.ProjectID) ([]*core.Pipeline, error) {
	target, err := d.openGroomTarget(ctx, project)
	if err != nil {
		return nil, err
	}
	defer target.close()
	return target.backend.Pipelines().List(ctx, store.PipelineFilter{ProjectID: project})
}

func upPipelineDocOf(p *core.Pipeline) upPipelineDoc {
	produced := make([]string, 0, len(p.Produced))
	for _, id := range p.Produced {
		produced = append(produced, string(id))
	}
	return upPipelineDoc{
		Pipeline: string(p.ID),
		Capture:  string(p.CaptureID),
		State:    string(p.State),
		Gate:     string(p.Gate),
		Error:    p.Error,
		Session:  p.Session,
		Produced: produced,
		Project:  string(p.ProjectID),
	}
}

// gateValue renders a gate for display: a dash when none is open.
func gateValue(gate string) string {
	if gate == "" {
		return "-"
	}
	return gate
}

// onOff renders a boolean as on or off for a startup line.
func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
