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
			"paths of the report and deferred-questions files, runs the durable grooming prompt through the\n" +
			"run service, and records the two outputs as doc artifacts. With --unattended the kickoff\n" +
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
	cmd.AddCommand(newGroomListCommand(deps), newGroomShowCommand(deps))
	return cmd
}

// runGroom resolves the scope, runs the durable session prompt with the kickoff
// appended, and captures the two outputs.
func (d *Deps) runGroom(cmd *cobra.Command, args []string, opts groomOptions) error {
	ctx := cmd.Context()
	projectID := d.resolveProject(opts.project)
	if err := requireProject(cmd, projectID); err != nil {
		return err
	}
	project, err := d.Projects.Get(ctx, projectID)
	if err != nil {
		return err
	}
	items, err := d.groomScope(ctx, projectID, args)
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

	dataDir, err := projectDataDir(d.Config)
	if err != nil {
		return err
	}
	sessionID := d.IDs.NewID("groom")
	reportPath := groom.ReportPath(dataDir, sessionID)
	deferredPath := groom.DeferredQuestionsPath(dataDir, sessionID)
	// The session writes inside its workspace (a sandboxed backend denies any
	// other path); ft copies the staged files out to the durable session dir.
	stagedReport := groom.StagedReportPath(sessionID)
	stagedDeferred := groom.StagedDeferredQuestionsPath(sessionID)

	// Snapshot the graph and start time before the session runs, so the
	// manifest can name the tasks the session produced.
	before, err := d.projectTaskIDs(ctx, projectID)
	if err != nil {
		return err
	}
	started := d.Clock.Now()

	prompt := strings.TrimRight(string(promptBytes), "\n") + "\n\n" +
		groom.Kickoff(string(projectID), groomScopeItems(items), stagedReport, stagedDeferred, opts.unattended)

	sel, err := d.prepareRun(cmd, opts.run)
	if err != nil {
		return err
	}
	interactive := d.runInteractive(cmd, opts.unattended)

	progress := d.startRunProgress(runProgressLabel("groom session "+sessionID, sel.backendName, sel.harness.Name()), interactive)
	outcome, runErr := app.NewRunService(d.Backend, d.Tasks, d.Clock, d.IDs).RunProject(ctx, app.ProjectRunInput{
		ProjectID:        project.ID,
		Backend:          sel.backend,
		Harness:          sel.harness,
		WorkspaceRoot:    sel.workspace,
		RepoBase:         sel.repoBase,
		WorkspaceRefresh: sel.refresh,
		Model:            sel.model,
		Args:             sel.args,
		Prompt:           prompt,
		Capture:          []string{stagedReport, stagedDeferred},
		Interactive:      interactive,
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

	// The session mutates the graph in its own process, so re-read the caller's
	// store before capturing or judging: a backend that caches at open (jsondir,
	// jsonfile) would otherwise serve the pre-session snapshot.
	post, err := d.reopenStore(ctx)
	if err != nil {
		return err
	}
	defer post.close()

	// Read both outputs before recording either, so a session that wrote only
	// one of them fails without leaving a partial artifact behind.
	reportBody, err := captureGroomOutput(sessionID, stagedReport, outcome.Captured)
	if err != nil {
		return err
	}
	deferredBody, err := captureGroomOutput(sessionID, stagedDeferred, outcome.Captured)
	if err != nil {
		return err
	}
	if err := writeGroomOutput(reportPath, reportBody); err != nil {
		return err
	}
	if err := writeGroomOutput(deferredPath, deferredBody); err != nil {
		return err
	}
	report, err := addGroomArtifact(ctx, post.artifacts, project.ID, items, sessionID, reportPath, reportBody,
		"Grooming report "+sessionID,
		fmt.Sprintf("Deterministic report for grooming session %s", sessionID))
	if err != nil {
		return err
	}
	deferred, err := addGroomArtifact(ctx, post.artifacts, project.ID, items, sessionID, deferredPath, deferredBody,
		"Grooming deferred questions "+sessionID,
		fmt.Sprintf("Product questions deferred by grooming session %s", sessionID))
	if err != nil {
		return err
	}

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
				sessionID, joinTaskIDs(unresolved), deferredPath)
		}
	}

	mode := groomMode(opts.unattended, interactive)

	produced, err := producedTaskIDs(ctx, post.tasks, project.ID, before)
	if err != nil {
		return err
	}
	record := groom.SessionRecord{
		ID:        sessionID,
		Project:   string(project.ID),
		Mode:      mode,
		CreatedAt: started,
		Scope:     groomScopeItems(items),
		Report:    string(report.ID),
		Deferred:  string(deferred.ID),
		Produced:  produced,
	}
	if err := groom.WriteSession(dataDir, record); err != nil {
		return err
	}

	doc := groomDoc{
		Session:  sessionID,
		Scope:    scopeIDs(items),
		Report:   string(report.ID),
		Deferred: string(deferred.ID),
		Run:      "finished",
		Mode:     mode,
		Project:  string(project.ID),
		Repo:     "",
	}
	return d.emit(doc, func() {
		d.printFields(
			f("session", sessionID),
			f("scope", strings.Join(doc.Scope, ", ")),
			f("report", report.ID),
			f("deferred", deferred.ID),
			f("run", "finished"),
			f("mode", mode),
			f("project", project.ID),
			f("repo", d.repoValue("")),
		)
	}, groomHints(project.ID, sessionID, report.ID)...)
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
func (d *Deps) groomScope(ctx context.Context, projectID core.ProjectID, args []string) ([]*core.Ticket, error) {
	if len(args) > 0 {
		out := make([]*core.Ticket, 0, len(args))
		for _, arg := range args {
			task, err := d.Tasks.Get(ctx, core.TicketID(arg))
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
	ideas, err := d.Tasks.List(ctx, store.TicketFilter{
		ProjectID: projectID,
		Kind:      &ideaKind,
		Statuses:  []core.TicketStatus{core.StatusTodo},
	})
	if err != nil {
		return nil, err
	}
	ungroomed := false
	tasks, err := d.Tasks.List(ctx, store.TicketFilter{
		ProjectID: projectID,
		Groomed:   &ungroomed,
		Statuses:  groomableTaskStatuses,
	})
	if err != nil {
		return nil, err
	}
	out := make([]*core.Ticket, 0, len(ideas)+len(tasks))
	out = append(out, ideas...)
	for _, task := range tasks {
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

// addGroomArtifact records one session output as a doc artifact linked to the
// session directory, and to the sole item when the scope is one item.
func addGroomArtifact(ctx context.Context, artifacts *app.ArtifactService, projectID core.ProjectID, items []*core.Ticket, sessionID, path, body, title, brief string) (*core.Artifact, error) {
	input := app.ArtifactInput{
		ProjectID: projectID,
		Kind:      core.ArtifactDoc,
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

// groomStore is the post-run view of the caller's store: the services bound to a
// backend re-read from disk after the session finished.
type groomStore struct {
	tasks     *app.TicketService
	actors    *app.ActorService
	artifacts *app.ArtifactService
	close     func()
}

// reopenStore returns the caller's store re-read from disk, so the capture and
// the unattended guard see the graph the session mutated in its own process. A
// backend that caches at open (jsondir, jsonfile) would otherwise serve the
// pre-session snapshot. A memory store is returned as-is: reopening would lose
// it, and no other process can share it.
func (d *Deps) reopenStore(ctx context.Context) (groomStore, error) {
	path := d.Config.Store.Options["path"]
	if path == "" || path == ":memory:" {
		return groomStore{tasks: d.Tasks, actors: d.Actors, artifacts: d.Artifacts, close: func() {}}, nil
	}
	factory, err := d.StoreFactories.MustLookup(d.Config.Store.Backend)
	if err != nil {
		return groomStore{}, err
	}
	backend, err := factory(ctx, store.Config{Backend: d.Config.Store.Backend, Options: d.Config.Store.Options})
	if err != nil {
		return groomStore{}, err
	}
	return groomStore{
		tasks:     app.NewTicketService(backend, d.Clock, d.IDs),
		actors:    app.NewActorService(backend, d.Clock, d.IDs),
		artifacts: app.NewArtifactService(backend, d.Clock, d.IDs),
		close:     func() { _ = backend.Close() },
	}, nil
}

// projectDataDir resolves where a project's session outputs live: the directory
// of the configured store, else the per-user default beside the config. The path
// is made absolute because the session runs with a different working directory
// than ft, so a relative path would name different places.
func projectDataDir(cfg config.Config) (string, error) {
	path := cfg.Store.Options["path"]
	if path != "" && path != ":memory:" {
		// jsondir roots a document tree at its path; every other file-backed
		// backend stores one file whose parent is the data dir.
		dir := filepath.Dir(path)
		if cfg.Store.Backend == "jsondir" {
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
	Session  string   `json:"session" yaml:"session"`
	Scope    []string `json:"scope" yaml:"scope"`
	Report   string   `json:"report" yaml:"report"`
	Deferred string   `json:"deferred" yaml:"deferred"`
	Run      string   `json:"run" yaml:"run"`
	Mode     string   `json:"mode" yaml:"mode"`
	Project  string   `json:"project" yaml:"project"`
	Repo     string   `json:"repo" yaml:"repo"`
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
		{Command: fmt.Sprintf("ft groom show %s", sessionID), About: "read the session report and deferred questions"},
		{Command: fmt.Sprintf("ft doc get %s", reportID), About: "read the grooming report artifact"},
		{Command: fmt.Sprintf("ft doc list -p %s", projectID), About: "see the session artifacts"},
	}
}
