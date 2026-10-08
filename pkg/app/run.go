package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/khoinguyen/factotum/pkg/core"
	harnesspkg "github.com/khoinguyen/factotum/pkg/harness"
	"github.com/khoinguyen/factotum/pkg/isolation"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/workspace"
)

// ErrRunFailed wraps a run whose harness exited non-zero. The task is left as it
// was; only a failure note and event record the attempt.
var ErrRunFailed = errors.New("run: harness failed")

// defaultRunOutputLimit bounds the agent output stored in a run note, so a
// chatty agent cannot bloat the store.
const defaultRunOutputLimit = 8192

// RunService orchestrates one task end-to-end: it resolves the task's
// workspace, prepares the selected isolation backend, runs the selected harness,
// captures the output, and reflects progress into the store.
type RunService struct {
	backend store.Backend
	tasks   *TicketService
	clock   Clock
	ids     IDGen
}

func NewRunService(backend store.Backend, tasks *TicketService, clock Clock, ids IDGen) *RunService {
	return &RunService{backend: backend, tasks: tasks, clock: clock, ids: ids}
}

// RunInput is one single-task run. Backend and Harness are selected by the
// caller (config/flags); this service never picks a default.
type RunInput struct {
	TicketID      core.TicketID
	Backend       isolation.IsolationBackend
	Harness       harnesspkg.Harness
	WorkspaceRoot string
	// RepoBase is the absolute directory a relative project repo Path resolves
	// against (the project root); empty means relative paths cannot be resolved.
	RepoBase string
	// WorkspaceRefresh fetches and hard-resets a reused workspace checkout to
	// its upstream before running. A reused checkout whose origin URL changed
	// always fails, refresh or not.
	WorkspaceRefresh bool
	// Git and Token are the workspace's clone ports; nil uses the host git and
	// anonymous clones.
	Git   workspace.Git
	Token workspace.TokenProvider
	Model string
	Args  []string
	// Prompt, when non-empty, replaces the task-derived prompt the harness
	// receives. It lets a run be driven by a stored prompt (a file or an
	// artifact body) instead of the task's title and description.
	Prompt string
	Actor  *core.ActorID
	// OutputLimit caps the agent output stored in the run note; 0 uses the
	// service default.
	OutputLimit int
	// Interactive attaches the agent to a terminal instead of running it
	// headless: the harness builds its TUI invocation and the backend must
	// provide a terminal. A backend that cannot attach one reports
	// isolation.ErrNoTerminal.
	Interactive bool
}

// RunOutcome records what a run did. It is populated even when the harness fails
// so the caller can report the captured output alongside the error.
type RunOutcome struct {
	TicketID  core.TicketID
	Status    core.TicketStatus
	Output    string
	ExitCode  int
	Complete  bool
	Workspace string
}

