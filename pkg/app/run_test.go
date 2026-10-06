package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	harnesspkg "github.com/khoinguyen/factotum/pkg/harness"
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
	"github.com/khoinguyen/factotum/pkg/isolation/local"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/memory"
	"github.com/khoinguyen/factotum/pkg/workspace"
)

// shellHarness runs a fixed shell script as the agent, so the orchestration can
// be exercised against the real local backend without a real agent CLI.
type shellHarness struct{ script string }

func (h shellHarness) Name() string { return "shell" }

func (h shellHarness) Spec(req harnesspkg.Request) (isolation.Spec, error) {
	return isolation.Spec{Workdir: req.Workdir}, nil
}

func (h shellHarness) Command(req harnesspkg.Request) (isolation.Command, error) {
	return isolation.Command{Argv: []string{"/bin/sh", "-c", h.script}, Workdir: req.Workdir}, nil
}

func (h shellHarness) Done(isolation.Event) bool { return false }

func (h shellHarness) Result(out []byte) (harnesspkg.Result, error) {
	return harnesspkg.Result{Output: strings.TrimSpace(string(out))}, nil
}

// runFixture wires a RunService over an in-memory store with a project whose one
// repo is an existing local checkout, so workspace resolution needs no git.
type runFixture struct {
	tasks   *TaskService
	svc     *RunService
	backend *isofake.Backend
	harness *harnessfake.Harness
	root    string
	repoDir string
	project *core.Project
	task    *core.Task
}

func newRunFixture(t *testing.T) *runFixture {
	t.Helper()
	ctx := context.Background()
	storeBackend := memory.New()
	t.Cleanup(func() { _ = storeBackend.Close() })
	clock := fixedClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ids := &seqIDs{}
	projects := NewProjectService(storeBackend, clock, ids)
	tasks := NewTaskService(storeBackend, clock, ids)

	repoDir := t.TempDir()
	project, err := projects.Create(ctx, "Acme", "demo", []core.Repository{{Name: "web", Path: repoDir}})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	task, err := tasks.Add(ctx, TaskInput{
		ProjectID:   project.ID,
		Repo:        "web",
		Title:       "Add a widget",
		Description: "Implement the widget thoroughly.",
	})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	backend := isofake.New("sandbox")
	harness := harnessfake.New("opencode")
	return &runFixture{
		tasks:   tasks,
		svc:     NewRunService(storeBackend, tasks, clock, ids),
		backend: backend,
		harness: harness,
		root:    t.TempDir(),
		repoDir: repoDir,
		project: project,
		task:    task,
	}
}

func (f *runFixture) run(t *testing.T, in RunInput) (*RunOutcome, error) {
	t.Helper()
	if in.TaskID == "" {
		in.TaskID = f.task.ID
	}
	if in.Backend == nil {
		in.Backend = f.backend
	}
	if in.Harness == nil {
		in.Harness = f.harness
	}
	if in.WorkspaceRoot == "" {
		in.WorkspaceRoot = f.root
	}
	return f.svc.Run(context.Background(), in)
}

