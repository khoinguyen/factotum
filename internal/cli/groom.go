package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/internal/config"
	"github.com/khoinguyen/factotum/internal/groom"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/isolation/local"
	"github.com/khoinguyen/factotum/pkg/store"
)

// groomOptions is the per-invocation configuration of `ft groom`. The run
// overrides mirror `ft run`, because a session is a task-less run.
type groomOptions struct {
	project    string
	promptFile string
	unattended bool
	run        runOptions
}

func newGroomCommand(deps *Deps) *cobra.Command {
	var opts groomOptions

	cmd := &cobra.Command{
		Use:   "groom [item...]",
		Short: "Run a grooming session over a project's ideas and tasks",
		Long: "Run a task-less grooming session over every repository of the project. `ft groom`\n" +
			"resolves the scope - the named items, or by default every open idea plus every open\n" +
			"ungroomed task - injects a session kickoff naming the scope and the workspace-relative\n" +
			"paths of the report, deferred-questions, feature spec, plan, and tech-design files, runs the\n" +
			"durable grooming prompt through the run service, and records the five outputs as doc\n" +
			"artifacts. With --unattended the kickoff\n" +
			"tells the session there is no product owner: it defers every product question and still\n" +
			"finishes agent-ready, and the run fails if any scoped item is left neither agent-ready\n" +
			"nor deferred. On a terminal the session runs attached to it (the agent's TUI) so the\n" +
			"PO can answer the grill; --unattended, or a pipe or redirect, runs it headless.\n" +
			"The sandbox and harness resolve like `ft run`: --sandbox/--harness, then\n" +
			"FACTOTUM_RUN_*, then the [run] config table (project over user), prompting once on a\n" +
			"terminal when unset; choosing the local backend in that prompt asks to opt in\n" +
			"(default no) and records run.allow_host in the user config.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return deps.runGroom(cmd, args, opts)
		},
	}
	cmd.Flags().StringVarP(&opts.project, "project", "p", "", "project id (defaults to the configured project)")
	cmd.Flags().StringVar(&opts.promptFile, "prompt-file", "", "session prompt file (default "+groom.PromptPath+")")
	cmd.Flags().BoolVar(&opts.unattended, "unattended", false, "run with no product owner: defer product questions and still finish agent-ready")
	cmd.Flags().StringVar(&opts.run.sandbox, "sandbox", "", "isolation backend name (defaults to run.sandbox; e.g. local, openshell, docker)")
	cmd.Flags().StringVar(&opts.run.sandbox, "backend", "", "deprecated alias for --sandbox")
	_ = cmd.Flags().MarkHidden("backend")
	cmd.Flags().StringVar(&opts.run.harness, "harness", "", "harness name (defaults to run.harness; e.g. opencode)")
	cmd.Flags().StringVar(&opts.run.workspace, "workspace", "", "workspace root directory (default under the config dir)")
	cmd.Flags().StringVar(&opts.run.model, "model", "", "model override passed to the harness")
	cmd.Flags().StringArrayVar(&opts.run.args, "arg", nil, "extra argument passed to the harness (repeatable)")
	cmd.Flags().BoolVar(&opts.run.allowHost, "allow-host", false, "opt in to the unsandboxed local backend (dev-only)")
	cmd.Flags().BoolVar(&opts.run.refresh, "refresh", false, "fetch and reset a reused workspace checkout before running")
	cmd.AddCommand(newGroomListCommand(deps), newGroomShowCommand(deps), newGroomReviewCommand(deps))
	return cmd
}

