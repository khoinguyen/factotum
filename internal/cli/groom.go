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
	run        runOptions
}

func newGroomCommand(deps *Deps) *cobra.Command {
	var opts groomOptions

	cmd := &cobra.Command{
		Use:   "groom [item...]",
		Short: "Run a grooming session over a project's ideas and tasks",
		Long: "Run a task-less grooming session over every repository of the project. `ft groom`\n" +
			"resolves the scope - the named items, or by default every open idea plus every open\n" +
			"ungroomed task - injects a session kickoff naming the scope and the absolute paths of\n" +
			"the report and deferred-questions files, runs the durable grooming prompt through the\n" +
			"run service, and records the two outputs as doc artifacts. The backend and harness\n" +
			"are selected explicitly, by flag or by the [run] config table; there is no default\n" +
			"backend.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return deps.runGroom(cmd, args, opts)
		},
	}
	cmd.Flags().StringVarP(&opts.project, "project", "p", "", "project id (defaults to the configured project)")
	cmd.Flags().StringVar(&opts.promptFile, "prompt-file", "", "session prompt file (default "+groom.PromptPath+")")
	cmd.Flags().StringVar(&opts.run.backend, "backend", "", "isolation backend name (required; e.g. local, openshell, docker)")
	cmd.Flags().StringVar(&opts.run.harness, "harness", "", "harness name (required; e.g. opencode)")
	cmd.Flags().StringVar(&opts.run.workspace, "workspace", "", "workspace root directory (default under the config dir)")
	cmd.Flags().StringVar(&opts.run.model, "model", "", "model override passed to the harness")
	cmd.Flags().StringArrayVar(&opts.run.args, "arg", nil, "extra argument passed to the harness (repeatable)")
	cmd.Flags().BoolVar(&opts.run.allowHost, "allow-host", false, "opt in to the unsandboxed local backend (dev-only)")
	cmd.Flags().BoolVar(&opts.run.refresh, "refresh", false, "fetch and reset a reused workspace checkout before running")
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
	sessionDir := groom.SessionDir(dataDir, sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return fmt.Errorf("create grooming session dir %s: %w", sessionDir, err)
	}
	reportPath := groom.ReportPath(dataDir, sessionID)
	deferredPath := groom.DeferredQuestionsPath(dataDir, sessionID)

	prompt := strings.TrimRight(string(promptBytes), "\n") + "\n\n" +
		groom.Kickoff(string(projectID), groomScopeItems(items), reportPath, deferredPath)

	sel, err := d.prepareRun(cmd, opts.run)
	if err != nil {
		return err
	}

	_, runErr := app.NewRunService(d.Backend, d.Tasks, d.Clock, d.IDs).RunProject(ctx, app.ProjectRunInput{
		ProjectID:        project.ID,
		Backend:          sel.backend,
		Harness:          sel.harness,
		WorkspaceRoot:    sel.workspace,
		WorkspaceRefresh: sel.refresh,
		Model:            sel.model,
		Args:             sel.args,
		Prompt:           prompt,
	})
	if errors.Is(runErr, local.ErrNotOptedIn) {
		return usageError(cmd, "backend %q runs unsandboxed and is not opted in; pass --allow-host (or set run.allow_host) only for trusted work", sel.backendName)
	}
	if runErr != nil {
		return runErr
	}

	// Read both outputs before recording either, so a session that wrote only
	// one of them fails without leaving a partial artifact behind.
	reportBody, err := readGroomOutput(sessionID, reportPath)
	if err != nil {
		return err
	}
	deferredBody, err := readGroomOutput(sessionID, deferredPath)
	if err != nil {
		return err
	}
	report, err := d.addGroomArtifact(ctx, project.ID, items, sessionID, reportPath, reportBody,
		"Grooming report "+sessionID,
		fmt.Sprintf("Deterministic report for grooming session %s", sessionID))
	if err != nil {
		return err
	}
	deferred, err := d.addGroomArtifact(ctx, project.ID, items, sessionID, deferredPath, deferredBody,
		"Grooming deferred questions "+sessionID,
		fmt.Sprintf("Product questions deferred by grooming session %s", sessionID))
	if err != nil {
		return err
	}

	doc := groomDoc{
		Session:  sessionID,
		Scope:    scopeIDs(items),
		Report:   string(report.ID),
		Deferred: string(deferred.ID),
		Run:      "finished",
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
			f("project", project.ID),
			f("repo", d.repoValue("")),
		)
	}, groomHints(project.ID, report.ID)...)
}

// groomScope resolves the session's items: the named ones, or by default every
// open idea plus every open ungroomed executable task, sorted by id so the
// kickoff is deterministic.
func (d *Deps) groomScope(ctx context.Context, projectID core.ProjectID, args []string) ([]*core.Task, error) {
	if len(args) > 0 {
		out := make([]*core.Task, 0, len(args))
		for _, arg := range args {
			task, err := d.Tasks.Get(ctx, core.TaskID(arg))
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
	ideas, err := d.Tasks.List(ctx, store.TaskFilter{
		ProjectID: projectID,
		Kind:      &ideaKind,
		Statuses:  []core.TaskStatus{core.StatusTodo},
	})
	if err != nil {
		return nil, err
	}
	ungroomed := false
	tasks, err := d.Tasks.List(ctx, store.TaskFilter{
		ProjectID: projectID,
		Groomed:   &ungroomed,
		Statuses:  groomableTaskStatuses,
	})
	if err != nil {
		return nil, err
	}
	out := make([]*core.Task, 0, len(ideas)+len(tasks))
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
var groomableTaskStatuses = []core.TaskStatus{
	core.StatusTodo, core.StatusInProgress, core.StatusBlocked,
}

func sortTasksByID(tasks []*core.Task) {
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
}

func groomScopeItems(tasks []*core.Task) []groom.ScopeItem {
	items := make([]groom.ScopeItem, 0, len(tasks))
	for _, task := range tasks {
		items = append(items, groom.ScopeItem{ID: string(task.ID), Kind: string(task.Kind), Title: task.Title})
	}
	return items
}

func scopeIDs(tasks []*core.Task) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, string(task.ID))
	}
	return ids
}

// readGroomOutput reads one session output. A missing or empty output is an
// error: a session that produces nothing durable must fail loudly, not look
// finished.
func readGroomOutput(sessionID, path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("groom session %s produced no output at %s: %w", sessionID, path, err)
	}
	if strings.TrimSpace(string(body)) == "" {
		return "", fmt.Errorf("groom session %s wrote an empty output at %s", sessionID, path)
	}
	return string(body), nil
}

// addGroomArtifact records one session output as a doc artifact linked to the
// session directory, and to the sole item when the scope is one item.
func (d *Deps) addGroomArtifact(ctx context.Context, projectID core.ProjectID, items []*core.Task, sessionID, path, body, title, brief string) (*core.Artifact, error) {
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
		input.TaskID = &id
	}
	return d.Artifacts.Add(ctx, input)
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
	Project  string   `json:"project" yaml:"project"`
	Repo     string   `json:"repo" yaml:"repo"`
}

func groomHints(projectID core.ProjectID, reportID core.ArtifactID) []hint {
	return []hint{
		{Command: fmt.Sprintf("ft doc get %s", reportID), About: "read the grooming report"},
		{Command: fmt.Sprintf("ft doc list -p %s", projectID), About: "see the session artifacts"},
	}
}