func TestRunDeliversPromptAndCompletesTask(t *testing.T) {
	f := newRunFixture(t)
	f.backend.Program(isolation.ExecResult{Stdout: []byte("all done\n"), ExitCode: 0})

	outcome, err := f.run(t, RunInput{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcome.Status != core.StatusReadyForReview {
		t.Fatalf("outcome.Status = %q, want ready_for_review", outcome.Status)
	}
	if outcome.Output != "all done" {
		t.Fatalf("outcome.Output = %q, want %q", outcome.Output, "all done")
	}

	stored, err := f.tasks.Get(context.Background(), f.task.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.Status != core.StatusReadyForReview {
		t.Fatalf("stored status = %q, want ready_for_review", stored.Status)
	}
	if len(stored.Notes) == 0 || !strings.Contains(stored.Notes[len(stored.Notes)-1].Body, "all done") {
		t.Fatalf("expected a completion note with the output, got %+v", stored.Notes)
	}

	specs := f.backend.Prepared()
	if len(specs) != 1 {
		t.Fatalf("Prepare called %d times, want 1", len(specs))
	}
	if specs[0].Workdir != f.repoDir {
		t.Fatalf("prepared Workdir = %q, want the single checkout %q", specs[0].Workdir, f.repoDir)
	}

	cmds := f.backend.Commands()
	if len(cmds) != 1 {
		t.Fatalf("Exec called %d times, want 1", len(cmds))
	}
	prompt := cmds[0].Argv[len(cmds[0].Argv)-1]
	for _, want := range []string{"Add a widget", "Implement the widget thoroughly.", "web"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}

	if deleted := f.backend.Deleted(); len(deleted) != 1 {
		t.Fatalf("backend Delete called %d times, want 1 (cleanup)", len(deleted))
	}
}

func TestRunFailureLeavesTaskStateIntact(t *testing.T) {
	f := newRunFixture(t)
	f.backend.Program(isolation.ExecResult{Stdout: []byte("boom"), ExitCode: 2})

	outcome, err := f.run(t, RunInput{})
	if !errors.Is(err, ErrRunFailed) {
		t.Fatalf("Run() error = %v, want ErrRunFailed", err)
	}
	if outcome == nil || outcome.ExitCode != 2 {
		t.Fatalf("outcome = %+v, want exit code 2", outcome)
	}

	stored, err := f.tasks.Get(context.Background(), f.task.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.Status != core.StatusTodo {
		t.Fatalf("status = %q, want todo (unchanged on failure)", stored.Status)
	}
	if len(stored.Notes) == 0 || !strings.Contains(stored.Notes[len(stored.Notes)-1].Body, "failed") {
		t.Fatalf("expected a failure note, got %+v", stored.Notes)
	}
}

func TestRunWorkspaceFailureTouchesNothing(t *testing.T) {
	ctx := context.Background()
	storeBackend := memory.New()
	t.Cleanup(func() { _ = storeBackend.Close() })
	clock := fixedClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ids := &seqIDs{}
	projects := NewProjectService(storeBackend, clock, ids)
	tasks := NewTaskService(storeBackend, clock, ids)
	// A repo with neither a URL nor a Path cannot be materialized.
	project, err := projects.Create(ctx, "Acme", "demo", []core.Repository{{Name: "web"}})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	task, err := tasks.Add(ctx, TaskInput{ProjectID: project.ID, Repo: "web", Title: "x"})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	backend := isofake.New("sandbox")
	svc := NewRunService(storeBackend, tasks, clock, ids)
	if _, err := svc.Run(ctx, RunInput{TaskID: task.ID, Backend: backend, Harness: harnessfake.New("h"), WorkspaceRoot: t.TempDir()}); err == nil {
		t.Fatal("Run() error = nil, want workspace failure")
	}

	stored, err := tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.Status != core.StatusTodo || len(stored.Notes) != 0 {
		t.Fatalf("workspace failure changed task state: status=%q notes=%d", stored.Status, len(stored.Notes))
	}
	if got := backend.Prepared(); len(got) != 0 {
		t.Fatalf("Prepare called despite workspace failure: %+v", got)
	}
}

func TestRunRecordsLifecycleEvents(t *testing.T) {
	f := newRunFixture(t)
	f.backend.Program(isolation.ExecResult{Stdout: []byte("ok"), ExitCode: 0})
	if _, err := f.run(t, RunInput{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	events, err := f.svc.backend.Events().List(context.Background(), store.EventFilter{TaskID: &f.task.ID})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	kinds := map[core.EventKind]bool{}
	for _, event := range events {
		kinds[event.Kind] = true
	}
	if !kinds[core.EventTaskRunStarted] {
		t.Fatalf("missing run_started event: %+v", kinds)
	}
	if !kinds[core.EventTaskRunFinished] {
		t.Fatalf("missing run_finished event: %+v", kinds)
	}
}

func TestRunRequiresBackendAndHarness(t *testing.T) {
	f := newRunFixture(t)
	if _, err := f.svc.Run(context.Background(), RunInput{TaskID: f.task.ID, WorkspaceRoot: f.root}); err == nil {
		t.Fatal("Run() with no backend/harness error = nil, want error")
	}
}

// recordingGit is a workspace.Git that records clones and refreshes and reports
// the cloned URL as each checkout's remote, so a re-run sees a matching remote.
type recordingGit struct {
	clones    []workspace.CloneRequest
	refreshes []workspace.RefreshRequest
}

func (g *recordingGit) Clone(_ context.Context, req workspace.CloneRequest) error {
	g.clones = append(g.clones, req)
	return os.MkdirAll(filepath.Join(req.Dir, ".git"), 0o755)
}

func (g *recordingGit) RemoteURL(_ context.Context, dir string) (string, error) {
	for _, c := range g.clones {
		if c.Dir == dir {
			return c.URL, nil
		}
	}
	return "", errors.New("recordingGit: no remote recorded for " + dir)
}

func (g *recordingGit) Refresh(_ context.Context, req workspace.RefreshRequest) error {
	g.refreshes = append(g.refreshes, req)
	return nil
}

// TestRunRefreshesWorkspaceOnRerun proves RunInput.WorkspaceRefresh reaches
// workspace.Resolve: a cloned repo is refreshed on the second run with the flag
// and left alone without it.
func TestRunRefreshesWorkspaceOnRerun(t *testing.T) {
	ctx := context.Background()
	storeBackend := memory.New()
	t.Cleanup(func() { _ = storeBackend.Close() })
	clock := fixedClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ids := &seqIDs{}
	projects := NewProjectService(storeBackend, clock, ids)
	tasks := NewTaskService(storeBackend, clock, ids)
	project, err := projects.Create(ctx, "Acme", "demo", []core.Repository{{Name: "backend", URL: "https://example.com/acme/backend.git"}})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	task, err := tasks.Add(ctx, TaskInput{ProjectID: project.ID, Repo: "backend", Title: "x"})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("ok\n"), ExitCode: 0})
	git := &recordingGit{}
	svc := NewRunService(storeBackend, tasks, clock, ids)
	root := t.TempDir()
	run := func(refresh bool) {
		t.Helper()
		if _, err := svc.Run(ctx, RunInput{
			TaskID: task.ID, Backend: backend, Harness: harnessfake.New("h"),
			WorkspaceRoot: root, Git: git, WorkspaceRefresh: refresh,
		}); err != nil {
			t.Fatalf("Run(refresh=%v) error = %v", refresh, err)
		}
	}

	run(false)
	if len(git.clones) != 1 || len(git.refreshes) != 0 {
		t.Fatalf("first run clones=%d refreshes=%d, want 1/0", len(git.clones), len(git.refreshes))
	}
	run(true)
	if len(git.refreshes) != 1 {
		t.Errorf("refreshing run did not refresh the reused checkout: refreshes=%d", len(git.refreshes))
	}
}

// TestRunThroughRealLocalBackend drives the orchestration through the real
// unsandboxed local backend and a real subprocess, so the wiring is proven
// beyond the in-memory fakes.
func TestRunThroughRealLocalBackend(t *testing.T) {
	f := newRunFixture(t)
	backend := local.New(local.Options{AllowHost: true, Warn: io.Discard})

	outcome, err := f.run(t, RunInput{
		Backend: backend,
		Harness: shellHarness{script: "printf 'hello from host\\n'"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcome.Output != "hello from host" {
		t.Fatalf("output = %q, want %q", outcome.Output, "hello from host")
	}
	stored, err := f.tasks.Get(context.Background(), f.task.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.Status != core.StatusReadyForReview {
		t.Fatalf("status = %q, want ready_for_review", stored.Status)
	}
}