// Run executes one task. It writes progress only after the workspace is
// resolved: a failure to prepare the run never changes task state, and a failed
// harness leaves the task's status untouched.
func (s *RunService) Run(ctx context.Context, in RunInput) (*RunOutcome, error) {
	if in.Backend == nil {
		return nil, errors.New("run: no isolation backend selected")
	}
	if in.Harness == nil {
		return nil, errors.New("run: no harness selected")
	}

	task, err := s.backend.Tickets().Get(ctx, in.TicketID)
	if err != nil {
		return nil, err
	}
	if task.Status == core.StatusDone || task.Status == core.StatusCancelled {
		return nil, fmt.Errorf("%w: task %s is %s", core.ErrInvalid, task.ID, task.Status)
	}
	project, err := s.backend.Projects().Get(ctx, task.ProjectID)
	if err != nil {
		return nil, err
	}

	plan, err := workspace.Resolve(ctx, *project, *task, workspace.Options{
		Root:    in.WorkspaceRoot,
		Base:    in.RepoBase,
		Git:     in.Git,
		Token:   in.Token,
		Refresh: in.WorkspaceRefresh,
	})
	if err != nil {
		return nil, err
	}

	res, err := s.runHarness(ctx, harnessRun{
		backend:     in.Backend,
		harness:     in.Harness,
		plan:        plan,
		prompt:      runPrompt(project, task, in.Prompt),
		model:       in.Model,
		args:        in.Args,
		interactive: in.Interactive,
		labels:      map[string]string{"project": string(project.ID), "task": string(task.ID)},
		// The run is about to start: record it before Exec so a harness or wait
		// failure is observable, and abort if the event cannot be written.
		onStart: func() error {
			return s.appendRunEvent(ctx, task, core.EventTaskRunStarted,
				fmt.Sprintf("run started for %s with %s/%s", task.ID, in.Backend.Name(), in.Harness.Name()),
				map[string]any{"backend": in.Backend.Name(), "harness": in.Harness.Name()})
		},
	})
	if err != nil {
		return nil, err
	}

	outcome := &RunOutcome{
		TicketID:  task.ID,
		Status:    task.Status,
		Output:    res.output,
		ExitCode:  res.exitCode,
		Complete:  res.complete,
		Workspace: plan.Root,
	}

	if res.exitCode != 0 {
		if err := s.recordRunNote(ctx, task, in, runReport("failed", res.exitCode, res.output, in.OutputLimit)); err != nil {
			return outcome, err
		}
		if err := s.finishRunEvent(ctx, task, res.exitCode, false); err != nil {
			return outcome, err
		}
		return outcome, fmt.Errorf("%w: exit code %d", ErrRunFailed, res.exitCode)
	}

	updated, err := s.tasks.SetStatus(ctx, task.ID, core.StatusReadyForReview)
	if err != nil {
		return outcome, err
	}
	outcome.Status = updated.Status
	if err := s.recordRunNote(ctx, task, in, runReport("finished", res.exitCode, res.output, in.OutputLimit)); err != nil {
		return outcome, err
	}
	if err := s.finishRunEvent(ctx, task, res.exitCode, true); err != nil {
		return outcome, err
	}
	return outcome, nil
}

// ProjectRunInput is one task-less run: a prompt run over every repository of a
// project, with no task to reflect progress into.
type ProjectRunInput struct {
	ProjectID        core.ProjectID
	Backend          isolation.IsolationBackend
	Harness          harnesspkg.Harness
	WorkspaceRoot    string
	WorkspaceRefresh bool
	// RepoBase is the absolute directory a relative project repo Path resolves
	// against (the project root); empty means relative paths cannot be resolved.
	RepoBase string
	// Git and Token are the workspace's clone ports; nil uses the host git and
	// anonymous clones.
	Git   workspace.Git
	Token workspace.TokenProvider
	Model string
	Args  []string
	// Prompt is the instruction the harness receives, used verbatim.
	Prompt string
	// Capture names workspace-relative paths whose contents are read out of the
	// environment after the harness runs, before the environment is torn down.
	// The files are returned in ProjectRunOutcome.Captured. A path the run
	// never wrote is omitted, so the caller decides how to report it.
	Capture []string
	// Interactive attaches the agent to a terminal instead of running it
	// headless: the harness builds its TUI invocation and the backend must
	// provide a terminal. A backend that cannot attach one reports
	// isolation.ErrNoTerminal.
	Interactive bool
}

// ProjectRunOutcome records what a task-less run did. There is no task status:
// the run never touches the task graph.
type ProjectRunOutcome struct {
	ProjectID core.ProjectID
	Output    string
	ExitCode  int
	Complete  bool
	Workspace string
	// Checkouts are the project's repos materialized for the run.
	Checkouts []workspace.Checkout
	// Captured holds the contents of the requested Capture paths, keyed by the
	// path as requested. A requested path the run did not write is absent.
	Captured map[string][]byte
}

