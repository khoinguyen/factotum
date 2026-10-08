package conformance

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// pipelineBase is a fixed, well-in-the-past instant so ordering and claims are
// deterministic.
var pipelineBase = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func testPipeline(t *testing.T, be store.Backend) {
	t.Helper()
	ctx := context.Background()
	repo := be.Pipelines()

	t.Run("roundtrip", func(t *testing.T) { testPipelineRoundtrip(t, repo, ctx) })
	t.Run("validate", func(t *testing.T) { testPipelineValidate(t, repo, ctx) })
	t.Run("list", func(t *testing.T) { testPipelineList(t, repo, ctx) })
	t.Run("claim", func(t *testing.T) { testPipelineClaim(t, repo, ctx) })
	t.Run("claim-concurrency", func(t *testing.T) { testPipelineClaimConcurrency(t, repo, ctx) })
	t.Run("terminal-immutability", func(t *testing.T) { testPipelineTerminalImmutability(t, repo, ctx) })
}

func testPipelineRoundtrip(t *testing.T, repo store.PipelineRepo, ctx context.Context) {
	t.Helper()
	const project = core.ProjectID("prj-rt")
	capture := core.TicketID("i-rt")
	milestone := core.TicketID("m-rt")
	pipeline := &core.Pipeline{
		ID:        "pl-rt",
		ProjectID: project,
		CaptureID: capture,
		Milestone: &milestone,
		State:     core.PipelineQueued,
		Gate:      core.GateNone,
		CreatedAt: pipelineBase,
		UpdatedAt: pipelineBase,
	}
	if err := repo.Create(ctx, pipeline); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := repo.Create(ctx, pipeline); !errors.Is(err, core.ErrAlreadyExists) {
		t.Fatalf("Create() duplicate error = %v, want ErrAlreadyExists", err)
	}
	if _, err := repo.Get(ctx, "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Get() missing error = %v, want ErrNotFound", err)
	}

	got, err := repo.Get(ctx, "pl-rt")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.CaptureID != capture || got.ProjectID != project || got.State != core.PipelineQueued {
		t.Fatalf("Get() = %+v, want capture/project/state preserved", got)
	}
	if got.Milestone == nil || *got.Milestone != milestone {
		t.Fatalf("Get().Milestone = %v, want %q", got.Milestone, milestone)
	}

	// A working update carries the produced tasks, session, and gate.
	got.State = core.PipelineBuilding
	got.Gate = core.GateMergeApproval
	got.Session = "sess-1"
	got.Produced = []core.TicketID{"t-1", "t-2"}
	got.Attempts = 2
	got.UpdatedAt = pipelineBase.Add(time.Minute)
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	reloaded, err := repo.Get(ctx, "pl-rt")
	if err != nil {
		t.Fatalf("Get() after update error = %v", err)
	}
	if reloaded.State != core.PipelineBuilding || reloaded.Gate != core.GateMergeApproval {
		t.Fatalf("Get() after update = %+v, want building/merge_approval", reloaded)
	}
	if reloaded.Session != "sess-1" || len(reloaded.Produced) != 2 || reloaded.Produced[1] != "t-2" {
		t.Fatalf("Get() after update = %+v, want session and produced preserved", reloaded)
	}
	if reloaded.Attempts != 2 {
		t.Fatalf("Get().Attempts = %d, want 2", reloaded.Attempts)
	}

	missing := &core.Pipeline{ID: "pl-missing", ProjectID: project, CaptureID: "i-x", State: core.PipelineQueued}
	if err := repo.Update(ctx, missing); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Update() missing error = %v, want ErrNotFound", err)
	}
}