// runGroom resolves the scope, runs the durable session prompt with the kickoff
// appended, and captures the five outputs.
func (d *Deps) runGroom(cmd *cobra.Command, args []string, opts groomOptions) error {
	ctx := cmd.Context()
	projectID := d.resolveProject(opts.project)
	if err := requireProject(cmd, projectID); err != nil {
		return err
	}
	target, err := d.openGroomTarget(ctx, projectID)
	if err != nil {
		return err
	}
	defer target.close()
	project, err := target.projects.Get(ctx, projectID)
	if err != nil {
		return err
	}
	items, err := d.groomScope(ctx, target.tasks, projectID, args)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return fmt.Errorf("groom: no items in scope for project %s; capture an idea or name items", projectID)
	}

	promptPath := opts.promptFile
	if promptPath == "" {
		promptPath = groom.PromptPath
	}
	promptBytes, err := os.ReadFile(promptPath)
	if err != nil {
		return fmt.Errorf("read session prompt %s: %w", promptPath, err)
	}
	if strings.TrimSpace(string(promptBytes)) == "" {
		return fmt.Errorf("session prompt %s is empty", promptPath)
	}

	dataDir := target.dataDir
	sessionID := d.IDs.NewID("groom")
	// Every session emits five deterministic documents. The session writes them
	// inside its workspace (a sandboxed backend denies any other path); ft
	// copies the staged files out to the durable session dir.
	outputs := groomOutputs(dataDir, sessionID)

	// Snapshot the graph and start time before the session runs, so the
	// manifest can name the tasks the session produced.
	before, err := projectTaskIDs(ctx, target.tasks, projectID)
	if err != nil {
		return err
	}
	started := d.Clock.Now()

	// The session stages its outputs at the workspace-relative paths the kickoff
	// names; the run service captures exactly those files.
	capture := make([]string, 0, len(outputs))
	for _, o := range outputs {
		capture = append(capture, o.staged)
	}

	prompt := strings.TrimRight(string(promptBytes), "\n") + "\n\n" +
		groom.Kickoff(string(projectID), groomScopeItems(items), groom.StagedPaths(sessionID), sessionID, opts.unattended)

	sel, err := d.prepareRun(cmd, opts.run)
	if err != nil {
		return err
	}
	interactive := d.runInteractive(cmd, opts.unattended)

	progress := d.startRunProgress(runProgressLabel("groom session "+sessionID, sel.backendName, sel.harness.Name()), interactive)
	outcome, runErr := app.NewRunService(target.backend, target.tasks, d.Clock, d.IDs).RunProject(ctx, app.ProjectRunInput{
		ProjectID:        project.ID,
		Backend:          sel.backend,
		Harness:          sel.harness,
		WorkspaceRoot:    sel.workspace,
		RepoBase:         sel.repoBase,
		WorkspaceRefresh: sel.refresh,
		Model:            sel.model,
		Args:             sel.args,
		Prompt:           prompt,
		Capture:          capture,
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
	if runErr != nil {
		return runErr
	}

	// The session mutates the graph in its own process, so re-read the target's
	// store before capturing or judging: a backend that caches at open (jsondir,
	// jsonfile) would otherwise serve the pre-session snapshot.
	post, err := d.postRun(ctx, target)
	if err != nil {
		return err
	}
	defer post.close()

	// Read every output before recording any, so a session that wrote only some
	// of them fails without leaving a partial artifact behind.
	bodies := make(map[string]string, len(outputs))
	for _, o := range outputs {
		body, err := captureGroomOutput(sessionID, o.staged, outcome.Captured)
		if err != nil {
			return err
		}
		bodies[o.key] = body
	}
	for _, o := range outputs {
		if err := writeGroomOutput(o.path, bodies[o.key]); err != nil {
			return err
		}
	}
	artifacts := make(map[string]core.ArtifactID, len(outputs))
	for _, o := range outputs {
		artifact, err := addGroomArtifact(ctx, post.artifacts, project.ID, items, sessionID, o.path, bodies[o.key], o.kind, o.title, o.brief)
		if err != nil {
			return err
		}
		artifacts[o.key] = artifact.ID
	}
	report, deferred := artifacts["report"], artifacts["deferred"]
	deferredBody := bodies["deferred"]

	// Unattended runs must not leave work silently stranded: every scoped item
	// is either agent-ready or named in the deferred file by the time the
	// session ends.
	if opts.unattended {
		unresolved, err := unattendedUnresolved(ctx, post.tasks, post.actors, project.ID, items, deferredBody)
		if err != nil {
			return err
		}
		if len(unresolved) > 0 {
			return fmt.Errorf("groom: unattended session %s left items neither agent-ready nor deferred: %s (see %s)",
				sessionID, joinTaskIDs(unresolved), groom.DeferredQuestionsPath(dataDir, sessionID))
		}
	}

	mode := groomMode(opts.unattended, interactive)

	produced, err := producedTaskIDs(ctx, post.tasks, project.ID, before)
	if err != nil {
		return err
	}
	record := groom.SessionRecord{
		ID:         sessionID,
		Project:    string(project.ID),
		Mode:       mode,
		CreatedAt:  started,
		Scope:      groomScopeItems(items),
		Report:     string(report),
		Deferred:   string(deferred),
		Spec:       string(artifacts["spec"]),
		Plan:       string(artifacts["plan"]),
		TechDesign: string(artifacts["tech_design"]),
		Produced:   produced,
	}
	if err := groom.WriteSession(dataDir, record); err != nil {
		return err
	}

	doc := groomDoc{
		Session:    sessionID,
		Scope:      scopeIDs(items),
		Report:     string(report),
		Deferred:   string(deferred),
		Spec:       string(artifacts["spec"]),
		Plan:       string(artifacts["plan"]),
		TechDesign: string(artifacts["tech_design"]),
		Run:        "finished",
		Mode:       mode,
		Project:    string(project.ID),
		Repo:       "",
	}
	return d.emit(doc, func() {
		d.printFields(
			f("session", sessionID),
			f("scope", strings.Join(doc.Scope, ", ")),
			f("report", report),
			f("deferred", deferred),
			f("spec", artifacts["spec"]),
			f("plan", artifacts["plan"]),
			f("tech_design", artifacts["tech_design"]),
			f("run", "finished"),
			f("mode", mode),
			f("project", project.ID),
			f("repo", d.repoValue("")),
		)
	}, groomHints(project.ID, sessionID, report)...)
}

// groomMode names a session's mode for its manifest and output. A session is
// unattended when it has no product owner (--unattended), interactive when it is
// attached to a terminal so a PO can answer the grill, and headless otherwise.
func groomMode(unattended, interactive bool) string {
	switch {
	case unattended:
		return "unattended"
	case interactive:
		return "interactive"
	default:
		return "headless"
	}
}

// groomScope resolves the session's items: the named ones, or by default every
// open idea plus every open ungroomed executable task, sorted by id so the
// kickoff is deterministic.
func (d *Deps) groomScope(ctx context.Context, tasks *app.TicketService, projectID core.ProjectID, args []string) ([]*core.Ticket, error) {
	if len(args) > 0 {
		out := make([]*core.Ticket, 0, len(args))
		for _, arg := range args {
			task, err := tasks.Get(ctx, core.TicketID(arg))
			if err != nil {
				return nil, err
			}
			if task.ProjectID != projectID {
				return nil, fmt.Errorf("%w: %s belongs to project %s, not %s", core.ErrInvalid, task.ID, task.ProjectID, projectID)
			}
			out = append(out, task)
		}
		sortTasksByID(out)
		return out, nil
	}

	ideaKind := core.KindIdea
	ideas, err := tasks.List(ctx, store.TicketFilter{
		ProjectID: projectID,
		Kind:      &ideaKind,
		Statuses:  []core.TicketStatus{core.StatusTodo},
	})
	if err != nil {
		return nil, err
	}
	ungroomed := false
	tasksOpen, err := tasks.List(ctx, store.TicketFilter{
		ProjectID: projectID,
		Groomed:   &ungroomed,
		Statuses:  groomableTaskStatuses,
	})
	if err != nil {
		return nil, err
	}
	out := make([]*core.Ticket, 0, len(ideas)+len(tasksOpen))
	out = append(out, ideas...)
	for _, task := range tasksOpen {
		if task.Kind.Executable() {
			out = append(out, task)
		}
	}
	sortTasksByID(out)
	return out, nil
}

// groomableTaskStatuses are the open statuses an ungroomed task can hold and
// still be work a session should refine. Ready-for-review and resolved work is
// past grooming.
var groomableTaskStatuses = []core.TicketStatus{
	core.StatusTodo, core.StatusInProgress, core.StatusBlocked,
}

func sortTasksByID(tasks []*core.Ticket) {
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
}

func groomScopeItems(tasks []*core.Ticket) []groom.ScopeItem {
	items := make([]groom.ScopeItem, 0, len(tasks))
	for _, task := range tasks {
		items = append(items, groom.ScopeItem{ID: string(task.ID), Kind: string(task.Kind), Title: task.Title})
	}
	return items
}

func scopeIDs(tasks []*core.Ticket) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, string(task.ID))
	}
	return ids
}

