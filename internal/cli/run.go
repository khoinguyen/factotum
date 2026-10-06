package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	harnesspkg "github.com/khoinguyen/factotum/pkg/harness"
	"github.com/khoinguyen/factotum/pkg/isolation"
	"github.com/khoinguyen/factotum/pkg/isolation/local"
	"github.com/khoinguyen/factotum/pkg/isolation/openshell"
)

// runOptions are the per-invocation overrides of the [run] config.
type runOptions struct {
	backend   string
	harness   string
	workspace string
	model     string
	args      []string
	allowHost bool
	goal      string
	maxTasks  int
}

func newRunCommand(deps *Deps) *cobra.Command {
	var opts runOptions

	cmd := &cobra.Command{
		Use:   "run [task-id]",
		Short: "Run a task, or drive the DAG loop toward a goal",
		Long: "Run one task end-to-end, or with --goal drive the graph toward a goal. In single\n" +
			"task mode `ft run <task-id>` resolves the task's workspace, prepares the selected\n" +
			"isolation backend, runs the selected harness, captures its output, and reflects\n" +
			"progress back into the store. With --goal the launcher repeatedly selects the\n" +
			"highest-ranked startable task on the path to the goal, runs it, and re-reads the\n" +
			"graph until the goal is reached, no work is ready, on-path work is blocked, or the\n" +
			"--max-tasks budget is exhausted. The backend and harness are selected explicitly,\n" +
			"by flag or by the [run] config table; there is no default backend. The local backend\n" +
			"is unsandboxed and requires --allow-host (or run.allow_host).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.goal != "" && len(args) > 0 {
				return usageError(cmd, "give either a task id or --goal, not both")
			}
			if opts.goal == "" && len(args) == 0 {
				return usageError(cmd, "a task id or --goal is required")
			}
			if opts.maxTasks < 0 {
				return usageError(cmd, "--max-tasks must not be negative")
			}
			if opts.goal != "" {
				return deps.runGoal(cmd, opts.goal, opts)
			}
			return deps.runTask(cmd, args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.backend, "backend", "", "isolation backend name (required; e.g. local, openshell)")
	cmd.Flags().StringVar(&opts.harness, "harness", "", "harness name (required; e.g. opencode)")
	cmd.Flags().StringVar(&opts.workspace, "workspace", "", "workspace root directory (default under the config dir)")
	cmd.Flags().StringVar(&opts.model, "model", "", "model override passed to the harness")
	cmd.Flags().StringArrayVar(&opts.args, "arg", nil, "extra argument passed to the harness (repeatable)")
	cmd.Flags().BoolVar(&opts.allowHost, "allow-host", false, "opt in to the unsandboxed local backend (dev-only)")
	cmd.Flags().StringVar(&opts.goal, "goal", "", "drive the DAG loop toward this goal task or milestone")
	cmd.Flags().IntVar(&opts.maxTasks, "max-tasks", 0, "stop the loop after this many task runs (0 = no budget)")
	return cmd
}

// runSelection is the resolved backend, harness, and workspace a run uses.
type runSelection struct {
	backend     isolation.IsolationBackend
	backendName string
	harness     harnesspkg.Harness
	workspace   string
	model       string
	args        []string
}

