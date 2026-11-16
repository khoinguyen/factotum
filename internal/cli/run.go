package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/internal/config"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	harnesspkg "github.com/khoinguyen/factotum/pkg/harness"
	"github.com/khoinguyen/factotum/pkg/isolation"
	"github.com/khoinguyen/factotum/pkg/isolation/local"
	"github.com/khoinguyen/factotum/pkg/isolation/openshell"
	"github.com/khoinguyen/factotum/pkg/workspace"
)

// runOptions are the per-invocation overrides of the [run] config.
type runOptions struct {
	project        string
	sandbox        string
	harness        string
	workspace      string
	model          string
	image          string
	user           string
	args           []string
	allowHost      bool
	refresh        bool
	goal           string
	unattended     bool
	maxTasks       int
	promptFile     string
	promptArtifact string
}

// errRunInterrupted reports a run an operator stopped with INT or TERM. The run
// is cancelled rather than the process ended on the spot, and this is what the
// interrupted run returns, so the outcome reads as an interruption instead of as
// whatever the harness made of losing its command.
var errRunInterrupted = errors.New("run interrupted")

func newRunCommand(deps *Deps) *cobra.Command {
	var opts runOptions

	cmd := &cobra.Command{
		Use:   "run [target-id]",
		Short: "Run a task, capture, or drive the DAG loop toward a goal",
		Long: "Run one task end-to-end, or with --goal drive the graph toward a goal. In single\n" +
			"task mode `ft run <task-id>` resolves the task's workspace, prepares the selected\n" +
			"isolation backend, runs the selected harness, captures its output, and reflects\n" +
			"progress back into the store. A positional id may instead be an idea or bug\n" +
			"capture: `ft run <capture-id>` runs every task that belongs to it, in DAG order\n" +
			"(the same loop as --goal). With --goal the launcher repeatedly selects the\n" +
			"highest-ranked agent-ready (groomed) task on the path to the goal - a task, a\n" +
			"milestone, or a capture - runs it, and re-reads the graph until the goal is\n" +
			"reached, no work is ready, on-path work is blocked, or the --max-tasks budget\n" +
			"is exhausted. With a prompt source and no task id, `ft run` runs the prompt\n" +
			"once over every repository of the selected project, writing nothing to the\n" +
			"task graph. -p/--project selects the project whose store the run reads and\n" +
			"writes, defaulting to the configured project; the target must belong to it,\n" +
			"and the launched session inherits that store. The sandbox and harness\n" +
			"resolve from --sandbox and --harness, then FACTOTUM_RUN_*, then the [run]\n" +
			"config table (project over user); with stdin and stdout both on a terminal\n" +
			"an unset one prompts once and is saved, while a non-interactive unset one\n" +
			"is an error. Whenever the resolved sandbox is local and run.allow_host is\n" +
			"unset, an interactive run asks to opt in (default no) and records\n" +
			"run.allow_host in the user config, however local was chosen; the local\n" +
			"backend is unsandboxed and otherwise requires --allow-host (or\n" +
			"run.allow_host).\n" +
			"On a terminal, a single-task run or a task-less prompt run attaches the agent\n" +
			"to the terminal (for example the OpenCode TUI) so a human can answer it, and\n" +
			"its output stays live and is not captured, so a task run's note records the\n" +
			"outcome only; --unattended, or a pipe or redirect, keeps the run headless.\n" +
			"The --goal loop always runs headless.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.goal != "" && len(args) > 0 {
				return usageError(cmd, "give either a task id or --goal, not both")
			}
			if opts.goal == "" && len(args) == 0 && opts.promptFile == "" && opts.promptArtifact == "" {
				return usageError(cmd, "a task id, --goal, or a prompt source (--prompt-file/--prompt-artifact) is required")
			}
			if opts.maxTasks < 0 {
				return usageError(cmd, "--max-tasks must not be negative")
			}
			if err := validateRunPrompt(cmd, opts); err != nil {
				return err
			}
			return withRunInterrupts(cmd, func() error {
				if opts.goal != "" {
					return deps.runGoal(cmd, opts.goal, opts)
				}
				if len(args) == 0 {
					return deps.runProject(cmd, opts)
				}
				return deps.runTask(cmd, args[0], opts)
			})
		},
	}
	cmd.Flags().StringVarP(&opts.project, "project", "p", "", "project id whose store the run reads and writes (defaults to the configured project)")
	cmd.Flags().StringVar(&opts.sandbox, "sandbox", "", "isolation backend name (defaults to run.sandbox; e.g. local, openshell, docker)")
	cmd.Flags().StringVar(&opts.harness, "harness", "", "harness name (defaults to run.harness; e.g. opencode, opencode2, pi)")
	cmd.Flags().StringVar(&opts.workspace, "workspace", "", "workspace root directory (default under the config dir)")
	cmd.Flags().StringVar(&opts.model, "model", "", "model override passed to the harness")
	cmd.Flags().StringVar(&opts.image, "image", "", "base image an isolating backend runs the harness from (required for pi; defaults to the harness's own image)")
	cmd.Flags().StringVar(&opts.user, "user", "", "container identity (uid, uid:gid, or name) an isolating backend runs as (defaults to run.user, else the image's USER; local ignores it)")
	cmd.Flags().StringArrayVar(&opts.args, "arg", nil, "extra argument passed to the harness (repeatable)")
	cmd.Flags().BoolVar(&opts.allowHost, "allow-host", false, "opt in to the unsandboxed local backend (dev-only)")
	cmd.Flags().BoolVar(&opts.refresh, "refresh", false, "fetch and reset a reused workspace checkout to its upstream before running")
	cmd.Flags().StringVar(&opts.goal, "goal", "", "drive the DAG loop toward this goal task, milestone, or capture (idea/bug)")
	cmd.Flags().BoolVar(&opts.unattended, "unattended", false, "run headless on a terminal (no attached agent); the agent runs attached to the terminal by default")
	cmd.Flags().IntVar(&opts.maxTasks, "max-tasks", 0, "stop the loop after this many task runs (0 = no budget)")
	cmd.Flags().StringVar(&opts.promptFile, "prompt-file", "", "use this file's content as the harness prompt (single task or task-less run)")
	cmd.Flags().StringVar(&opts.promptArtifact, "prompt-artifact", "", "use this artifact's body as the harness prompt (single task or task-less run)")
	deps.validateBeforeStore(cmd, func(cmd *cobra.Command) error {
		return deps.validateRunFlags(cmd, opts)
	})
	return cmd
}