// captureGroomOutput returns one session output read from the run's captured
// files, keyed by the workspace-relative path the session staged it at. A
// staged output the session never wrote, or wrote empty, is an error that names
// that path: capture must fail loudly, not look finished.
func captureGroomOutput(sessionID, staged string, captured map[string][]byte) (string, error) {
	body, ok := captured[staged]
	if !ok {
		return "", fmt.Errorf("groom session %s produced no output at %s", sessionID, staged)
	}
	if strings.TrimSpace(string(body)) == "" {
		return "", fmt.Errorf("groom session %s wrote an empty output at %s", sessionID, staged)
	}
	return string(body), nil
}

// writeGroomOutput writes one captured body to its durable session path,
// creating the session directory.
func writeGroomOutput(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create grooming session dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write grooming session output %s: %w", path, err)
	}
	return nil
}

// groomOutput names one deterministic session document: the key that identifies
// it, where the session stages it, where ft captures it, the artifact kind it is
// recorded as, and the title and brief used when recording it.
type groomOutput struct {
	key    string
	staged string
	path   string
	kind   core.ArtifactKind
	title  string
	brief  string
}

// groomOutputs returns the five documents every session emits, in capture order:
// the report and deferred questions, then the feature spec, plan, and tech
// design.
func groomOutputs(dataDir, sessionID string) []groomOutput {
	staged := groom.StagedPaths(sessionID)
	return []groomOutput{
		{
			key: "report", staged: staged.Report, path: groom.ReportPath(dataDir, sessionID),
			kind: core.ArtifactDoc, title: "Grooming report " + sessionID,
			brief: fmt.Sprintf("Deterministic report for grooming session %s", sessionID),
		},
		{
			key: "deferred", staged: staged.Deferred, path: groom.DeferredQuestionsPath(dataDir, sessionID),
			kind: core.ArtifactDoc, title: "Grooming deferred questions " + sessionID,
			brief: fmt.Sprintf("Product questions deferred by grooming session %s", sessionID),
		},
		{
			key: "spec", staged: staged.Spec, path: groom.SpecPath(dataDir, sessionID),
			kind: core.ArtifactSpec, title: "Feature spec " + sessionID,
			brief: fmt.Sprintf("Feature spec produced by grooming session %s", sessionID),
		},
		{
			key: "plan", staged: staged.Plan, path: groom.PlanPath(dataDir, sessionID),
			kind: core.ArtifactDoc, title: "Feature plan " + sessionID,
			brief: fmt.Sprintf("Feature plan produced by grooming session %s", sessionID),
		},
		{
			key: "tech_design", staged: staged.TechDesign, path: groom.TechDesignPath(dataDir, sessionID),
			kind: core.ArtifactDoc, title: "Feature tech design " + sessionID,
			brief: fmt.Sprintf("Feature tech design produced by grooming session %s", sessionID),
		},
	}
}