// prepareRun merges the per-invocation overrides into the [run] config and
// builds the selected backend and harness. Selection is explicit: there is no
// default backend, and an unknown name is a usage error.
func (d *Deps) prepareRun(cmd *cobra.Command, opts runOptions) (*runSelection, error) {
	cfg := d.Config.Run
	if opts.backend != "" {
		cfg.Backend = opts.backend
	}
	if opts.harness != "" {
		cfg.Harness = opts.harness
	}
	if opts.workspace != "" {
		cfg.Workspace = opts.workspace
	}
	if opts.model != "" {
		cfg.Model = opts.model
	}
	if len(opts.args) > 0 {
		cfg.Args = append(append([]string(nil), cfg.Args...), opts.args...)
	}
	if opts.allowHost {
		cfg.AllowHost = true
	}
	cfg.PolicyPath = projectPolicyPath(d.ProjectConfigPath)

	if cfg.Backend == "" {
		return nil, usageError(cmd, "--backend is required (set run.backend or FACTOTUM_RUN_BACKEND); available: %s", strings.Join(d.RunBackends.Names(), ", "))
	}
	if cfg.Harness == "" {
		return nil, usageError(cmd, "--harness is required (set run.harness or FACTOTUM_RUN_HARNESS); available: %s", strings.Join(d.RunHarnesses.Names(), ", "))
	}
	workspaceRoot, err := runWorkspaceRoot(cfg.Workspace)
	if err != nil {
		return nil, usageError(cmd, "%v", err)
	}

	backendFactory, err := d.RunBackends.MustLookup(cfg.Backend)
	if err != nil {
		return nil, usageError(cmd, "unknown isolation backend %q; available: %s", cfg.Backend, strings.Join(d.RunBackends.Names(), ", "))
	}
	backend, err := backendFactory(cfg, d.Err)
	if err != nil {
		return nil, err
	}
	harnessFactory, err := d.RunHarnesses.MustLookup(cfg.Harness)
	if err != nil {
		return nil, usageError(cmd, "unknown harness %q; available: %s", cfg.Harness, strings.Join(d.RunHarnesses.Names(), ", "))
	}
	agentHarness, err := harnessFactory(cfg)
	if err != nil {
		return nil, err
	}
	return &runSelection{
		backend:     backend,
		backendName: backend.Name(),
		harness:     agentHarness,
		workspace:   workspaceRoot,
		model:       cfg.Model,
		args:        cfg.Args,
	}, nil
}

func (d *Deps) runTask(cmd *cobra.Command, taskID string, opts runOptions) error {
	sel, err := d.prepareRun(cmd, opts)
	if err != nil {
		return err
	}

	task, err := d.Tasks.Get(cmd.Context(), core.TaskID(taskID))
	if err != nil {
		return err
	}

	outcome, runErr := app.NewRunService(d.Backend, d.Tasks, d.Clock, d.IDs).Run(cmd.Context(), app.RunInput{
		TaskID:        task.ID,
		Backend:       sel.backend,
		Harness:       sel.harness,
		WorkspaceRoot: sel.workspace,
		Model:         sel.model,
		Args:          sel.args,
		Actor:         d.currentActorID(cmd.Context()),
	})
	if errors.Is(runErr, local.ErrNotOptedIn) {
		return usageError(cmd, "backend %q runs unsandboxed and is not opted in; pass --allow-host (or set run.allow_host) only for trusted work", sel.backendName)
	}
	if outcome == nil {
		return runErr
	}
	if err := d.printRunOutcome(task, outcome, runErr); err != nil {
		return err
	}
	return runErr
}

// runGoal drives the DAG loop toward goal, running each selected task through
// the same single-task service.
func (d *Deps) runGoal(cmd *cobra.Command, goalID string, opts runOptions) error {
	sel, err := d.prepareRun(cmd, opts)
	if err != nil {
		return err
	}
	goal, err := d.Tasks.Get(cmd.Context(), core.TaskID(goalID))
	if err != nil {
		return err
	}
	ranker, err := d.Rankers.MustLookup("composite")
	if err != nil {
		return err
	}

	svc := app.NewRunService(d.Backend, d.Tasks, d.Clock, d.IDs)
	runner := func(ctx context.Context, taskID core.TaskID) (*app.RunOutcome, error) {
		return svc.Run(ctx, app.RunInput{
			TaskID:        taskID,
			Backend:       sel.backend,
			Harness:       sel.harness,
			WorkspaceRoot: sel.workspace,
			Model:         sel.model,
			Args:          sel.args,
			Actor:         d.currentActorID(ctx),
		})
	}

	text := !d.structured()
	outcome, runErr := app.NewRunLoopService(d.Backend, ranker, d.Clock).Run(cmd.Context(), app.LoopInput{
		GoalID:   goal.ID,
		Runner:   runner,
		MaxTasks: opts.maxTasks,
		OnStep: func(step app.LoopStep) {
			if text {
				d.printLoopStep(step)
			}
		},
	})
	if errors.Is(runErr, local.ErrNotOptedIn) {
		return usageError(cmd, "backend %q runs unsandboxed and is not opted in; pass --allow-host (or set run.allow_host) only for trusted work", sel.backendName)
	}
	if outcome == nil {
		return runErr
	}
	if err := d.printLoopOutcome(goal, outcome, runErr); err != nil {
		return err
	}
	return runErr
}