func testPipelineValidate(t *testing.T, repo store.PipelineRepo, ctx context.Context) {
	t.Helper()
	const project = core.ProjectID("prj-v")
	capture := core.TicketID("i-v")
	invalids := []struct {
		name     string
		pipeline core.Pipeline
	}{
		{"empty id", core.Pipeline{ProjectID: project, CaptureID: capture, State: core.PipelineQueued}},
		{"empty project", core.Pipeline{ID: "pl", CaptureID: capture, State: core.PipelineQueued}},
		{"empty capture", core.Pipeline{ID: "pl", ProjectID: project, State: core.PipelineQueued}},
		{"unknown state", core.Pipeline{ID: "pl", ProjectID: project, CaptureID: capture, State: "lost"}},
		{"unknown gate", core.Pipeline{ID: "pl", ProjectID: project, CaptureID: capture, State: core.PipelineQueued, Gate: "bad"}},
	}
	for _, tt := range invalids {
		t.Run(tt.name, func(t *testing.T) {
			pipeline := tt.pipeline
			if err := repo.Create(ctx, &pipeline); !errors.Is(err, core.ErrInvalid) {
				t.Fatalf("Create() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func testPipelineList(t *testing.T, repo store.PipelineRepo, ctx context.Context) {
	t.Helper()
	const project = core.ProjectID("prj-list")
	const other = core.ProjectID("prj-list-other")
	captureA := core.TicketID("i-a")
	captureB := core.TicketID("i-b")

	pipelines := []*core.Pipeline{
		pipelineRecord("pl-1", project, captureA, core.PipelineQueued, core.GateNone),
		pipelineRecord("pl-2", project, captureB, core.PipelineBuilding, core.GateMergeApproval),
		pipelineRecord("pl-3", project, "i-c", core.PipelineFailed, core.GateDesignRework),
		pipelineRecord("pl-4", other, "i-d", core.PipelineQueued, core.GateNone),
	}
	pipelines[1].CreatedAt = pipelineBase.Add(time.Second)
	pipelines[2].CreatedAt = pipelineBase.Add(2 * time.Second)
	pipelines[3].CreatedAt = pipelineBase.Add(3 * time.Second)
	for _, pipeline := range pipelines {
		if err := repo.Create(ctx, pipeline); err != nil {
			t.Fatalf("Create(%s) error = %v", pipeline.ID, err)
		}
	}

	assertPipelineIDs(t, repo, ctx, store.PipelineFilter{ProjectID: project}, []core.PipelineID{"pl-1", "pl-2", "pl-3"})
	assertPipelineIDs(t, repo, ctx, store.PipelineFilter{ProjectID: project, States: []core.PipelineState{core.PipelineQueued}}, []core.PipelineID{"pl-1"})
	assertPipelineIDs(t, repo, ctx, store.PipelineFilter{ProjectID: project, Gates: []core.GateKind{core.GateMergeApproval}}, []core.PipelineID{"pl-2"})
	assertPipelineIDs(t, repo, ctx, store.PipelineFilter{ProjectID: project, CaptureID: &captureB}, []core.PipelineID{"pl-2"})
	assertPipelineIDs(t, repo, ctx, store.PipelineFilter{ProjectID: project, Limit: 2}, []core.PipelineID{"pl-1", "pl-2"})
	assertPipelineIDs(t, repo, ctx, store.PipelineFilter{ProjectID: other}, []core.PipelineID{"pl-4"})
	assertPipelineIDs(t, repo, ctx, store.PipelineFilter{ProjectID: project, States: []core.PipelineState{core.PipelineDone}}, nil)
}

func pipelineRecord(id core.PipelineID, project core.ProjectID, capture core.TicketID, state core.PipelineState, gate core.GateKind) *core.Pipeline {
	return &core.Pipeline{
		ID:        id,
		ProjectID: project,
		CaptureID: capture,
		State:     state,
		Gate:      gate,
		CreatedAt: pipelineBase,
		UpdatedAt: pipelineBase,
	}
}

func assertPipelineIDs(t *testing.T, repo store.PipelineRepo, ctx context.Context, filter store.PipelineFilter, want []core.PipelineID) {
	t.Helper()
	pipelines, err := repo.List(ctx, filter)
	if err != nil {
		t.Fatalf("List(%+v) error = %v", filter, err)
	}
	got := make([]core.PipelineID, 0, len(pipelines))
	for _, pipeline := range pipelines {
		got = append(got, pipeline.ID)
	}
	if len(got) != len(want) {
		t.Fatalf("List(%+v) = %v, want %v", filter, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("List(%+v) = %v, want %v", filter, got, want)
		}
	}
}

func testPipelineClaim(t *testing.T, repo store.PipelineRepo, ctx context.Context) {
	t.Helper()
	const project = core.ProjectID("prj-claim")
	const other = core.ProjectID("prj-claim-other")

	late := pipelineRecord("c-late", project, "i-late", core.PipelineQueued, core.GateNone)
	late.CreatedAt = pipelineBase.Add(time.Second)
	early := pipelineRecord("c-early", project, "i-early", core.PipelineQueued, core.GateNone)
	working := pipelineRecord("c-working", project, "i-working", core.PipelineBuilding, core.GateNone)
	foreign := pipelineRecord("c-foreign", other, "i-foreign", core.PipelineQueued, core.GateNone)
	for _, pipeline := range []*core.Pipeline{late, early, working, foreign} {
		if err := repo.Create(ctx, pipeline); err != nil {
			t.Fatalf("Create(%s) error = %v", pipeline.ID, err)
		}
	}

	// The oldest queued pipeline for the project is claimed and moved into its
	// first working state.
	claimed, err := repo.Claim(ctx, store.PipelineClaimRequest{ProjectID: project})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claimed.ID != "c-early" {
		t.Fatalf("Claim() = %s, want oldest c-early", claimed.ID)
	}
	if claimed.State != core.PipelineGrooming {
		t.Fatalf("Claim() state = %q, want grooming", claimed.State)
	}
	stored, err := repo.Get(ctx, "c-early")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.State != core.PipelineGrooming {
		t.Fatalf("stored state after claim = %q, want grooming", stored.State)
	}

	// The next claim takes the next queued one, skipping the in-flight pipeline.
	second, err := repo.Claim(ctx, store.PipelineClaimRequest{ProjectID: project})
	if err != nil {
		t.Fatalf("second Claim() error = %v", err)
	}
	if second.ID != "c-late" {
		t.Fatalf("second Claim() = %s, want c-late", second.ID)
	}

	// Nothing queued remains for the project; a foreign project's queue is
	// untouched and still claimable.
	if _, err := repo.Claim(ctx, store.PipelineClaimRequest{ProjectID: project}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Claim(empty) error = %v, want ErrNotFound", err)
	}
	foreignClaim, err := repo.Claim(ctx, store.PipelineClaimRequest{ProjectID: other})
	if err != nil {
		t.Fatalf("foreign Claim() error = %v", err)
	}
	if foreignClaim.ID != "c-foreign" {
		t.Fatalf("foreign Claim() = %s, want c-foreign", foreignClaim.ID)
	}
}

func testPipelineClaimConcurrency(t *testing.T, repo store.PipelineRepo, ctx context.Context) {
	t.Helper()
	const project = core.ProjectID("prj-race")
	if err := repo.Create(ctx, pipelineRecord("race-1", project, "i-race", core.PipelineQueued, core.GateNone)); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	const workers = 8
	var (
		start   sync.WaitGroup
		done    sync.WaitGroup
		mu      sync.Mutex
		winners int
		errs    []error
	)
	start.Add(1)
	done.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer done.Done()
			start.Wait()
			claimed, err := repo.Claim(ctx, store.PipelineClaimRequest{ProjectID: project})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && claimed != nil:
				winners++
			case errors.Is(err, core.ErrNotFound):
			default:
				errs = append(errs, err)
			}
		}()
	}
	start.Done()
	done.Wait()

	if len(errs) > 0 {
		t.Fatalf("concurrent Claim() errors = %v", errs)
	}
	if winners != 1 {
		t.Fatalf("concurrent claims winners = %d, want exactly 1", winners)
	}
}

func testPipelineTerminalImmutability(t *testing.T, repo store.PipelineRepo, ctx context.Context) {
	t.Helper()
	const project = core.ProjectID("prj-terminal")

	// done and cancelled are immutable: any update is rejected.
	for _, state := range []core.PipelineState{core.PipelineDone, core.PipelineCancelled} {
		id := core.PipelineID("pl-" + string(state))
		if err := repo.Create(ctx, pipelineRecord(id, project, core.TicketID("i-"+string(state)), state, core.GateNone)); err != nil {
			t.Fatalf("Create(%s) error = %v", id, err)
		}
		target, err := repo.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get(%s) error = %v", id, err)
		}
		target.Error = "changed"
		if err := repo.Update(ctx, target); !errors.Is(err, core.ErrConflict) {
			t.Fatalf("Update(%s) error = %v, want ErrConflict", state, err)
		}
	}

	// failed and paused are reversible: retry/resume must be able to move them.
	for _, state := range []core.PipelineState{core.PipelineFailed, core.PipelinePaused} {
		id := core.PipelineID("pl-" + string(state))
		if err := repo.Create(ctx, pipelineRecord(id, project, core.TicketID("i-"+string(state)), state, core.GateNone)); err != nil {
			t.Fatalf("Create(%s) error = %v", id, err)
		}
		target, err := repo.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get(%s) error = %v", id, err)
		}
		target.State = core.PipelineQueued
		if err := repo.Update(ctx, target); err != nil {
			t.Fatalf("Update(%s) error = %v, want nil", state, err)
		}
	}
}
