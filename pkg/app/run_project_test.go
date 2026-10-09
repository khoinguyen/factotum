package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	harnesspkg "github.com/khoinguyen/factotum/pkg/harness"
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/memory"
	"github.com/khoinguyen/factotum/pkg/workspace"
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

// TestRunProjectCallsOnResolveWithPlanBeforeHarness pins that the resolve hook
// receives the materialized plan after resolution and before the backend is
// prepared, so a caller can advise on in-place checkouts before any work starts.
func TestRunProjectCallsOnResolveWithPlanBeforeHarness(t *testing.T) {
	f := newProjectRunFixture(t)
	f.backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})

	var plan *workspace.Plan
	calls := 0
	_, err := f.run(t, ProjectRunInput{
		Prompt: "p",
		OnResolve: func(p *workspace.Plan) {
			calls++
			if prepared := f.backend.Prepared(); len(prepared) != 0 {
				t.Errorf("OnResolve ran after Prepare; prepared = %+v", prepared)
			}
			plan = p
		},
	})
	if err != nil {
		t.Fatalf("RunProject() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("OnResolve called %d times, want 1", calls)
	}
	if plan == nil || len(plan.Checkouts) != 2 {
		t.Fatalf("OnResolve plan = %+v, want two checkouts", plan)
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

// TestRunProjectCarriesStoreEnvAndPinsProject proves a task-less run (a groom
// session) hands the harness the caller's resolved store and the project id, so
// the session's child ft reads that store instead of a project or user config it
// finds in the checkout.
func TestRunProjectCarriesStoreEnvAndPinsProject(t *testing.T) {
	f := newProjectRunFixture(t)
	f.backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})

	storeEnv := map[string]string{
		harnesspkg.EnvStore:     "jsonfile",
		harnesspkg.EnvStoreOpts: "path=/tmp/lab/db.json",
	}
	if _, err := f.run(t, ProjectRunInput{Prompt: "groom", StoreEnv: storeEnv}); err != nil {
		t.Fatalf("RunProject() error = %v", err)
	}
	env := f.backend.Prepared()[0].Env
	if env[harnesspkg.EnvProject] != string(f.project.ID) {
		t.Errorf("env %s = %q, want the run's project %q", harnesspkg.EnvProject, env[harnesspkg.EnvProject], f.project.ID)
	}
	if env[harnesspkg.EnvStore] != "jsonfile" {
		t.Errorf("env %s = %q, want the configured backend", harnesspkg.EnvStore, env[harnesspkg.EnvStore])
	}
	if env[harnesspkg.EnvStoreOpts] != "path=/tmp/lab/db.json" {
		t.Errorf("env %s = %q, want the configured options", harnesspkg.EnvStoreOpts, env[harnesspkg.EnvStoreOpts])
	}
}

// TestRunProjectCarriesHubReceiverEnv proves a task-less run with an actor and a
// complete hub installs the same receiver identity and hub transport as a task
// run, so a session launched by a task-less `ft run` can message peers over the
// configured hub instead of falling back to the local ft store.
func TestRunProjectCarriesHubReceiverEnv(t *testing.T) {
	f := newProjectRunFixture(t)
	f.backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})

	actor := core.ActorID("act-agent")
	if _, err := f.run(t, ProjectRunInput{
		Prompt:     "groom",
		Actor:      &actor,
		MsgURL:     "http://hub:8484",
		ServeToken: "tok",
	}); err != nil {
		t.Fatalf("RunProject() error = %v", err)
	}
	env := f.backend.Prepared()[0].Env
	if env[harnesspkg.EnvActor] != string(actor) {
		t.Errorf("env %s = %q, want the run actor %q", harnesspkg.EnvActor, env[harnesspkg.EnvActor], actor)
	}
	if env[harnesspkg.EnvMsgURL] != "http://hub:8484" {
		t.Errorf("env %s = %q, want the configured hub URL", harnesspkg.EnvMsgURL, env[harnesspkg.EnvMsgURL])
	}
	if env[harnesspkg.EnvServeToken] != "tok" {
		t.Errorf("env %s = %q, want the configured serve token", harnesspkg.EnvServeToken, env[harnesspkg.EnvServeToken])
	}
}

// TestRunProjectWithoutActorInstallsNoReceiver proves a task-less run that names
// no actor installs no receiver and no hub env even when a hub is configured: an
// actor is required to register a receiver, and the run still pins its project.
func TestRunProjectWithoutActorInstallsNoReceiver(t *testing.T) {
	f := newProjectRunFixture(t)
	f.backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})

	if _, err := f.run(t, ProjectRunInput{
		Prompt:     "groom",
		MsgURL:     "http://hub:8484",
		ServeToken: "tok",
	}); err != nil {
		t.Fatalf("RunProject() error = %v", err)
	}
	env := f.backend.Prepared()[0].Env
	if harnesspkg.MessagingEnabled(env) {
		t.Fatalf("env %v enables messaging, want disabled without an actor", env)
	}
	if env[harnesspkg.EnvProject] != string(f.project.ID) {
		t.Errorf("env %s = %q, want the run's project pinned", harnesspkg.EnvProject, env[harnesspkg.EnvProject])
	}
}

func TestRunProjectRequiresBackendAndHarness(t *testing.T) {
	f := newProjectRunFixture(t)
	if _, err := f.svc.RunProject(context.Background(), ProjectRunInput{ProjectID: f.project.ID, WorkspaceRoot: f.root, Prompt: "p"}); err == nil {
		t.Fatal("RunProject() with no backend/harness error = nil, want error")
	}
}