// projectPolicyPath resolves the project's committed OpenShell policy override
// from the loaded project config path (<root>/.factotum/config.toml). It is
// empty when the project root is unknown, which means no opt-in.
func projectPolicyPath(projectConfigPath string) string {
	if projectConfigPath == "" {
		return ""
	}
	return openshell.OverridePath(filepath.Dir(filepath.Dir(projectConfigPath)))
}

// runWorkspaceRoot resolves the workspace root: the configured value, or a
// per-user default under the config directory.
func runWorkspaceRoot(value string) (string, error) {
	if value != "" {
		if !filepath.IsAbs(value) {
			return "", fmt.Errorf("workspace %q must be an absolute path", value)
		}
		return value, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine a workspace root; pass --workspace")
	}
	return filepath.Join(home, ".factotum", "workspaces"), nil
}

// runDoc is the lossless structured shape of `ft run`.
type runDoc struct {
	TaskID   string `json:"task_id" yaml:"task_id"`
	Run      string `json:"run" yaml:"run"`
	Status   string `json:"status" yaml:"status"`
	ExitCode int    `json:"exit_code" yaml:"exit_code"`
	Complete bool   `json:"complete" yaml:"complete"`
	Output   string `json:"output,omitempty" yaml:"output,omitempty"`
	Project  string `json:"project" yaml:"project"`
	Repo     string `json:"repo" yaml:"repo"`
}

func (d *Deps) printRunOutcome(task *core.Task, outcome *app.RunOutcome, runErr error) error {
	state := "finished"
	if runErr != nil {
		state = "failed"
	}
	doc := runDoc{
		TaskID:   string(task.ID),
		Run:      state,
		Status:   string(outcome.Status),
		ExitCode: outcome.ExitCode,
		Complete: outcome.Complete,
		Output:   outcome.Output,
		Project:  string(task.ProjectID),
		Repo:     task.Repo,
	}
	return d.emit(doc, func() {
		d.printFields(
			f("task_id", task.ID),
			f("run", state),
			f("status", outcome.Status),
			f("exit_code", outcome.ExitCode),
			f("complete", outcome.Complete),
			f("project", task.ProjectID),
			f("repo", d.repoValue(task.Repo)),
		)
		if output := strings.TrimSpace(outcome.Output); output != "" {
			d.printf("\n%s\n", output)
		}
	}, d.runHints(task, runErr)...)
}

func (d *Deps) runHints(task *core.Task, runErr error) []hint {
	id := string(task.ID)
	if runErr != nil {
		return []hint{{Command: fmt.Sprintf("ft task get %s", id), About: "inspect the failed run"}}
	}
	return []hint{
		{Command: fmt.Sprintf("ft task get %s", id), About: "inspect the task"},
		{Command: fmt.Sprintf("ft task note create %s --body \"PR: <url>\" --link pr=<url>", id), About: "attach the PR link"},
	}
}

// runLoopStepDoc is one iteration of `ft run --goal` in structured output.
type runLoopStepDoc struct {
	Iteration int    `json:"iteration" yaml:"iteration"`
	TaskID    string `json:"task_id" yaml:"task_id"`
	Title     string `json:"title" yaml:"title"`
	Run       string `json:"run" yaml:"run"`
	Status    string `json:"status" yaml:"status"`
	ExitCode  int    `json:"exit_code" yaml:"exit_code"`
	Complete  bool   `json:"complete" yaml:"complete"`
	Remaining int    `json:"remaining" yaml:"remaining"`
	Output    string `json:"output,omitempty" yaml:"output,omitempty"`
}