// addGroomArtifact records one session output as an artifact linked to the
// session directory, and to the sole item when the scope is one item.
func addGroomArtifact(ctx context.Context, artifacts *app.ArtifactService, projectID core.ProjectID, items []*core.Ticket, sessionID, path, body string, kind core.ArtifactKind, title, brief string) (*core.Artifact, error) {
	input := app.ArtifactInput{
		ProjectID: projectID,
		Kind:      kind,
		Title:     title,
		Brief:     brief,
		Body:      body,
		Path:      path,
		Links:     []core.Link{{Kind: core.LinkURL, URL: filepath.Dir(path), Title: "session " + sessionID}},
	}
	if len(items) == 1 {
		id := items[0].ID
		input.TicketID = &id
	}
	return artifacts.Add(ctx, input)
}

// groomTarget is the project a grooming command acts on and the store its data
// lives in. Its services are bound to that store, so a cross-project -p reads and
// writes the named project's graph and data dir rather than the caller's
// configured store. close releases a backend this target opened; it is a no-op
// for the configured project, whose backend the caller owns.
type groomTarget struct {
	store     config.Store
	dataDir   string
	backend   store.Backend
	projects  *app.ProjectService
	tasks     *app.TicketService
	actors    *app.ActorService
	artifacts *app.ArtifactService
	close     func()
}

