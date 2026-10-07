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
	tasks   *TaskService
	clock   Clock
	ids     IDGen
}

func NewRunService(backend store.Backend, tasks *TaskService, clock Clock, ids IDGen) *RunService {
	return &RunService{backend: backend, tasks: tasks, clock: clock, ids: ids}
}

// RunInput is one single-task run. Backend and Harness are selected by the
// caller (config/flags); this service never picks a default.
type RunInput struct {
	TaskID        core.TaskID
	Backend       isolation.IsolationBackend
	Harness       harnesspkg.Harness
	WorkspaceRoot string
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
}

// RunOutcome records what a run did. It is populated even when the harness fails
// so the caller can report the captured output alongside the error.
type RunOutcome struct {
	TaskID    core.TaskID
	Status    core.TaskStatus
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

	task, err := s.backend.Tasks().Get(ctx, in.TaskID)
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
		Git:     in.Git,
		Token:   in.Token,
		Refresh: in.WorkspaceRefresh,
	})
	if err != nil {
		return nil, err
	}

	req := harnesspkg.Request{
		Prompt:  runPrompt(project, task, in.Prompt),
		Model:   in.Model,
		Workdir: runWorkdir(plan),
		Args:    in.Args,
		Labels:  map[string]string{"project": string(project.ID), "task": string(task.ID)},
	}
	spec, err := in.Harness.Spec(req)
	if err != nil {
		return nil, err
	}
	handle, err := in.Backend.Prepare(ctx, spec)
	if err != nil {
		return nil, err
	}
	defer func() { _ = in.Backend.Delete(context.WithoutCancel(ctx), handle) }()

	command, err := in.Harness.Command(req)
	if err != nil {
		return nil, err
	}

	// The run is about to start: record it before Exec so a harness or wait
	// failure is observable, and abort if the event cannot be written.
	if err := s.appendRunEvent(ctx, task, core.EventTaskRunStarted,
		fmt.Sprintf("run started for %s with %s/%s", task.ID, in.Backend.Name(), in.Harness.Name()),
		map[string]any{"backend": in.Backend.Name(), "harness": in.Harness.Name()}); err != nil {
		return nil, err
	}

	execution, err := in.Backend.Exec(ctx, handle, command)
	if err != nil {
		return nil, err
	}

	// Drain events so a streamed backend is never blocked; Wait still returns the
	// complete buffered output.
	for range execution.Events() {
	}
	result, err := execution.Wait(ctx)
	if err != nil {
		return nil, err
	}
	parsed, err := in.Harness.Result(result.Stdout)
	if err != nil {
		return nil, err
	}

	outcome := &RunOutcome{
		TaskID:    task.ID,
		Status:    task.Status,
		Output:    parsed.Output,
		ExitCode:  result.ExitCode,
		Complete:  parsed.Complete,
		Workspace: plan.Root,
	}

	if result.ExitCode != 0 {
		if err := s.recordRunNote(ctx, task, in, runReport("failed", result.ExitCode, parsed.Output, in.OutputLimit)); err != nil {
			return outcome, err
		}
		if err := s.finishRunEvent(ctx, task, result.ExitCode, false); err != nil {
			return outcome, err
		}
		return outcome, fmt.Errorf("%w: exit code %d", ErrRunFailed, result.ExitCode)
	}

	updated, err := s.tasks.SetStatus(ctx, task.ID, core.StatusReadyForReview)
	if err != nil {
		return outcome, err
	}
	outcome.Status = updated.Status
	if err := s.recordRunNote(ctx, task, in, runReport("finished", result.ExitCode, parsed.Output, in.OutputLimit)); err != nil {
		return outcome, err
	}
	if err := s.finishRunEvent(ctx, task, result.ExitCode, true); err != nil {
		return outcome, err
	}
	return outcome, nil
}

// runPrompt returns the prompt a harness receives: the caller-supplied override
// when set, else the task-derived prompt. An override is data (a stored prompt),
// so it is used verbatim.
func runPrompt(project *core.Project, task *core.Task, override string) string {
	if override != "" {
		return override
	}
	return TaskPrompt(project, task)
}

// TaskPrompt builds the instruction a harness receives for a task: its title,
// identity, and description, so a run is reproducible from the task alone.
func TaskPrompt(project *core.Project, task *core.Task) string {
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

func (s *RunService) recordRunNote(ctx context.Context, task *core.Task, in RunInput, body string) error {
	_, err := s.tasks.AddNote(ctx, task.ID, NoteInput{
		Body:   body,
		Author: in.Actor,
		System: true,
	})
	return err
}

func (s *RunService) appendRunEvent(ctx context.Context, task *core.Task, kind core.EventKind, summary string, data map[string]any) error {
	return appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: task.ProjectID,
		TaskID:    &task.ID,
		Kind:      kind,
		Summary:   summary,
		Data:      data,
	})
}

// finishRunEvent records a completed run (success or failure).
func (s *RunService) finishRunEvent(ctx context.Context, task *core.Task, exitCode int, completed bool) error {
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