// runLoopDoc is the lossless structured shape of `ft run --goal`.
type runLoopDoc struct {
	Goal       string           `json:"goal" yaml:"goal"`
	GoalKind   string           `json:"goal_kind" yaml:"goal_kind"`
	GoalStatus string           `json:"goal_status" yaml:"goal_status"`
	Stop       string           `json:"stop" yaml:"stop"`
	Iterations int              `json:"iterations" yaml:"iterations"`
	Remaining  int              `json:"remaining" yaml:"remaining"`
	Steps      []runLoopStepDoc `json:"steps" yaml:"steps"`
	Project    string           `json:"project" yaml:"project"`
	Repo       string           `json:"repo" yaml:"repo"`
}

// printLoopStep streams one iteration's compact progress block.
func (d *Deps) printLoopStep(step app.LoopStep) {
	state := "finished"
	if step.Failed {
		state = "failed"
	}
	d.printFields(
		f("step", step.Iteration),
		f("task_id", step.TaskID),
		f("title", step.Title),
		f("run", state),
		f("status", step.Status),
		f("exit_code", step.ExitCode),
		f("remaining", step.Remaining),
	)
	d.printf("\n")
}

func (d *Deps) printLoopOutcome(goal *core.Task, outcome *app.LoopOutcome, runErr error) error {
	doc := runLoopDoc{
		Goal:       string(outcome.Goal),
		GoalKind:   string(outcome.GoalKind),
		GoalStatus: string(outcome.GoalStatus),
		Stop:       string(outcome.Stop),
		Iterations: len(outcome.Steps),
		Remaining:  outcome.Remaining,
		Project:    string(goal.ProjectID),
		Repo:       goal.Repo,
	}
	for _, step := range outcome.Steps {
		doc.Steps = append(doc.Steps, loopStepDoc(step))
	}
	return d.emit(doc, func() {
		d.printFields(
			f("goal", outcome.Goal),
			f("goal_kind", outcome.GoalKind),
			f("goal_status", outcome.GoalStatus),
			f("stop", outcome.Stop),
			f("iterations", len(outcome.Steps)),
			f("remaining", outcome.Remaining),
			f("project", goal.ProjectID),
			f("repo", d.repoValue(goal.Repo)),
		)
	}, d.runLoopHints(goal, outcome, runErr)...)
}

func loopStepDoc(step app.LoopStep) runLoopStepDoc {
	state := "finished"
	if step.Failed {
		state = "failed"
	}
	return runLoopStepDoc{
		Iteration: step.Iteration,
		TaskID:    string(step.TaskID),
		Title:     step.Title,
		Run:       state,
		Status:    string(step.Status),
		ExitCode:  step.ExitCode,
		Complete:  step.Complete,
		Remaining: step.Remaining,
		Output:    step.Output,
	}
}

func (d *Deps) runLoopHints(goal *core.Task, outcome *app.LoopOutcome, runErr error) []hint {
	id := string(goal.ID)
	if runErr != nil {
		return []hint{{Command: fmt.Sprintf("ft task get %s", id), About: "inspect the failed goal run"}}
	}
	switch outcome.Stop {
	case app.StopGoalReached:
		return []hint{{Command: fmt.Sprintf("ft task get %s", id), About: "review the goal"}}
	case app.StopBudgetExhausted:
		return []hint{{Command: fmt.Sprintf("ft run --goal %s", id), About: "resume the loop"}}
	default:
		return []hint{
			{Command: fmt.Sprintf("ft task next --toward %s", id), About: "see what is startable"},
			{Command: fmt.Sprintf("ft task get %s", id), About: "inspect the goal"},
		}
	}
}