// withRunInterrupts runs one `ft run` dispatch, turning INT and TERM into
// cancellation of the run's context instead of ending the process on the spot.
// Cancellation unwinds the run through its ordinary error path, which is what
// retires a session the run minted and tears down the sandbox it prepared: an
// unhandled signal skips every defer and leaves that session alive forever. A
// second signal is left to the default disposition, so a harness that ignores
// cancellation can never leave ft uninterruptible. That escape is deliberately
// not graceful: it ends the process on the spot, so the teardown above is
// skipped and whatever it would have removed is left behind - a container or
// sandbox keeps running and a minted session stays live, with nothing that
// reaps either until the operator cleans them up. One Ctrl-C is the graceful
// stop; the second is the way out.
func withRunInterrupts(cmd *cobra.Command, run func() error) error {
	ctx, cancel := context.WithCancelCause(cmd.Context())
	defer cancel(nil)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		select {
		case <-signals:
			signal.Stop(signals)
			cancel(errRunInterrupted)
		case <-ctx.Done():
		}
	}()

	cmd.SetContext(ctx)
	err := run()
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return err
}

// validateRunFlags rejects a name given on the command line that the registries
// do not have. It is registered with validateBeforeStore, so the check runs
// after cobra parses flags and before the root opens the caller's store: a name
// that can never run must not create, migrate, or lock a database. An unset name
// comes from config and is resolved later, once the selection is built.
func (d *Deps) validateRunFlags(cmd *cobra.Command, opts runOptions) error {
	if opts.sandbox != "" {
		if _, err := d.RunBackends.MustLookup(opts.sandbox); err != nil {
			return usageError(cmd, "unknown isolation backend %q; available: %s", opts.sandbox, strings.Join(d.RunBackends.Names(), ", "))
		}
	}
	if opts.harness != "" {
		if _, err := d.RunHarnesses.MustLookup(opts.harness); err != nil {
			return usageError(cmd, "unknown harness %q; available: %s", opts.harness, strings.Join(d.RunHarnesses.Names(), ", "))
		}
	}
	return nil
}

// storeEnvFor is the store configuration a run hands its launched session, so
// the session's child ft reads the same store instead of resolving one -
// possibly the real user database - from the checkout's project or user config.
// It names the store the run reads and writes: the configured store, or the
// selected project's store for a cross-project `-p`. It formats the options as
// config.Load parses them. A file-naming option is made absolute because the
// session runs with a different working directory than ft, so a relative value
// would name a different file (or none) inside the checkout.
func (d *Deps) storeEnvFor(store config.Store) map[string]string {
	if store.Backend == "" {
		return nil
	}
	env := map[string]string{harnesspkg.EnvStore: store.Backend}
	options := make(map[string]string, len(store.Options))
	for key, value := range store.Options {
		options[key] = value
	}
	for _, key := range []string{"path", "migrate_from"} {
		value := options[key]
		if value == "" || value == ":memory:" || filepath.IsAbs(value) {
			continue
		}
		if abs, err := filepath.Abs(value); err == nil {
			options[key] = abs
		}
	}
	if formatted := config.FormatStoreOptions(options); formatted != "" {
		env[harnesspkg.EnvStoreOpts] = formatted
	}
	return env
}

// validateRunPrompt checks the prompt-source flags before the run resolves its
// project: the two sources are mutually exclusive, and a --goal loop derives its
// prompt per task so it takes neither.
func validateRunPrompt(cmd *cobra.Command, opts runOptions) error {
	if opts.promptFile != "" && opts.promptArtifact != "" {
		return usageError(cmd, "give either --prompt-file or --prompt-artifact, not both")
	}
	if opts.goal != "" && (opts.promptFile != "" || opts.promptArtifact != "") {
		return usageError(cmd, "--prompt-file and --prompt-artifact apply to a single task or task-less run, not --goal")
	}
	return nil
}

// resolveRunPrompt resolves the optional prompt override for a run from a file
// or an artifact body. An empty result means the run uses the task-derived
// prompt (a task-less run always has a source). The artifact resolves against
// the project the run selected, so a -p run uses the prompt stored in that
// project's store rather than the caller's.
func (d *Deps) resolveRunPrompt(cmd *cobra.Command, opts runOptions, artifacts *app.ArtifactService) (string, error) {
	switch {
	case opts.promptFile != "":
		data, err := os.ReadFile(opts.promptFile)
		if err != nil {
			return "", fmt.Errorf("read prompt file %s: %w", opts.promptFile, err)
		}
		if strings.TrimSpace(string(data)) == "" {
			return "", fmt.Errorf("prompt file %s is empty", opts.promptFile)
		}
		return string(data), nil
	case opts.promptArtifact != "":
		artifact, err := artifacts.Get(cmd.Context(), core.ArtifactID(opts.promptArtifact))
		if err != nil {
			return "", fmt.Errorf("resolve prompt artifact %s: %w", opts.promptArtifact, err)
		}
		if strings.TrimSpace(artifact.Body) == "" {
			return "", usageError(cmd, "prompt artifact %s has an empty body", opts.promptArtifact)
		}
		return artifact.Body, nil
	default:
		return "", nil
	}
}

