package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/isolation/local"
)

// runOptions are the per-invocation overrides of the [run] config.
type runOptions struct {
	backend   string
	harness   string
	workspace string
	model     string
	args      []string
	allowHost bool
}

func newRunCommand(deps *Deps) *cobra.Command {
	var opts runOptions

	cmd := &cobra.Command{
		Use:   "run <task-id>",
		Short: "Run one task through an isolation backend and an agent harness",
		Long: "Run one task end-to-end: resolve its workspace, prepare the selected isolation\n" +
			"backend, run the selected harness, capture its output, and reflect progress back\n" +
			"into the store. The backend and harness are selected explicitly, by flag or by the\n" +
			"[run] config table; there is no default backend. The local backend is unsandboxed\n" +
			"and requires --allow-host (or run.allow_host).",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return deps.runTask(cmd, args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.backend, "backend", "", "isolation backend name (required; e.g. local)")
	cmd.Flags().StringVar(&opts.harness, "harness", "", "harness name (required; e.g. opencode)")
	cmd.Flags().StringVar(&opts.workspace, "workspace", "", "workspace root directory (default under the config dir)")
	cmd.Flags().StringVar(&opts.model, "model", "", "model override passed to the harness")
	cmd.Flags().StringArrayVar(&opts.args, "arg", nil, "extra argument passed to the harness (repeatable)")
	cmd.Flags().BoolVar(&opts.allowHost, "allow-host", false, "opt in to the unsandboxed local backend (dev-only)")
	return cmd
}

func (d *Deps) runTask(cmd *cobra.Command, taskID string, opts runOptions) error {
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

	if cfg.Backend == "" {
		return usageError(cmd, "--backend is required (set run.backend or FACTOTUM_RUN_BACKEND); available: %s", strings.Join(d.RunBackends.Names(), ", "))
	}
	if cfg.Harness == "" {
		return usageError(cmd, "--harness is required (set run.harness or FACTOTUM_RUN_HARNESS); available: %s", strings.Join(d.RunHarnesses.Names(), ", "))
	}
	workspaceRoot, err := runWorkspaceRoot(cfg.Workspace)
	if err != nil {
		return usageError(cmd, "%v", err)
	}

	backendFactory, err := d.RunBackends.MustLookup(cfg.Backend)
	if err != nil {
		return usageError(cmd, "unknown isolation backend %q; available: %s", cfg.Backend, strings.Join(d.RunBackends.Names(), ", "))
	}
	backend, err := backendFactory(cfg, d.Err)
	if err != nil {
		return err
	}
	harnessFactory, err := d.RunHarnesses.MustLookup(cfg.Harness)
	if err != nil {
		return usageError(cmd, "unknown harness %q; available: %s", cfg.Harness, strings.Join(d.RunHarnesses.Names(), ", "))
	}
	agentHarness, err := harnessFactory(cfg)
	if err != nil {
		return err
	}

	task, err := d.Tasks.Get(cmd.Context(), core.TaskID(taskID))
	if err != nil {
		return err
	}

	outcome, runErr := app.NewRunService(d.Backend, d.Tasks, d.Clock, d.IDs).Run(cmd.Context(), app.RunInput{
		TaskID:        task.ID,
		Backend:       backend,
		Harness:       agentHarness,
		WorkspaceRoot: workspaceRoot,
		Model:         cfg.Model,
		Args:          cfg.Args,
		Actor:         d.currentActorID(cmd.Context()),
	})
	if errors.Is(runErr, local.ErrNotOptedIn) {
		return usageError(cmd, "backend %q runs unsandboxed and is not opted in; pass --allow-host (or set run.allow_host) only for trusted work", cfg.Backend)
	}
	if outcome == nil {
		return runErr
	}
	if err := d.printRunOutcome(task, outcome, runErr); err != nil {
		return err
	}
	return runErr
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
