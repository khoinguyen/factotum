package up

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/internal/groom"
	"github.com/khoinguyen/factotum/internal/serve"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

const integrationToken = "s3cret"

// TestServeCaptureStartsPipelineAndGrooms is the serve->controller integration
// test: a capture POSTed to the real serve write path is stored, the real
// controller enqueues a pipeline for it, and a supervisor pass runs the groom
// stage, which writes a real session manifest and records the produced task.
// Only the agent invocation is a stub (SessionFunc); serve, the controller, the
// store, and the session manifest are real.
func TestServeCaptureStartsPipelineAndGrooms(t *testing.T) {
	dataDir := t.TempDir()
	f := newFixture(t, nil)
	ctx := context.Background()

	// The groom session function is the one test double: it fabricates the work a
	// real groom session would do, and writes the real manifest.
	session := func(ctx context.Context, p *core.Pipeline, sessionID string) (SessionResult, error) {
		task, err := f.tasks.Add(ctx, app.TicketInput{
			ProjectID: p.ProjectID,
			Kind:      core.KindTask,
			Title:     "produced from the capture",
		})
		if err != nil {
			return SessionResult{}, err
		}
		if _, err := f.tasks.AddDep(ctx, task.ID, p.CaptureID); err != nil {
			return SessionResult{}, err
		}
		record := groom.SessionRecord{
			ID:        sessionID,
			Project:   string(p.ProjectID),
			Mode:      "unattended",
			CreatedAt: f.clock.Now(),
			Scope:     []groom.ScopeItem{{ID: string(p.CaptureID), Kind: string(core.KindIdea), Title: "capture"}},
			Produced:  []string{string(task.ID)},
		}
		if err := groom.WriteSession(dataDir, record); err != nil {
			return SessionResult{}, err
		}
		return SessionResult{Produced: []core.TicketID{task.ID}, Detail: "groomed"}, nil
	}
	f.controller = mustController(t, f, map[core.PipelineState]StageRunner{
		core.PipelineGrooming: &GroomRunner{DataDir: dataDir, IDs: f.ids, Session: session},
	})

	server, err := serve.New(serve.Options{
		Backend:    f.backend,
		Clock:      f.clock,
		Project:    f.project.ID,
		Token:      integrationToken,
		Tasks:      f.tasks,
		AutoGroom:  true,
		Controller: f.controller,
	})
	if err != nil {
		t.Fatalf("serve.New() error = %v", err)
	}
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	body := strings.NewReader(`{"kind":"idea","text":"Add a dark mode toggle to the dashboard"}`)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/capture", body)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+integrationToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/capture status = %d, want 201", resp.StatusCode)
	}
	var result struct {
		ID core.TicketID `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	// The capture write enqueued a pipeline synchronously.
	pipelines, err := f.controller.Pipelines(ctx)
	if err != nil {
		t.Fatalf("Pipelines() error = %v", err)
	}
	if len(pipelines) != 1 {
		t.Fatalf("pipelines after capture = %d, want 1", len(pipelines))
	}
	pipeline := pipelines[0]
	if pipeline.CaptureID != result.ID {
		t.Fatalf("pipeline capture = %s, want %s", pipeline.CaptureID, result.ID)
	}

	// A supervisor pass drives the groom stage.
	if err := f.controller.Step(ctx); err != nil {
		t.Fatalf("Step() error = %v", err)
	}

	grown := f.pipeline(t, pipeline.ID)
	if grown.State != core.PipelineReview {
		t.Fatalf("pipeline state = %q, want review", grown.State)
	}
	if grown.Session == "" {
		t.Fatal("pipeline session is empty; the groom stage did not record one")
	}
	if _, err := os.Stat(groom.SessionManifestPath(dataDir, grown.Session)); err != nil {
		t.Fatalf("groom session manifest missing: %v", err)
	}
	if len(grown.Produced) != 1 {
		t.Fatalf("pipeline produced = %v, want one task", grown.Produced)
	}
	produced, err := f.tasks.Get(ctx, grown.Produced[0])
	if err != nil {
		t.Fatalf("Get(produced) error = %v", err)
	}
	if !dependsOn(produced, result.ID) {
		t.Fatalf("produced task deps = %v, want the capture origin edge", produced.Deps)
	}

	// The pipeline also surfaces the whole trace as events.
	assertEventKinds(t, f, core.EventPipelineQueued, core.EventPipelineStageStarted, core.EventPipelineStageFinished)
}

func dependsOn(task *core.Ticket, id core.TicketID) bool {
	for _, dep := range task.Deps {
		if dep == id {
			return true
		}
	}
	return false
}

func mustController(t *testing.T, f *fixture, stages map[core.PipelineState]StageRunner) *Controller {
	t.Helper()
	controller, err := New(Options{
		Backend: f.backend,
		Clock:   f.clock,
		IDs:     f.ids,
		Project: f.project.ID,
		Stages:  stages,
		Logf:    func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("New(controller) error = %v", err)
	}
	return controller
}

// TestGroomRunnerReusesRecordedSession proves the runner's idempotency: a
// session already recorded for the capture is reused, so a crash between the
// manifest write and the pipeline save does not groom twice.
func TestGroomRunnerReusesRecordedSession(t *testing.T) {
	dataDir := t.TempDir()
	ids := &seqIDs{}
	capture := core.TicketID("i-1")
	if err := groom.WriteSession(dataDir, groom.SessionRecord{
		ID:        "groom-existing",
		Project:   "factotum",
		CreatedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		Scope:     []groom.ScopeItem{{ID: string(capture), Kind: "idea", Title: "capture"}},
		Produced:  []string{"t-7"},
	}); err != nil {
		t.Fatalf("WriteSession() error = %v", err)
	}

	called := false
	runner := &GroomRunner{
		DataDir: dataDir,
		IDs:     ids,
		Session: func(context.Context, *core.Pipeline, string) (SessionResult, error) {
			called = true
			return SessionResult{}, nil
		},
	}
	result, err := runner.Run(context.Background(), &core.Pipeline{CaptureID: capture, ProjectID: "factotum"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if called {
		t.Fatal("Run() dispatched a new session instead of reusing the recorded one")
	}
	if result.Session != "groom-existing" || len(result.Produced) != 1 || result.Produced[0] != "t-7" {
		t.Fatalf("Run() = %+v, want the recorded session and its produced task", result)
	}
}