// runSelection is the resolved backend, harness, and workspace a run uses.
type runSelection struct {
	backend     isolation.IsolationBackend
	backendName string
	harness     harnesspkg.Harness
	workspace   string
	repoBase    string
	model       string
	refresh     bool
	args        []string
}

// prepareRun merges the per-invocation overrides into the [run] config and
// builds the selected backend and harness. Selection is explicit: there is no
// default backend, and an unknown name is a usage error.
func (d *Deps) prepareRun(cmd *cobra.Command, opts runOptions) (*runSelection, error) {
	cfg := d.Config.Run
	if opts.sandbox != "" {
		cfg.Sandbox = opts.sandbox
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
	if opts.image != "" {
		cfg.Image = opts.image
	}
	if opts.user != "" {
		cfg.User = opts.user
	}
	if len(opts.args) > 0 {
		cfg.Args = append(append([]string(nil), cfg.Args...), opts.args...)
	}
	if opts.allowHost {
		cfg.AllowHost = true
	}
	if opts.refresh {
		cfg.Refresh = true
	}
	cfg.PolicyPath = projectPolicyPath(d.ProjectConfigPath)

	if err := d.resolveRunSelection(cmd, &cfg); err != nil {
		return nil, err
	}
	workspaceRoot, err := runWorkspaceRoot(cfg.Workspace)
	if err != nil {
		return nil, usageError(cmd, "%v", err)
	}

	backendFactory, err := d.RunBackends.MustLookup(cfg.Sandbox)
	if err != nil {
		return nil, usageError(cmd, "unknown isolation backend %q; available: %s", cfg.Sandbox, strings.Join(d.RunBackends.Names(), ", "))
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
		repoBase:    config.ProjectRoot(d.ProjectConfigPath),
		model:       cfg.Model,
		refresh:     cfg.Refresh,
		args:        cfg.Args,
	}, nil
}

// resolveRunSelection fills an unset sandbox or harness. Config (env > project
// > user) and flags have already been merged into cfg. An interactive session
// (a terminal on both stdin and stdout) prompts once from the available
// options, persists the pick to the project or user config the user selects,
// and uses it so the next run needs no flag. A non-interactive one with a
// missing selection is a usage error naming the available options.
func (d *Deps) resolveRunSelection(cmd *cobra.Command, cfg *config.Run) error {
	// The local backend's host opt-in belongs to the resolved sandbox, not to
	// choosing it here: local pinned by config or a flag is just as unsandboxed
	// as local picked at the prompt.
	needsOptIn := cfg.Sandbox == local.Name && !cfg.AllowHost
	if cfg.Sandbox != "" && cfg.Harness != "" && !needsOptIn {
		return nil
	}
	if !d.promptsAllowed(cmd) {
		if cfg.Sandbox == "" {
			return usageError(cmd, "--sandbox is required; set run.sandbox (project or user config), FACTOTUM_RUN_SANDBOX, or run on a terminal to choose; available: %s", strings.Join(d.RunBackends.Names(), ", "))
		}
		if cfg.Harness == "" {
			return usageError(cmd, "--harness is required; set run.harness (project or user config), FACTOTUM_RUN_HARNESS, or run on a terminal to choose; available: %s", strings.Join(d.RunHarnesses.Names(), ", "))
		}
		// Both are pinned: the missing opt-in surfaces at selection with its
		// own hint, so a non-interactive run never blocks on a prompt.
		return nil
	}

	p := d.prompter(cmd)
	var chosen config.RunDefaults
	if cfg.Sandbox == "" {
		name, err := promptRunChoice(p, "sandbox", d.RunBackends.Names())
		if err != nil {
			return err
		}
		cfg.Sandbox = name
		chosen.Sandbox = name
	}
	if cfg.Sandbox == local.Name && !cfg.AllowHost {
		optIn, err := promptLocalOptIn(p)
		if err != nil {
			return err
		}
		if !optIn {
			return usageError(cmd, "local backend resolved but host opt-in declined; nothing saved. Pass --allow-host for this run, or rerun to choose another sandbox")
		}
		chosen.AllowHost = true
		cfg.AllowHost = true
	}
	if cfg.Harness == "" {
		name, err := promptRunChoice(p, "harness", d.RunHarnesses.Names())
		if err != nil {
			return err
		}
		chosen.Harness = name
	}
	if chosen.AllowHost && chosen.Sandbox == "" && chosen.Harness == "" {
		// Only the host-scoped opt-in was granted, and that always belongs in
		// the machine config: there is no scope to ask about.
		path, err := d.userConfigPath()
		if err != nil {
			return err
		}
		if err := config.WriteRunDefaults(path, false, "", config.RunDefaults{AllowHost: true}); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(d.Err, "ft: recorded run.allow_host opt-in in %s\n", path)
		return nil
	}
	scope, err := d.promptRunScope(p)
	if err != nil {
		return err
	}
	path, optInPath, err := d.persistRunDefaults(scope, chosen)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(d.Err, "ft: saved run defaults to %s\n", path)
	if optInPath != "" {
		_, _ = fmt.Fprintf(d.Err, "ft: recorded run.allow_host opt-in in %s\n", optInPath)
	}
	if chosen.Harness != "" {
		cfg.Harness = chosen.Harness
	}
	return nil
}

// promptLocalOptIn asks the explicit, default-no opt-in for the unsandboxed
// local backend. Whether local was chosen here or pinned in config, the host
// trust it grants is loud enough to confirm rather than assume.
func promptLocalOptIn(p Prompter) (bool, error) {
	return p.Confirm(
		"the local backend runs UNSANDBOXED on the host (host credentials, network, files); allow it?",
		false,
	)
}

// promptRunChoice asks for one run dimension from its available options,
// defaulting to the first. An answer outside the set is rejected, so a typo
// never selects an unknown adapter.
func promptRunChoice(p Prompter, dimension string, options []string) (string, error) {
	if len(options) == 0 {
		return "", fmt.Errorf("no %s is available to choose; register one first", dimension)
	}
	label := fmt.Sprintf("%s (%s)", dimension, strings.Join(options, ", "))
	value, err := p.Input(label, options[0])
	if err != nil {
		return "", err
	}
	for _, option := range options {
		if value == option {
			return value, nil
		}
	}
	return "", fmt.Errorf("unknown %s %q; available: %s", dimension, value, strings.Join(options, ", "))
}

// promptRunScope asks which config scope persists the chosen defaults. A
// project-scope write must pin the project it configures, so with no configured
// project that scope is not offered at all: the pick falls to the machine
// config, which always succeeds, and the run says why.
func (d *Deps) promptRunScope(p Prompter) (string, error) {
	if strings.TrimSpace(d.Config.Project) == "" {
		_, _ = fmt.Fprintln(d.Err, "ft: project scope needs a configured project; saving run defaults to the machine config")
		return "user", nil
	}
	scope, err := p.Input("save run defaults to (project/user)", "user")
	if err != nil {
		return "", err
	}
	switch scope {
	case "project", "user":
		return scope, nil
	default:
		return "", fmt.Errorf("unknown scope %q; want project or user", scope)
	}
}

// persistRunDefaults writes chosen into the selected scope's config and, for
// the host-scoped allow_host opt-in, into the user config, returning the path
// written and (when the opt-in was split out) the user config path. A committed
// project file must never carry allow_host, so a project-scoped pick writes the
// opt-in to the user config separately. The machine path is resolved before the
// first write: a resolution failure must not leave the project file written
// without the opt-in.
func (d *Deps) persistRunDefaults(scope string, chosen config.RunDefaults) (path, optInPath string, err error) {
	if scope == "project" {
		path := d.ProjectConfigPath
		if path == "" {
			path = config.DefaultPath
		}
		var userPath string
		if chosen.AllowHost {
			userPath, err = d.userConfigPath()
			if err != nil {
				return "", "", err
			}
		}
		committable := config.RunDefaults{Sandbox: chosen.Sandbox, Harness: chosen.Harness}
		if err := config.WriteRunDefaults(path, true, d.Config.Project, committable); err != nil {
			return "", "", err
		}
		if !chosen.AllowHost {
			return path, "", nil
		}
		if err := config.WriteRunDefaults(userPath, false, "", config.RunDefaults{AllowHost: true}); err != nil {
			return "", "", err
		}
		return path, userPath, nil
	}
	path, err = d.userConfigPath()
	if err != nil {
		return "", "", err
	}
	if err := config.WriteRunDefaults(path, false, "", chosen); err != nil {
		return "", "", err
	}
	return path, "", nil
}

// userConfigPath resolves the machine-scoped config path to write: the loaded
// path, else $HOME/.factotum/config.toml.
func (d *Deps) userConfigPath() (string, error) {
	path := d.UserConfigPath
	if path == "" {
		path = config.UserPath(d.Getenv)
	}
	if path == "" {
		return "", errors.New("cannot locate the machine config; set $HOME or pass --user-config")
	}
	return path, nil
}

// promptsAllowed reports whether a command may prompt the user: an injected
// prompter (tests), or a terminal on both stdout and stdin. stdout alone is not
// enough: with stdin redirected the prompts read EOF, accept every default, and
// persist a config nobody chose, so such a session is headless instead. `ft run`
// selects its sandbox and harness by the same rule, and `ft init` by this one.
func (d *Deps) promptsAllowed(cmd *cobra.Command) bool {
	if d.Prompt != nil {
		return true
	}
	if d.IsTerminal == nil || !d.IsTerminal(d.Out) {
		return false
	}
	return d.stdinTerminal(cmd.InOrStdin())
}

// runInteractive reports whether a run should attach the agent to the terminal:
// an interactive session (both stdin and stdout a terminal) that did not opt
// into unattended mode. Every other combination runs headless, so a pipe, a
// redirect, or an explicit --unattended is unchanged.
func (d *Deps) runInteractive(cmd *cobra.Command, unattended bool) bool {
	if unattended || d.IsTerminal == nil || !d.IsTerminal(d.Out) {
		return false
	}
	return d.stdinTerminal(cmd.InOrStdin())
}

// stdinTerminal reports whether a reader is a terminal, honoring the injected
// check in tests.
func (d *Deps) stdinTerminal(r io.Reader) bool {
	if d.IsTerminalReader != nil {
		return d.IsTerminalReader(r)
	}
	return isTerminalReader(r)
}

// interactiveUnsupported reports whether an interactive run failed because its
// backend cannot attach a terminal, naming the way out. It matches the
// dedicated sentinel, so an unrelated unsupported request is reported as-is.
func (d *Deps) interactiveUnsupported(cmd *cobra.Command, interactive bool, backendName string, runErr error) error {
	if !interactive || !errors.Is(runErr, isolation.ErrNoTerminal) {
		return nil
	}
	return usageError(cmd, "backend %q cannot attach an interactive terminal; rerun with --unattended for a headless run, or use --sandbox local --allow-host", backendName)
}

// warnIncompleteHub warns when the [serve] table names a hub URL but no token.
// The hub's message transport is always token-gated, so a launched receiver
// would speak it unauthenticated and fail-close every message. The incomplete
// hub is dropped from the run env, so the warning explains the fallback to the
// local ft store.
func (d *Deps) warnIncompleteHub() {
	if d.Config.Serve.URL != "" && strings.TrimSpace(d.Config.Serve.Token) == "" {
		d.warnf("serve.url is set but serve.token is empty; ignoring the hub because a receiver cannot authenticate to it (set serve.token or FACTOTUM_SERVE_TOKEN, or unset serve.url)")
	}
}

// warnIncompleteHubForRun warns about a half-configured hub only when the run
// will launch a receiver, which requires an actor. A run with no actor installs
// no receiver, so an incomplete hub cannot affect it and the warning would name
// a receiver that does not exist.
func (d *Deps) warnIncompleteHubForRun(actor *core.ActorID) {
	if actor == nil || *actor == "" {
		return
	}
	d.warnIncompleteHub()
}

// warnLocalPlan warns when a resolved workspace uses a repository's local
// checkout in place. It reads the resolved plan rather than the configured
// repos, so it fires exactly when workspace.Resolve produced an OriginLocal
// checkout and names the absolute path the run operates in — a relative
// configured Path resolves against the project root, so the raw config would
// print an unresolved path. A repo with a URL is cloned into the workspace
// (OriginClone) and not warned about.
func (d *Deps) warnLocalPlan(plan *workspace.Plan) {
	for _, checkout := range plan.Checkouts {
		if checkout.Origin != workspace.OriginLocal {
			continue
		}
		d.warnf("repository %q uses the local checkout %s in place; the run may modify the source checkout (register a URL to run in an isolated workspace copy)", checkout.Name, checkout.Path)
	}
}

// runActor resolves the identity the run's message receiver registers as, or
// says this run will launch without one. A surviving reference only needs the
// identity rule: a loop role is a mailbox, not a record, and still resolves. A
// reference bindCaller dropped is not revisited - one reference cannot be
// unusable for attribution and usable as a mailbox - and its attribution
// warning does not say the session will be unreachable, so the run says that.
// Either way the run keeps launching without a receiver: the work is still
// worth doing and the actor can be registered while it runs. The actor resolves
// against the selected project's store, because that is the store the launched
// receiver registers in.
func (d *Deps) runActor(ctx context.Context, actors *app.ActorService) *core.ActorID {
	if d.ActorRef == "" {
		if d.ActorDropped != "" {
			d.warnNoReceiver(d.ActorDropped)
		}
		return nil
	}
	actor, err := actors.Identity(ctx, core.ActorID(d.ActorRef))
	if err != nil {
		if !errors.Is(err, core.ErrNotFound) {
			d.warnf("could not resolve actor %q (%v)", d.ActorRef, err)
		}
		d.warnNoReceiver(d.ActorRef)
		return nil
	}
	return &actor.ID
}

// runTargetCheck reports whether the resolved run target belongs to the project
// the run selected. A target absent from the selected project's store, or one
// that lives there under another project, is a not-found error naming both, so
// -p is authoritative rather than advisory. An unset project (no flag and none
// configured) changes nothing, so a run against a bare store keeps working.
func runTargetCheck(id core.TicketID, projectID core.ProjectID, task *core.Ticket, err error) error {
	if err != nil {
		if errors.Is(err, core.ErrNotFound) && projectID != "" {
			return fmt.Errorf("%w: task %s in project %s", core.ErrNotFound, id, projectID)
		}
		return err
	}
	if projectID != "" && task.ProjectID != projectID {
		return fmt.Errorf("%w: task %s is not in project %s (it belongs to %s)", core.ErrNotFound, id, projectID, task.ProjectID)
	}
	return nil
}

// warnNoReceiver names the receiver consequence and its fix for a run whose
// named actor resolves to no mailbox, so a launched session nobody can reach is
// never silent. The fix is path-specific: the --actor path accepts a loop role,
// which needs no record, but a caller is only ever resolved as a record, so
// offering it there would name a fix the caller path cannot take.
func (d *Deps) warnNoReceiver(ref string) {
	if d.Caller != "" {
		d.warnf("no message receiver will register for actor %q; the run launches unreachable over ft msg (register the caller with ft actor create %q)", ref, ref)
		return
	}
	d.warnf("no message receiver will register for actor %q; the run launches unreachable over ft msg (create the actor with ft actor create %q, or pass a loop role)", ref, ref)
}

func (d *Deps) runTask(cmd *cobra.Command, taskID string, opts runOptions) error {
	projectID := d.resolveProject(opts.project)
	target, err := d.openProjectTarget(cmd.Context(), projectID)
	if err != nil {
		return err
	}
	defer target.close()

	prompt, err := d.resolveRunPrompt(cmd, opts, target.artifacts)
	if err != nil {
		return err
	}

	sel, err := d.prepareRun(cmd, opts)
	if err != nil {
		return err
	}

	task, err := target.tasks.Get(cmd.Context(), core.TicketID(taskID))
	if err := runTargetCheck(core.TicketID(taskID), projectID, task, err); err != nil {
		return err
	}
	// A capture (idea or bug) is not a task to run once: target it as a loop
	// over every task that belongs to it.
	if task.Kind.CapturedByHuman() {
		if prompt != "" {
			return usageError(cmd, "--prompt-file and --prompt-artifact apply to a single task or task-less run, not a capture (%s %s)", task.Kind, task.ID)
		}
		return d.driveLoop(cmd, target, taskID, opts, sel)
	}

	actor := d.runActor(cmd.Context(), target.actors)
	d.warnIncompleteHubForRun(actor)
	interactive := d.runInteractive(cmd, opts.unattended)

	progress := d.startRunProgress(runProgressLabel("run "+taskID, sel.backendName, sel.harness.Name()), interactive)
	outcome, runErr := app.NewRunService(target.backend, target.tasks, d.Clock, d.IDs).Run(cmd.Context(), app.RunInput{
		TicketID:         task.ID,
		Backend:          sel.backend,
		Harness:          sel.harness,
		WorkspaceRoot:    sel.workspace,
		RepoBase:         sel.repoBase,
		WorkspaceRefresh: sel.refresh,
		Model:            sel.model,
		Args:             sel.args,
		Prompt:           prompt,
		Actor:            actor,
		MsgURL:           d.Config.Serve.URL,
		ServeToken:       d.Config.Serve.Token,
		StoreEnv:         d.storeEnvFor(target.store),
		Interactive:      interactive,
		OnResolve:        d.warnLocalPlan,
	})
	progress.stop()
	if errors.Is(runErr, local.ErrNotOptedIn) {
		return usageError(cmd, "backend %q runs unsandboxed and is not opted in; pass --allow-host (or set run.allow_host) only for trusted work", sel.backendName)
	}
	if err := d.interactiveUnsupported(cmd, interactive, sel.backendName, runErr); err != nil {
		return err
	}
	if outcome == nil {
		return runErr
	}
	if err := d.printRunOutcome(task, outcome, runErr); err != nil {
		return err
	}
	return runErr
}

// runProject runs one stored prompt over all of the selected project's repos
// with no task: a task-less prompt run (a grooming session, for example). It
// materializes every project repo and writes nothing to the task graph.
func (d *Deps) runProject(cmd *cobra.Command, opts runOptions) error {
	projectID := d.resolveProject(opts.project)
	if err := requireProject(cmd, projectID); err != nil {
		return err
	}
	target, err := d.openProjectTarget(cmd.Context(), projectID)
	if err != nil {
		return err
	}
	defer target.close()

	prompt, err := d.resolveRunPrompt(cmd, opts, target.artifacts)
	if err != nil {
		return err
	}

	actor := d.runActor(cmd.Context(), target.actors)
	d.warnIncompleteHubForRun(actor)
	sel, err := d.prepareRun(cmd, opts)
	if err != nil {
		return err
	}
	interactive := d.runInteractive(cmd, opts.unattended)
	project, err := target.projects.Get(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	progress := d.startRunProgress(runProgressLabel("run project "+string(project.ID), sel.backendName, sel.harness.Name()), interactive)
	outcome, runErr := app.NewRunService(target.backend, target.tasks, d.Clock, d.IDs).RunProject(cmd.Context(), app.ProjectRunInput{
		ProjectID:        project.ID,
		Backend:          sel.backend,
		Harness:          sel.harness,
		WorkspaceRoot:    sel.workspace,
		RepoBase:         sel.repoBase,
		WorkspaceRefresh: sel.refresh,
		Model:            sel.model,
		Args:             sel.args,
		Prompt:           prompt,
		Actor:            actor,
		MsgURL:           d.Config.Serve.URL,
		ServeToken:       d.Config.Serve.Token,
		StoreEnv:         d.storeEnvFor(target.store),
		Interactive:      interactive,
		OnResolve:        d.warnLocalPlan,
	})
	progress.stop()
	if errors.Is(runErr, local.ErrNotOptedIn) {
		return usageError(cmd, "backend %q runs unsandboxed and is not opted in; pass --allow-host (or set run.allow_host) only for trusted work", sel.backendName)
	}
	if err := d.interactiveUnsupported(cmd, interactive, sel.backendName, runErr); err != nil {
		return err
	}
	if outcome == nil {
		return runErr
	}
	if err := d.printProjectRunOutcome(project, outcome, runErr); err != nil {
		return err
	}
	return runErr
}

// runGoal drives the DAG loop toward a goal: a task, a milestone, or a capture
// (idea/bug), whose tasks the loop runs in DAG order.
func (d *Deps) runGoal(cmd *cobra.Command, goalID string, opts runOptions) error {
	projectID := d.resolveProject(opts.project)
	target, err := d.openProjectTarget(cmd.Context(), projectID)
	if err != nil {
		return err
	}
	defer target.close()

	sel, err := d.prepareRun(cmd, opts)
	if err != nil {
		return err
	}
	return d.driveLoop(cmd, target, goalID, opts, sel)
}

// driveLoop runs the loop target - a task, milestone, or capture - through the
// single-task service, reusing an already-resolved run selection. It resolves
// the receiver actor on the first iteration, not up front: a goal that selects
// no work launches no session and installs no receiver, so warning about one
// would be a false alarm.
func (d *Deps) driveLoop(cmd *cobra.Command, target projectTarget, targetID string, opts runOptions, sel *runSelection) error {
	goal, err := target.tasks.Get(cmd.Context(), core.TicketID(targetID))
	if err := runTargetCheck(core.TicketID(targetID), d.resolveProject(opts.project), goal, err); err != nil {
		return err
	}
	ranker, err := d.Rankers.MustLookup("composite")
	if err != nil {
		return err
	}

	svc := app.NewRunService(target.backend, target.tasks, d.Clock, d.IDs)
	// One session for the whole loop: the caller's when this invocation inherited
	// one, else a fresh id, so every task the loop runs lands on one timeline
	// instead of each run minting its own. A minted session is the loop's to
	// close: it is started when the first task is actually about to run and
	// retired on the way out, so a loop refused in preflight or selecting no work
	// leaves no empty row - and, never having started one, reports no session
	// either. An inherited session belongs to the caller and is never started or
	// retired here.
	session := d.Session
	minted := session == ""
	started := false
	var startSession func(context.Context) error
	if minted {
		session = d.Sessions.NewID()
		startSession = func(ctx context.Context) error {
			if started {
				return nil
			}
			if _, err := d.Sessions.Start(ctx, goal.ProjectID, session); err != nil {
				return err
			}
			started = true
			return nil
		}
		defer func() {
			if !started {
				return
			}
			// The loop is over whether or not it finished cleanly. `ft run`
			// turns an operator's INT/TERM into cancellation of this context, so
			// an interrupted loop unwinds through here too; the context is
			// already cancelled by then, which is why the retire runs on one that
			// ignores cancellation. Losing the event must not mask the loop's own
			// result.
			_, _ = d.Sessions.Retire(context.WithoutCancel(cmd.Context()), goal.ProjectID, session, "")
		}()
	}
	// The actor (and the receiver it installs) is resolved when the loop first
	// launches a task, so a run that selects no work never warns about a
	// receiver it never starts.
	var actor *core.ActorID
	actorResolved := false
	runner := func(ctx context.Context, taskID core.TicketID) (*app.RunOutcome, error) {
		if !actorResolved {
			actor = d.runActor(ctx, target.actors)
			d.warnIncompleteHubForRun(actor)
			actorResolved = true
		}
		progress := d.startRunProgress(runProgressLabel("run "+string(taskID), sel.backendName, sel.harness.Name()), false)
		defer progress.stop()
		return svc.Run(ctx, app.RunInput{
			TicketID:         taskID,
			Backend:          sel.backend,
			Harness:          sel.harness,
			WorkspaceRoot:    sel.workspace,
			RepoBase:         sel.repoBase,
			WorkspaceRefresh: sel.refresh,
			Model:            sel.model,
			Args:             sel.args,
			Actor:            actor,
			MsgURL:           d.Config.Serve.URL,
			ServeToken:       d.Config.Serve.Token,
			StoreEnv:         d.storeEnvFor(target.store),
			Session:          session,
			OnPrepared:       startSession,
			OnResolve:        d.warnLocalPlan,
		})
	}

	text := !d.structured()
	outcome, runErr := app.NewRunLoopService(target.backend, ranker, d.Clock).Run(cmd.Context(), app.LoopInput{
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
	// A loop that selected no work minted a session id but never recorded it, so
	// report no session rather than an id that resolves to nothing. A caller's
	// session exists independently of this loop, so an inherited id is always
	// reported.
	reported := session
	if minted && !started {
		reported = ""
	}
	if err := d.printLoopOutcome(goal, outcome, reported, runErr); err != nil {
		return err
	}
	return runErr
}

// projectPolicyPath resolves the project's committed OpenShell policy override
// from the loaded project config path. The override is the config file's
// sibling, so a custom -c/--config resolves the policy next to the config it
// loaded. It is empty when no config path is known, which means no opt-in.
func projectPolicyPath(projectConfigPath string) string {
	if projectConfigPath == "" {
		return ""
	}
	return openshell.OverridePathForConfig(projectConfigPath)
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
	TicketID string `json:"task_id" yaml:"task_id"`
	Run      string `json:"run" yaml:"run"`
	Status   string `json:"status" yaml:"status"`
	ExitCode int    `json:"exit_code" yaml:"exit_code"`
	Complete bool   `json:"complete" yaml:"complete"`
	Output   string `json:"output,omitempty" yaml:"output,omitempty"`
	Stderr   string `json:"stderr,omitempty" yaml:"stderr,omitempty"`
	Session  string `json:"session" yaml:"session"`
	Project  string `json:"project" yaml:"project"`
	Repo     string `json:"repo" yaml:"repo"`
}

func (d *Deps) printRunOutcome(task *core.Ticket, outcome *app.RunOutcome, runErr error) error {
	state := "finished"
	if runErr != nil {
		state = "failed"
	}
	doc := runDoc{
		TicketID: string(task.ID),
		Run:      state,
		Status:   string(outcome.Status),
		ExitCode: outcome.ExitCode,
		Complete: outcome.Complete,
		Output:   outcome.Output,
		Stderr:   outcome.Stderr,
		Session:  outcome.Session,
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
			f("session", outcome.Session),
			f("project", task.ProjectID),
			f("repo", d.repoValue(task.Repo)),
		)
		if output := strings.TrimSpace(outcome.Output); output != "" {
			d.printf("\n%s\n", output)
		}
		if stderr := strings.TrimSpace(outcome.Stderr); stderr != "" {
			d.printf("\nstderr:\n%s\n", stderr)
		}
	}, d.runHints(task, runErr)...)
}

func (d *Deps) runHints(task *core.Ticket, runErr error) []hint {
	id := string(task.ID)
	if runErr != nil {
		return []hint{{Command: fmt.Sprintf("ft task get %s", id), About: "inspect the failed run"}}
	}
	return []hint{
		{Command: fmt.Sprintf("ft task get %s", id), About: "inspect the task"},
		{Command: fmt.Sprintf("ft task note create %s --body \"PR: <url>\" --link pr=<url>", id), About: "attach the PR link"},
	}
}

// runProjectDoc is the lossless structured shape of a task-less `ft run`.
type runProjectDoc struct {
	Run      string   `json:"run" yaml:"run"`
	ExitCode int      `json:"exit_code" yaml:"exit_code"`
	Complete bool     `json:"complete" yaml:"complete"`
	Repos    []string `json:"repos" yaml:"repos"`
	Output   string   `json:"output,omitempty" yaml:"output,omitempty"`
	Stderr   string   `json:"stderr,omitempty" yaml:"stderr,omitempty"`
	Session  string   `json:"session" yaml:"session"`
	Project  string   `json:"project" yaml:"project"`
	Repo     string   `json:"repo" yaml:"repo"`
}

func (d *Deps) printProjectRunOutcome(project *core.Project, outcome *app.ProjectRunOutcome, runErr error) error {
	state := "finished"
	if runErr != nil {
		state = "failed"
	}
	repos := make([]string, 0, len(outcome.Checkouts))
	for _, checkout := range outcome.Checkouts {
		repos = append(repos, checkout.Name)
	}
	doc := runProjectDoc{
		Run:      state,
		ExitCode: outcome.ExitCode,
		Complete: outcome.Complete,
		Repos:    repos,
		Output:   outcome.Output,
		Stderr:   outcome.Stderr,
		Session:  outcome.Session,
		Project:  string(project.ID),
		Repo:     "",
	}
	return d.emit(doc, func() {
		d.printFields(
			f("run", state),
			f("exit_code", outcome.ExitCode),
			f("complete", outcome.Complete),
			f("repos", strings.Join(repos, ", ")),
			f("session", outcome.Session),
			f("project", project.ID),
			f("repo", d.repoValue("")),
		)
		if output := strings.TrimSpace(outcome.Output); output != "" {
			d.printf("\n%s\n", output)
		}
		if stderr := strings.TrimSpace(outcome.Stderr); stderr != "" {
			d.printf("\nstderr:\n%s\n", stderr)
		}
	}, d.runProjectHints(project, runErr)...)
}

func (d *Deps) runProjectHints(project *core.Project, runErr error) []hint {
	id := string(project.ID)
	if runErr != nil {
		return []hint{{Command: fmt.Sprintf("ft project get %s", id), About: "inspect the failed project run"}}
	}
	return []hint{{Command: fmt.Sprintf("ft task list --project %s", id), About: "see the project's tasks"}}
}

// runLoopStepDoc is one iteration of `ft run --goal` in structured output.
type runLoopStepDoc struct {
	Iteration int    `json:"iteration" yaml:"iteration"`
	TicketID  string `json:"task_id" yaml:"task_id"`
	Title     string `json:"title" yaml:"title"`
	Run       string `json:"run" yaml:"run"`
	Status    string `json:"status" yaml:"status"`
	ExitCode  int    `json:"exit_code" yaml:"exit_code"`
	Complete  bool   `json:"complete" yaml:"complete"`
	Remaining int    `json:"remaining" yaml:"remaining"`
	Output    string `json:"output,omitempty" yaml:"output,omitempty"`
	Stderr    string `json:"stderr,omitempty" yaml:"stderr,omitempty"`
}

// runLoopDoc is the lossless structured shape of `ft run --goal`.
type runLoopDoc struct {
	Goal       string           `json:"goal" yaml:"goal"`
	GoalKind   string           `json:"goal_kind" yaml:"goal_kind"`
	GoalStatus string           `json:"goal_status" yaml:"goal_status"`
	Stop       string           `json:"stop" yaml:"stop"`
	Iterations int              `json:"iterations" yaml:"iterations"`
	Remaining  int              `json:"remaining" yaml:"remaining"`
	NotRun     []string         `json:"not_run,omitempty" yaml:"not_run,omitempty"`
	Steps      []runLoopStepDoc `json:"steps" yaml:"steps"`
	Session    string           `json:"session,omitempty" yaml:"session,omitempty"`
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
		f("task_id", step.TicketID),
		f("title", step.Title),
		f("run", state),
		f("status", step.Status),
		f("exit_code", step.ExitCode),
		f("remaining", step.Remaining),
	)
	if stderr := strings.TrimSpace(step.Stderr); stderr != "" {
		d.printf("\nstderr:\n%s\n", stderr)
	}
	d.printf("\n")
}

// printLoopOutcome reports the loop's result. session is the id the loop ran
// under, or empty when it recorded none - a zero-iteration loop whose id was
// freshly minted - so the reported output only ever names a session that
// exists.
func (d *Deps) printLoopOutcome(goal *core.Ticket, outcome *app.LoopOutcome, session string, runErr error) error {
	doc := runLoopDoc{
		Goal:       string(outcome.Goal),
		GoalKind:   string(outcome.GoalKind),
		GoalStatus: string(outcome.GoalStatus),
		Stop:       string(outcome.Stop),
		Iterations: len(outcome.Steps),
		Remaining:  outcome.Remaining,
		Session:    session,
		Project:    string(goal.ProjectID),
		Repo:       goal.Repo,
	}
	for _, id := range outcome.NotRun {
		doc.NotRun = append(doc.NotRun, string(id))
	}
	for _, step := range outcome.Steps {
		doc.Steps = append(doc.Steps, loopStepDoc(step))
	}
	return d.emit(doc, func() {
		fields := []field{
			f("goal", outcome.Goal),
			f("goal_kind", outcome.GoalKind),
			f("goal_status", outcome.GoalStatus),
			f("stop", outcome.Stop),
			f("iterations", len(outcome.Steps)),
			f("remaining", outcome.Remaining),
		}
		for _, id := range outcome.NotRun {
			fields = append(fields, f("not_run", id))
		}
		if session != "" {
			fields = append(fields, f("session", session))
		}
		fields = append(fields, f("project", goal.ProjectID), f("repo", d.repoValue(goal.Repo)))
		d.printFields(fields...)
	}, d.runLoopHints(goal, outcome, runErr)...)
}

func loopStepDoc(step app.LoopStep) runLoopStepDoc {
	state := "finished"
	if step.Failed {
		state = "failed"
	}
	return runLoopStepDoc{
		Iteration: step.Iteration,
		TicketID:  string(step.TicketID),
		Title:     step.Title,
		Run:       state,
		Status:    string(step.Status),
		ExitCode:  step.ExitCode,
		Complete:  step.Complete,
		Remaining: step.Remaining,
		Output:    step.Output,
		Stderr:    step.Stderr,
	}
}

func (d *Deps) runLoopHints(goal *core.Ticket, outcome *app.LoopOutcome, runErr error) []hint {
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