// RunProject runs one prompt over all of a project's repositories without a
// task. It writes nothing to the task graph: no task note and no status change,
// because there is no task. A failed harness still returns the captured outcome
// alongside ErrRunFailed.
func (s *RunService) RunProject(ctx context.Context, in ProjectRunInput) (*ProjectRunOutcome, error) {
	if in.Backend == nil {
		return nil, errors.New("run: no isolation backend selected")
	}
	if in.Harness == nil {
		return nil, errors.New("run: no harness selected")
	}

	project, err := s.backend.Projects().Get(ctx, in.ProjectID)
	if err != nil {
		return nil, err
	}

	// An empty task names no repo, so resolution spans every project repo.
	plan, err := workspace.Resolve(ctx, *project, core.Ticket{}, workspace.Options{
		Root:    in.WorkspaceRoot,
		Base:    in.RepoBase,
		Git:     in.Git,
		Token:   in.Token,
		Refresh: in.WorkspaceRefresh,
	})
	if err != nil {
		return nil, err
	}

	res, err := s.runHarness(ctx, harnessRun{
		backend:     in.Backend,
		harness:     in.Harness,
		plan:        plan,
		prompt:      in.Prompt,
		model:       in.Model,
		args:        in.Args,
		interactive: in.Interactive,
		labels:      map[string]string{"project": string(project.ID)},
		capture:     in.Capture,
	})
	if err != nil {
		return nil, err
	}

	outcome := &ProjectRunOutcome{
		ProjectID: project.ID,
		Output:    res.output,
		ExitCode:  res.exitCode,
		Complete:  res.complete,
		Workspace: plan.Root,
		Checkouts: plan.Checkouts,
		Captured:  res.captured,
	}
	if res.exitCode != 0 {
		return outcome, fmt.Errorf("%w: exit code %d", ErrRunFailed, res.exitCode)
	}
	return outcome, nil
}

// harnessRun is one harness invocation, shared by the task and project run
// paths.
type harnessRun struct {
	backend isolation.IsolationBackend
	harness harnesspkg.Harness
	plan    *workspace.Plan
	prompt  string
	model   string
	args    []string
	labels  map[string]string
	// interactive builds the harness's TUI invocation and asks for a terminal.
	interactive bool
	// capture names workspace-relative paths read out of the environment after
	// the command exits and before the environment is deleted.
	capture []string
	// onStart runs after the command is built and before Exec; a task run uses
	// it to record the run-started event and abort if it cannot be written. Nil
	// means no pre-exec hook.
	onStart func() error
}

// harnessResult is what a harness returned from one run.
type harnessResult struct {
	exitCode int
	output   string
	complete bool
	// captured holds the requested capture paths' contents, keyed by the path as
	// requested. Nil when the run requested no capture.
	captured map[string][]byte
}

// runHarness prepares the selected backend, runs the harness command in the
// resolved workspace, and parses its output. Cleanup always runs.
func (s *RunService) runHarness(ctx context.Context, r harnessRun) (harnessResult, error) {
	req := harnesspkg.Request{
		Prompt:      r.prompt,
		Model:       r.model,
		Workdir:     runWorkdir(r.plan),
		Args:        r.args,
		Labels:      r.labels,
		Interactive: r.interactive,
	}
	spec, err := r.harness.Spec(req)
	if err != nil {
		return harnessResult{}, err
	}
	handle, err := r.backend.Prepare(ctx, spec)
	if err != nil {
		return harnessResult{}, err
	}
	defer func() { _ = r.backend.Delete(context.WithoutCancel(ctx), handle) }()

	command, err := r.harness.Command(req)
	if err != nil {
		return harnessResult{}, err
	}
	if r.onStart != nil {
		if err := r.onStart(); err != nil {
			return harnessResult{}, err
		}
	}

	execution, err := r.backend.Exec(ctx, handle, command)
	if err != nil {
		return harnessResult{}, err
	}

	// Drain events so a streamed backend is never blocked; Wait still returns the
	// complete buffered output.
	for range execution.Events() {
	}
	result, err := execution.Wait(ctx)
	if err != nil {
		return harnessResult{}, err
	}
	parsed, err := r.harness.Result(result.Stdout)
	if err != nil {
		return harnessResult{}, err
	}
	captured, err := captureFiles(ctx, r.backend, handle, r.capture)
	if err != nil {
		return harnessResult{}, err
	}
	return harnessResult{exitCode: result.ExitCode, output: parsed.Output, complete: parsed.Complete, captured: captured}, nil
}