// openGroomTarget resolves the project a grooming command acts on and binds its
// services to that project's store. The configured project (or an unset -p)
// reuses the caller's store; another project's store is opened from the machine
// config (see groomProjectStore). The caller closes the returned target.
func (d *Deps) openGroomTarget(ctx context.Context, projectID core.ProjectID) (groomTarget, error) {
	st, own, err := d.groomProjectStore(projectID)
	if err != nil {
		return groomTarget{}, err
	}
	dataDir, err := projectDataDir(st)
	if err != nil {
		return groomTarget{}, err
	}
	target := groomTarget{store: st, dataDir: dataDir, close: func() {}}
	if own {
		target.backend = d.Backend
		target.projects = d.Projects
		target.tasks = d.Tasks
		target.actors = d.Actors
		target.artifacts = d.Artifacts
		return target, nil
	}
	backend, err := d.openBackend(ctx, st)
	if err != nil {
		return groomTarget{}, err
	}
	target.backend = backend
	target.projects = app.NewProjectService(backend, d.Clock, d.IDs)
	target.tasks = app.NewTicketService(backend, d.Clock, d.IDs)
	target.actors = app.NewActorService(backend, d.Clock, d.IDs)
	target.artifacts = app.NewArtifactService(backend, d.Clock, d.IDs)
	target.close = func() { _ = backend.Close() }
	return target, nil
}

// groomProjectStore resolves the store a project's data lives in: the caller's
// configured store for the configured project (or an unset id), else the
// project's store from the machine config via config.StoreFor - its
// [projects.<id>] entry, or the file's top-level [store] when the project has no
// entry. own reports that the caller's configured store is the one to use, which
// is the fallback only when the machine config names no store for the project at
// all.
func (d *Deps) groomProjectStore(projectID core.ProjectID) (config.Store, bool, error) {
	if projectID == "" || string(projectID) == d.Config.Project {
		return d.Config.Store, true, nil
	}
	st, ok, err := config.StoreFor(d.UserConfigPath, string(projectID))
	if err != nil {
		return config.Store{}, false, err
	}
	if ok {
		return st, false, nil
	}
	return d.Config.Store, true, nil
}

// openBackend opens a store backend from a resolved store config.
func (d *Deps) openBackend(ctx context.Context, st config.Store) (store.Backend, error) {
	factory, err := d.StoreFactories.MustLookup(st.Backend)
	if err != nil {
		return nil, err
	}
	return factory(ctx, store.Config{
		Backend: st.Backend,
		Options: st.Options,
		Noticef: func(format string, args ...any) {
			_, _ = fmt.Fprintf(d.Err, "ft: "+format+"\n", args...)
		},
	})
}

// postRun re-reads the target's store from disk after the session ran in its own
// process, so capture and the unattended guard see the graph the session
// mutated. A store with no file path (memory) cannot be reopened and is returned
// as-is. The caller closes the returned target; a reopened one owns its backend.
func (d *Deps) postRun(ctx context.Context, target groomTarget) (groomTarget, error) {
	path := target.store.Options["path"]
	if path == "" || path == ":memory:" {
		post := target
		post.close = func() {}
		return post, nil
	}
	backend, err := d.openBackend(ctx, target.store)
	if err != nil {
		return groomTarget{}, err
	}
	post := target
	post.backend = backend
	post.projects = app.NewProjectService(backend, d.Clock, d.IDs)
	post.tasks = app.NewTicketService(backend, d.Clock, d.IDs)
	post.actors = app.NewActorService(backend, d.Clock, d.IDs)
	post.artifacts = app.NewArtifactService(backend, d.Clock, d.IDs)
	post.close = func() { _ = backend.Close() }
	return post, nil
}

// projectDataDir resolves where a project's session outputs live: the directory
// of the store, else the per-user default beside the config. The path is made
// absolute because the session runs with a different working directory than ft,
// so a relative path would name different places.
func projectDataDir(st config.Store) (string, error) {
	path := st.Options["path"]
	if path != "" && path != ":memory:" {
		// jsondir roots a document tree at its path; every other file-backed
		// backend stores one file whose parent is the data dir.
		dir := filepath.Dir(path)
		if st.Backend == "jsondir" {
			dir = path
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", fmt.Errorf("resolve project data dir from %s: %w", path, err)
		}
		return abs, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve project data dir: %w", err)
	}
	return filepath.Join(home, ".factotum"), nil
}

