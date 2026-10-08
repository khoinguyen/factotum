package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/memory"
)

// projectRunFixture wires a RunService over a task-less project: two repos that
// are existing local checkouts, so workspace resolution needs no git and spans
// the whole project.
type projectRunFixture struct {
	svc     *RunService
	backend *isofake.Backend
	harness *harnessfake.Harness
	project *core.Project
	root    string
}

func newProjectRunFixture(t *testing.T) *projectRunFixture {
	t.Helper()
	ctx := context.Background()
	storeBackend := memory.New()
	t.Cleanup(func() { _ = storeBackend.Close() })
	clock := fixedClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ids := &seqIDs{}
	projects := NewProjectService(storeBackend, clock, ids)
	tasks := NewTicketService(storeBackend, clock, ids)

	project, err := projects.Create(ctx, "Acme", "demo", []core.Repository{
		{Name: "backend", Path: t.TempDir()},
		{Name: "web", Path: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return &projectRunFixture{
		svc:     NewRunService(storeBackend, tasks, clock, ids),
		backend: isofake.New("sandbox"),
		harness: harnessfake.New("opencode"),
		project: project,
		root:    t.TempDir(),
	}
}

func (f *projectRunFixture) run(t *testing.T, in ProjectRunInput) (*ProjectRunOutcome, error) {
	t.Helper()
	if in.ProjectID == "" {
		in.ProjectID = f.project.ID
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
	return f.svc.RunProject(context.Background(), in)
}

// TestRunProjectMaterializesAllReposAndDeliversPromptVerbatim proves a task-less
// run spans every project repo, runs in the workspace root, and hands the prompt
// to the harness unchanged.
func TestRunProjectMaterializesAllReposAndDeliversPromptVerbatim(t *testing.T) {
	f := newProjectRunFixture(t)
	f.backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})

	outcome, err := f.run(t, ProjectRunInput{Prompt: "You are the grooming agent."})
	if err != nil {
		t.Fatalf("RunProject() error = %v", err)
	}
	if outcome.ProjectID != f.project.ID {
		t.Fatalf("outcome.ProjectID = %q, want %q", outcome.ProjectID, f.project.ID)
	}
	if outcome.ExitCode != 0 || outcome.Output != "done" {
		t.Fatalf("outcome = %+v, want exit 0 and output %q", outcome, "done")
	}
	if !outcome.Complete {
		t.Fatalf("outcome.Complete = false, want true for a successful run")
	}
	if len(outcome.Checkouts) != 2 || outcome.Checkouts[0].Name != "backend" || outcome.Checkouts[1].Name != "web" {
		t.Fatalf("checkouts = %+v, want backend and web", outcome.Checkouts)
	}

	specs := f.backend.Prepared()
	if len(specs) != 1 {
		t.Fatalf("Prepare called %d times, want 1", len(specs))
	}
	if specs[0].Workdir != f.root {
		t.Fatalf("prepared Workdir = %q, want the workspace root %q", specs[0].Workdir, f.root)
	}
	cmds := f.backend.Commands()
	if len(cmds) != 1 {
		t.Fatalf("Exec called %d times, want 1", len(cmds))
	}
	if prompt := cmds[0].Argv[len(cmds[0].Argv)-1]; prompt != "You are the grooming agent." {
		t.Fatalf("prompt = %q, want the prompt verbatim", prompt)
	}
	if deleted := f.backend.Deleted(); len(deleted) != 1 {
		t.Fatalf("backend Delete called %d times, want 1 (cleanup)", len(deleted))
	}
}

// TestRunProjectWritesNoTaskProgress proves a task-less run records nothing in
// the task graph: it must not invent a task note or event.
func TestRunProjectWritesNoTaskProgress(t *testing.T) {
	f := newProjectRunFixture(t)
	f.backend.Program(isolation.ExecResult{Stdout: []byte("ok"), ExitCode: 0})

	if _, err := f.run(t, ProjectRunInput{Prompt: "prompt"}); err != nil {
		t.Fatalf("RunProject() error = %v", err)
	}
	events, err := f.svc.backend.Events().List(context.Background(), store.EventFilter{
		ProjectID: f.project.ID,
		Kinds:     []core.EventKind{core.EventTaskRunStarted, core.EventTaskRunFinished},
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("task-less run wrote %d run event(s): %+v", len(events), events)
	}
}

// TestRunProjectInteractiveRequestsTTY proves an interactive selection reaches
// the harness: the harness (the fake translates Interactive into Command.TTY)
// marks the command as needing a terminal, and a headless run does not.
func TestRunProjectInteractiveRequestsTTY(t *testing.T) {
	for _, interactive := range []bool{true, false} {
		t.Run(fmt.Sprintf("interactive=%v", interactive), func(t *testing.T) {
			f := newProjectRunFixture(t)
			f.backend.Program(isolation.ExecResult{Stdout: []byte("done"), ExitCode: 0})
			if _, err := f.run(t, ProjectRunInput{Prompt: "groom", Interactive: interactive}); err != nil {
				t.Fatalf("RunProject() error = %v", err)
			}
			cmds := f.backend.Commands()
			if len(cmds) != 1 {
				t.Fatalf("Exec called %d times, want 1", len(cmds))
			}
			if cmds[0].TTY != interactive {
				t.Fatalf("TTY = %v, want %v", cmds[0].TTY, interactive)
			}
		})
	}
}

func TestRunProjectFailureReportsExitCode(t *testing.T) {
	f := newProjectRunFixture(t)
	f.backend.Program(isolation.ExecResult{Stdout: []byte("boom"), ExitCode: 3})

	outcome, err := f.run(t, ProjectRunInput{Prompt: "prompt"})
	if !errors.Is(err, ErrRunFailed) {
		t.Fatalf("RunProject() error = %v, want ErrRunFailed", err)
	}
	if outcome == nil || outcome.ExitCode != 3 {
		t.Fatalf("outcome = %+v, want exit code 3", outcome)
	}
	if outcome.Complete {
		t.Fatalf("outcome.Complete = true, want false for a failed run")
	}
}

// TestRunProjectCapturesRequestedFiles proves a task-less run can read declared
// files back out of the environment before it is torn down: a session that
// writes its outputs inside the workspace has them requested by
// workspace-relative path. A requested path the session never wrote is omitted,
// so the caller decides how to report it.
func TestRunProjectCapturesRequestedFiles(t *testing.T) {
	f := newProjectRunFixture(t)
	f.backend.Program(isolation.ExecResult{Stdout: []byte("done"), ExitCode: 0})
	f.backend.Stage(isolation.File{Path: "out/report.md", Content: []byte("# report\n")})

	outcome, err := f.run(t, ProjectRunInput{
		Prompt:  "groom",
		Capture: []string{"out/report.md", "out/deferred.md"},
	})
	if err != nil {
		t.Fatalf("RunProject() error = %v", err)
	}
	if got := string(outcome.Captured["out/report.md"]); got != "# report\n" {
		t.Fatalf("captured report = %q, want the staged content", got)
	}
	if _, ok := outcome.Captured["out/deferred.md"]; ok {
		t.Fatalf("captured a path the session never wrote: %+v", outcome.Captured)
	}
}

func TestRunProjectRequiresBackendAndHarness(t *testing.T) {
	f := newProjectRunFixture(t)
	if _, err := f.svc.RunProject(context.Background(), ProjectRunInput{ProjectID: f.project.ID, WorkspaceRoot: f.root, Prompt: "p"}); err == nil {
		t.Fatal("RunProject() with no backend/harness error = nil, want error")
	}
}
