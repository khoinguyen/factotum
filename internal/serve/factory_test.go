package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
)

// factoryPipelineJSON is the slice of a /api/snapshot pipeline row the /factory
// view renders: the origin capture, the phase, the open gate, the produced
// tasks, and the exact command that clears the gate.
type factoryPipelineJSON struct {
	ID           string `json:"id"`
	Capture      string `json:"capture"`
	CaptureTitle string `json:"capture_title"`
	CaptureURL   string `json:"capture_url"`
	State        string `json:"state"`
	Gate         string `json:"gate"`
	Command      string `json:"command"`
	Produced     []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"produced"`
}

type factorySnapshotJSON struct {
	Pipelines []factoryPipelineJSON `json:"pipelines"`
}

// addPipeline stores a pipeline directly so a serve test can project it without
// driving the whole controller.
func (f *fixture) addPipeline(t *testing.T, projectID core.ProjectID, id core.PipelineID, capture core.TicketID, state core.PipelineState, gate core.GateKind) *core.Pipeline {
	t.Helper()
	pipeline := &core.Pipeline{
		ID:        id,
		ProjectID: projectID,
		CaptureID: capture,
		State:     state,
		Gate:      gate,
		CreatedAt: f.clock.Now(),
		UpdatedAt: f.clock.Now(),
	}
	if err := f.backend.Pipelines().Create(context.Background(), pipeline); err != nil {
		t.Fatalf("Create(%s) error = %v", id, err)
	}
	return pipeline
}

// TestSnapshotAPICarriesPipelines proves the dashboard snapshot surfaces each
// pipeline with its origin capture, phase, open gate, produced tasks, and the
// exact operator command that clears the gate (docs/up/design.md §7).
func TestSnapshotAPICarriesPipelines(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	idea := f.addIdea(t, project.ID, "Spark of a plan", "")
	promoted := f.promote(t, idea.ID)
	pipeline := f.addPipeline(t, project.ID, "pl-1", idea.ID, core.PipelineBuilding, core.GateMergeApproval)
	pipeline.Produced = []core.TicketID{promoted.ID}
	if err := f.backend.Pipelines().Update(context.Background(), pipeline); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	ts := newTestServer(t, f, Options{Project: project.ID})

	var doc factorySnapshotJSON
	getDoc(t, ts.URL+"/api/snapshot", &doc)

	if len(doc.Pipelines) != 1 {
		t.Fatalf("pipelines = %+v, want one", doc.Pipelines)
	}
	got := doc.Pipelines[0]
	if got.ID != "pl-1" || got.Capture != string(idea.ID) {
		t.Fatalf("pipeline identity = %+v, want pl-1 driving %s", got, idea.ID)
	}
	if got.CaptureTitle != idea.Title || got.CaptureURL != "/idea/"+string(idea.ID) {
		t.Fatalf("capture = %q/%q, want %q /idea/%s", got.CaptureTitle, got.CaptureURL, idea.Title, idea.ID)
	}
	if got.State != "building" || got.Gate != "merge_approval" {
		t.Fatalf("state/gate = %q/%q, want building/merge_approval", got.State, got.Gate)
	}
	if got.Command != "ft up merge pl-1" {
		t.Fatalf("command = %q, want the merge clearing command", got.Command)
	}
	if len(got.Produced) != 1 || got.Produced[0].ID != string(promoted.ID) || got.Produced[0].Title != promoted.Title {
		t.Fatalf("produced = %+v, want %s %q", got.Produced, promoted.ID, promoted.Title)
	}
}

// TestSnapshotPipelineCommandMatchesState pins the no-gate case: a pipeline
// advancing without a human gate has no clearing command.
func TestSnapshotPipelineCommandMatchesState(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	idea := f.addIdea(t, project.ID, "Spark", "")
	f.addPipeline(t, project.ID, "pl-1", idea.ID, core.PipelineGrooming, core.GateNone)
	ts := newTestServer(t, f, Options{Project: project.ID})

	var doc factorySnapshotJSON
	getDoc(t, ts.URL+"/api/snapshot", &doc)
	if len(doc.Pipelines) != 1 || doc.Pipelines[0].Command != "" || doc.Pipelines[0].Gate != "" {
		t.Fatalf("pipelines = %+v, want one ungated pipeline with no command", doc.Pipelines)
	}
}

// TestSnapshotPipelinesAreProjectScoped proves a scoped dashboard shows only its
// project's pipelines while an all-projects one shows every project's.
func TestSnapshotPipelinesAreProjectScoped(t *testing.T) {
	f := newFixture(t)
	one := f.addProject(t, "one", "One")
	two := f.addProject(t, "two", "Two")
	oneIdea := f.addIdea(t, one.ID, "One idea", "")
	twoIdea := f.addIdea(t, two.ID, "Two idea", "")
	f.addPipeline(t, one.ID, "pl-1", oneIdea.ID, core.PipelineGrooming, core.GateNone)
	f.addPipeline(t, two.ID, "pl-2", twoIdea.ID, core.PipelineReview, core.GateNone)

	scoped := newTestServer(t, f, Options{Project: one.ID})
	var scopedDoc factorySnapshotJSON
	getDoc(t, scoped.URL+"/api/snapshot", &scopedDoc)
	if len(scopedDoc.Pipelines) != 1 || scopedDoc.Pipelines[0].ID != "pl-1" {
		t.Fatalf("scoped pipelines = %+v, want only pl-1", scopedDoc.Pipelines)
	}

	all := newTestServer(t, f, Options{All: true})
	var allDoc factorySnapshotJSON
	getDoc(t, all.URL+"/api/snapshot", &allDoc)
	if len(allDoc.Pipelines) != 2 {
		t.Fatalf("all-projects pipelines = %+v, want both", allDoc.Pipelines)
	}
}

// TestSnapshotAPIPipelinesEmptyArray guards the SPA contract: with no pipelines
// the snapshot sends [] rather than null, so the /factory view can iterate it.
func TestSnapshotAPIPipelinesEmptyArray(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID})

	resp, err := http.Get(ts.URL + "/api/snapshot")
	if err != nil {
		t.Fatalf("Get(/api/snapshot) error = %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readBody(t, resp)), &doc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got := string(doc["pipelines"]); got != "[]" {
		t.Fatalf("pipelines = %s, want []", got)
	}
}

// TestFactoryClientRouteServesAppShell proves /factory is a read-side client
// route: the server answers it with the app shell and the SPA router resolves it.
func TestFactoryClientRouteServesAppShell(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Assets: testAssets()})

	resp, err := http.Get(ts.URL + "/factory")
	if err != nil {
		t.Fatalf("Get(/factory) error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Get(/factory) status = %d, want 200", resp.StatusCode)
	}
	if body := readBody(t, resp); !strings.Contains(body, `id="root"`) {
		t.Fatalf("/factory body is not the SPA index:\n%s", body)
	}
}

// TestPipelineProjectionIsLive proves a pipeline stored after the first read is
// in the next snapshot, so the app's SSE-driven refetch surfaces it immediately.
func TestPipelineProjectionIsLive(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	idea := f.addIdea(t, project.ID, "Spark", "")
	ts := newTestServer(t, f, Options{Project: project.ID})

	var before factorySnapshotJSON
	getDoc(t, ts.URL+"/api/snapshot", &before)
	if len(before.Pipelines) != 0 {
		t.Fatalf("pipelines before = %+v, want none", before.Pipelines)
	}
	f.addPipeline(t, project.ID, "pl-1", idea.ID, core.PipelineQueued, core.GateNone)
	var after factorySnapshotJSON
	getDoc(t, ts.URL+"/api/snapshot", &after)
	if len(after.Pipelines) != 1 || after.Pipelines[0].ID != "pl-1" {
		t.Fatalf("pipelines after = %+v, want the new pipeline", after.Pipelines)
	}
}
