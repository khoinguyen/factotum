package cli

import (
	"context"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/internal/config"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
	"github.com/khoinguyen/factotum/pkg/store"
)

func TestUpStartStatusAndList(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "a spark", "-b", "context"))

	started := r.run("up", "start", ideaID, "-p", projectID)
	pipelineID := firstField(t, started)
	if pipelineID == "" || pipelineID == "(none)" {
		t.Fatalf("up start should enqueue a pipeline:\n%s", started)
	}
	if !strings.Contains(started, "state: queued") {
		t.Fatalf("up start should report state: queued:\n%s", started)
	}

	status := r.run("up", "status", "-p", projectID)
	for _, want := range []string{
		"pipeline: " + pipelineID,
		"capture: " + ideaID,
		"state: queued",
		"gate: -",
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("up status missing %q:\n%s", want, status)
		}
	}

	list := r.run("up", "list", "-p", projectID)
	if !strings.Contains(list, pipelineID) || !strings.Contains(list, "PIPELINE") {
		t.Fatalf("up list should table the pipeline:\n%s", list)
	}
}

func TestUpPipelineDocCarriesError(t *testing.T) {
	doc := upPipelineDocOf(&core.Pipeline{
		ID:        "pl-1",
		CaptureID: "i-1",
		ProjectID: "acme",
		State:     core.PipelineFailed,
		Error:     "phase executor exploded",
	})
	if doc.Error != "phase executor exploded" {
		t.Fatalf("doc.Error = %q, want the pipeline's failure reason", doc.Error)
	}
}

// repoRoot resolves this checkout's root from the test file's own path, so the
// grooming prompt (a repo-relative path) resolves after t.Chdir.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// upGroomBackend is the fake isolation backend the up groom session runs
// against. On Exec it stages the five groom outputs at the paths the injected
// kickoff names, so the real session capture path runs.
func upGroomBackend(t *testing.T, deferred string) groomBackend {
	t.Helper()
	base := isofake.New("sandbox")
	base.Program(isolation.ExecResult{Stdout: []byte("groomed\n"), ExitCode: 0})
	return groomBackend{Backend: base, onExec: func(cmd isolation.Command) {
		prompt := cmd.Argv[len(cmd.Argv)-1]
		docs := defaultGroomDocs()
		if deferred != "" {
			docs.deferred = deferred
		}
		stageGroomOutputs(t, base, prompt, docs)
	}}
}

func openTestStore(t *testing.T, deps *Deps, st config.Store) store.Backend {
	t.Helper()
	factory, err := deps.StoreFactories.MustLookup(st.Backend)
	if err != nil {
		t.Fatalf("lookup store %s: %v", st.Backend, err)
	}
	backend, err := factory(context.Background(), store.Config{Backend: st.Backend, Options: st.Options})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	return backend
}

// TestUpGroomSessionAttachesOnlyWhenInteractive pins the PO attach path: the
// pipeline's groom session requests a terminal (the harness TUI) exactly when a
// PO is attached (interactive true), and otherwise runs headless-unattended,
// deferring the capture's product questions so the advisory groom_questions gate
// opens (design §3/§8/§11). A headless daemon never waits on a PO who is not
// there, so the fallback is always unattended.
func TestUpGroomSessionAttachesOnlyWhenInteractive(t *testing.T) {
	t.Chdir(repoRoot(t))
	cases := []struct {
		name         string
		interactive  bool
		wantTTY      bool
		wantDeferred bool
	}{
		{"attached PO requests a TTY", true, true, false},
		{"no PO stays headless and defers", false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(t)
			projectID, _ := tasklessContext(t, r)
			ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "spark"))

			deferred := ""
			if tc.wantDeferred {
				deferred = "# Deferred questions\n\n## Questions\n\n- [ ] " + ideaID + ": which entry point?\n\n## Resolved\n"
			}
			backend := upGroomBackend(t, deferred)
			r.runBackend = backend
			r.runHarness = harnessfake.New("opencode")

			deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, io.Discard, io.Discard, hermeticGetenv)
			r.setup(deps)

			st := config.Store{Backend: "jsonfile", Options: map[string]string{"path": r.path}}
			be := openTestStore(t, deps, st)
			target := groomTarget{
				store:     st,
				dataDir:   filepath.Dir(r.path),
				backend:   be,
				projects:  app.NewProjectService(be, deps.Clock, deps.IDs),
				tasks:     app.NewTicketService(be, deps.Clock, deps.IDs),
				actors:    app.NewActorService(be, deps.Clock, deps.IDs),
				artifacts: app.NewArtifactService(be, deps.Clock, deps.IDs),
				close:     func() {},
			}
			sel := &runSelection{
				backend:     backend,
				backendName: "fake",
				harness:     harnessfake.New("opencode"),
				workspace:   t.TempDir(),
				repoBase:    t.TempDir(),
			}
			session := deps.upGroomSession(&cobra.Command{}, target, sel, tc.interactive)
			result, err := session(context.Background(), &core.Pipeline{
				ProjectID: core.ProjectID(projectID),
				CaptureID: core.TicketID(ideaID),
			}, "groom-test")
			if err != nil {
				t.Fatalf("up groom session error = %v", err)
			}
			cmds := backend.Commands()
			if len(cmds) != 1 {
				t.Fatalf("Exec called %d times, want 1", len(cmds))
			}
			if cmds[0].TTY != tc.wantTTY {
				t.Fatalf("TTY = %v, want %v", cmds[0].TTY, tc.wantTTY)
			}
			if result.Deferred != tc.wantDeferred {
				t.Fatalf("Deferred = %v, want %v", result.Deferred, tc.wantDeferred)
			}
		})
	}
}

func TestUpStartSkipsExecutableTicket(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "real work"))

	out := r.run("up", "start", taskID, "-p", projectID)
	if !strings.Contains(out, "pipeline: (none)") {
		t.Fatalf("up start on an executable ticket should skip:\n%s", out)
	}
}