// groomDoc is the lossless structured shape of `ft groom`.
type groomDoc struct {
	Session    string   `json:"session" yaml:"session"`
	Scope      []string `json:"scope" yaml:"scope"`
	Report     string   `json:"report" yaml:"report"`
	Deferred   string   `json:"deferred" yaml:"deferred"`
	Spec       string   `json:"spec" yaml:"spec"`
	Plan       string   `json:"plan" yaml:"plan"`
	TechDesign string   `json:"tech_design" yaml:"tech_design"`
	Run        string   `json:"run" yaml:"run"`
	Mode       string   `json:"mode" yaml:"mode"`
	Project    string   `json:"project" yaml:"project"`
	Repo       string   `json:"repo" yaml:"repo"`
}

// unattendedUnresolved returns the scoped items an unattended session left
// neither agent-ready nor named in the deferred-questions file. Agent-ready is
// the completion contract: a task groomed and assigned to an agent, or - for an
// idea - a promoted task that is. An item matching neither means the session
// stalled or lost work, which unattended mode must never do silently.
func unattendedUnresolved(ctx context.Context, tasks *app.TicketService, actors *app.ActorService, projectID core.ProjectID, items []*core.Ticket, deferredBody string) ([]core.TicketID, error) {
	var unresolved []core.TicketID
	var projectTasks []*core.Ticket
	for _, item := range items {
		if strings.Contains(deferredBody, string(item.ID)) {
			continue
		}
		task, err := tasks.Get(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		handled, err := itemHandled(ctx, tasks, actors, projectID, task, &projectTasks)
		if err != nil {
			return nil, err
		}
		if !handled {
			unresolved = append(unresolved, item.ID)
		}
	}
	return unresolved, nil
}

// itemHandled reports whether an unattended session handled one scoped item: an
// executable task is handled when it is agent-ready or resolved (the session
// judged it unnecessary), an idea when a promoted task of it is agent-ready.
func itemHandled(ctx context.Context, tasks *app.TicketService, actors *app.ActorService, projectID core.ProjectID, item *core.Ticket, cache *[]*core.Ticket) (bool, error) {
	if item.Kind.Executable() {
		if item.Resolves(core.DefaultResolutionPolicy()) {
			return true, nil
		}
		return agentReady(ctx, actors, item)
	}
	if *cache == nil {
		listed, err := tasks.List(ctx, store.TicketFilter{ProjectID: projectID})
		if err != nil {
			return false, err
		}
		*cache = listed
	}
	for _, candidate := range *cache {
		if !candidate.Kind.Executable() || !dependsOn(candidate, item.ID) {
			continue
		}
		ready, err := agentReady(ctx, actors, candidate)
		if err != nil {
			return false, err
		}
		if ready {
			return true, nil
		}
	}
	return false, nil
}

// agentReady is the agent-bucket rule: groomed and assigned to an agent actor.
func agentReady(ctx context.Context, actors *app.ActorService, task *core.Ticket) (bool, error) {
	if !task.Groomed || task.AssigneeID == nil {
		return false, nil
	}
	actor, err := actors.Get(ctx, *task.AssigneeID)
	if err != nil {
		return false, err
	}
	return actor.Kind == core.ActorAgent, nil
}

func dependsOn(task *core.Ticket, dep core.TicketID) bool {
	for _, id := range task.Deps {
		if id == dep {
			return true
		}
	}
	return false
}

func joinTaskIDs(ids []core.TicketID) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = string(id)
	}
	return strings.Join(parts, ", ")
}

func groomHints(projectID core.ProjectID, sessionID string, reportID core.ArtifactID) []hint {
	return []hint{
		{Command: fmt.Sprintf("ft groom show %s", sessionID), About: "read the session report, deferred questions, and feature docs"},
		{Command: fmt.Sprintf("ft groom review %s --verdict <verdict> -f <review>", sessionID), About: "record the architecture reviewer's verdict and gate the build"},
		{Command: fmt.Sprintf("ft doc get %s", reportID), About: "read the grooming report artifact"},
		{Command: fmt.Sprintf("ft doc list -p %s", projectID), About: "see the session artifacts"},
	}
}