// captureFiles reads the requested workspace-relative paths out of the
// environment before the caller deletes it, so a run can return files its
// harness wrote inside the workspace. It is a no-op without requested paths.
func captureFiles(ctx context.Context, backend isolation.IsolationBackend, h isolation.Handle, paths []string) (map[string][]byte, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	files, err := backend.Download(ctx, h, paths)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(files))
	for _, f := range files {
		out[f.Path] = f.Content
	}
	return out, nil
}

// runPrompt returns the prompt a harness receives: the caller-supplied override
// when set, else the task-derived prompt. An override is data (a stored prompt),
// so it is used verbatim.
func runPrompt(project *core.Project, task *core.Ticket, override string) string {
	if override != "" {
		return override
	}
	return TaskPrompt(project, task)
}

// TaskPrompt builds the instruction a harness receives for a task: its title,
// identity, and description, so a run is reproducible from the task alone.
func TaskPrompt(project *core.Project, task *core.Ticket) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", task.Title)
	fmt.Fprintf(&b, "Task %s in project %s", task.ID, project.ID)
	if task.Repo != "" {
		fmt.Fprintf(&b, " (repo %s)", task.Repo)
	}
	b.WriteString("\n")
	if description := strings.TrimSpace(task.Description); description != "" {
		fmt.Fprintf(&b, "\n%s\n", description)
	}
	return b.String()
}

// runWorkdir is the directory the harness runs in: the single checkout's path
// when a task touches one repo, otherwise the workspace root that holds them.
func runWorkdir(plan *workspace.Plan) string {
	if len(plan.Checkouts) == 1 {
		return plan.Checkouts[0].Path
	}
	return plan.Root
}

func (s *RunService) recordRunNote(ctx context.Context, task *core.Ticket, in RunInput, body string) error {
	_, err := s.tasks.AddNote(ctx, task.ID, NoteInput{
		Body:   body,
		Author: in.Actor,
		System: true,
	})
	return err
}

func (s *RunService) appendRunEvent(ctx context.Context, task *core.Ticket, kind core.EventKind, summary string, data map[string]any) error {
	return appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: task.ProjectID,
		TicketID:  &task.ID,
		Kind:      kind,
		Summary:   summary,
		Data:      data,
	})
}

// finishRunEvent records a completed run (success or failure).
func (s *RunService) finishRunEvent(ctx context.Context, task *core.Ticket, exitCode int, completed bool) error {
	state := "finished"
	if !completed {
		state = "failed"
	}
	return s.appendRunEvent(ctx, task, core.EventTaskRunFinished,
		fmt.Sprintf("run %s for %s (exit %d)", state, task.ID, exitCode),
		map[string]any{"exit_code": exitCode, "completed": completed})
}

func runReport(state string, exitCode int, output string, limit int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "ft run %s (exit %d)", state, exitCode)
	if output = strings.TrimSpace(output); output != "" {
		b.WriteString("\n\n")
		b.WriteString(truncateOutput(output, limit))
	}
	return b.String()
}

const truncationMarker = "\n... [truncated]"

func truncateOutput(s string, limit int) string {
	if limit <= 0 {
		limit = defaultRunOutputLimit
	}
	if len(s) <= limit {
		return s
	}
	if limit <= len(truncationMarker) {
		return s[:limit]
	}
	return s[:limit-len(truncationMarker)] + truncationMarker
}
