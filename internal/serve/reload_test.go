package serve

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/rank"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/jsonfile"
)

// The jsonfile store is a single document that the hub and a local CLI share.
// A long-running `ft serve` opens it once, so the hub must pick up a write
// another process makes to the same path; a snapshot taken at Open would let
// the dashboard show, and the capture write erase, state it never saw.
//
// Only a real second process proves it: goroutines in this package share one
// handle, whose in-process mutex would pass whether or not the store re-reads.
// The child re-executes this test binary and opens its own handle, the same
// pattern the store's own cross-process tests use.

const (
	jsonFileChildEnv   = "FACTOTUM_SERVE_JSONFILE_CHILD"
	jsonFileStoreEnv   = "FACTOTUM_SERVE_JSONFILE_STORE"
	jsonFileProjectEnv = "FACTOTUM_SERVE_JSONFILE_PROJECT"
	jsonFileTitleEnv   = "FACTOTUM_SERVE_JSONFILE_TITLE"
)

// TestJSONFileStoreChild is the second process the jsonfile serve tests start:
// it opens the store at the path the parent passes and adds one task through
// the same TicketService the CLI uses, so the write and its event look exactly
// like a local `ft` invocation. It skips in an ordinary run.
func TestJSONFileStoreChild(t *testing.T) {
	path := os.Getenv(jsonFileStoreEnv)
	if os.Getenv(jsonFileChildEnv) == "" || path == "" {
		t.Skip("only runs as the second process of the jsonfile serve tests")
	}
	ctx := context.Background()
	backend, err := jsonfile.Open(ctx, store.Config{
		Backend: "jsonfile",
		Options: map[string]string{"path": path},
	})
	if err != nil {
		t.Fatalf("jsonfile.Open() error = %v", err)
	}
	defer func() { _ = backend.Close() }()

	tasks := app.NewTicketService(backend, app.SystemClock{}, app.RandomIDGen{})
	task, err := tasks.Add(ctx, app.TicketInput{
		ProjectID: core.ProjectID(os.Getenv(jsonFileProjectEnv)),
		Title:     os.Getenv(jsonFileTitleEnv),
	})
	if err != nil {
		t.Fatalf("Add(%q) error = %v", os.Getenv(jsonFileTitleEnv), err)
	}
	if _, err := fmt.Fprintf(os.Stdout, "%s\n", task.ID); err != nil {
		t.Fatalf("print task id: %v", err)
	}
}

// jsonFileDashboard is a running dashboard over a jsonfile store, with its SSE
// stream already open so a caller can observe the hub's next push.
type jsonFileDashboard struct {
	path    string
	url     string
	project core.ProjectID
	events  <-chan string
}

// newJSONFileDashboard seeds one project and task through the handle the server
// will read, starts the dashboard on it, and subscribes to /events. The seeded
// task is written before the server starts, so it is part of the store the hub
// opened - the writes under test always come after.
func newJSONFileDashboard(t *testing.T) *jsonFileDashboard {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factotum.json")
	backend := openJSONFileStore(t, path)
	clock := app.SystemClock{}
	ids := app.RandomIDGen{}

	projects := app.NewProjectService(backend, clock, ids)
	tasks := app.NewTicketService(backend, clock, ids)
	project, err := projects.CreateWithID(ctx, "acme", "Acme", "", nil)
	if err != nil {
		t.Fatalf("CreateWithID() error = %v", err)
	}
	if _, err := tasks.Add(ctx, app.TicketInput{ProjectID: project.ID, Title: "Seeded before the hub started"}); err != nil {
		t.Fatalf("Add(seed) error = %v", err)
	}

	ranker, err := rank.Default()
	if err != nil {
		t.Fatalf("rank.Default() error = %v", err)
	}
	server, err := New(Options{
		Backend: backend,
		Clock:   clock,
		Ranker:  ranker,
		Project: project.ID,
		Token:   testToken,
		Tasks:   tasks,
		Assets:  testAssets(),
		Poll:    10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/events")
	if err != nil {
		t.Fatalf("Get(/events) error = %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	events := eventNames(resp.Body)
	// hello is written only after the broker has fixed its baseline, so the
	// stream is subscribed before the external write happens.
	expectEvent(t, events, "hello", 5*time.Second)

	return &jsonFileDashboard{path: path, url: ts.URL, project: project.ID, events: events}
}

func openJSONFileStore(t *testing.T, path string) store.Backend {
	t.Helper()
	backend, err := jsonfile.Open(context.Background(), store.Config{
		Backend: "jsonfile",
		Options: map[string]string{"path": path},
	})
	if err != nil {
		t.Fatalf("jsonfile.Open(%s) error = %v", path, err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	return backend
}

// writeFromAnotherProcess starts this test binary as a second process, adds a
// task through a handle it opened itself, and returns the id it wrote.
func writeFromAnotherProcess(t *testing.T, path string, project core.ProjectID, title string) core.TicketID {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestJSONFileStoreChild$", "-test.timeout=60s")
	cmd.Env = append(os.Environ(),
		jsonFileChildEnv+"=1",
		jsonFileStoreEnv+"="+path,
		jsonFileProjectEnv+"="+string(project),
		jsonFileTitleEnv+"="+title,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("second process write failed: %v\n%s", err, out)
	}
	id := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if id == "" {
		t.Fatalf("second process wrote no task id: %q", out)
	}
	return core.TicketID(id)
}

// TestServeSnapshotReadsAnotherProcessWrite proves a hub that opened the
// jsonfile document before a second process wrote serves that write: the
// dashboard snapshot lists it and its detail route resolves it by id.
func TestServeSnapshotReadsAnotherProcessWrite(t *testing.T) {
	dashboard := newJSONFileDashboard(t)
	id := writeFromAnotherProcess(t, dashboard.path, dashboard.project, "Written by the second process")

	snapshot := getBody(t, dashboard.url+"/api/snapshot")
	if !strings.Contains(snapshot, "Written by the second process") {
		t.Fatalf("/api/snapshot does not list the second process's task:\n%s", snapshot)
	}

	detail := getBody(t, dashboard.url+"/api/task/"+string(id))
	if !strings.Contains(detail, "Written by the second process") {
		t.Fatalf("/api/task/%s does not resolve the second process's task:\n%s", id, detail)
	}
}

// TestServeSSEReportsAnotherProcessWrite proves the live channel the app
// refetches on fires for a write made outside the hub, so an open dashboard
// updates without a manual reload.
func TestServeSSEReportsAnotherProcessWrite(t *testing.T) {
	dashboard := newJSONFileDashboard(t)
	writeFromAnotherProcess(t, dashboard.path, dashboard.project, "Written by the second process")
	expectEvent(t, dashboard.events, "update", 10*time.Second)
}

// TestServeCaptureKeepsAnotherProcessWrite proves the other half of sharing
// the document: the hub's own capture write must apply to what is on disk, not
// to the snapshot it opened, so it does not erase the second process's task.
func TestServeCaptureKeepsAnotherProcessWrite(t *testing.T) {
	dashboard := newJSONFileDashboard(t)
	external := writeFromAnotherProcess(t, dashboard.path, dashboard.project, "Written by the second process")

	resp := postCapture(t, dashboard.url+"/api/capture", testToken, `{"kind":"idea","text":"Captured while the hub was running"}`)
	if resp.StatusCode != http.StatusCreated {
		_ = resp.Body.Close()
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// A fresh handle reads the document the way the next `ft` invocation would.
	fresh := openJSONFileStore(t, dashboard.path)
	tickets, err := fresh.Tickets().List(context.Background(), store.TicketFilter{ProjectID: dashboard.project})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	titles := make(map[core.TicketID]string, len(tickets))
	for _, ticket := range tickets {
		titles[ticket.ID] = ticket.Title
	}
	if got := titles[external]; got != "Written by the second process" {
		t.Fatalf("the second process's task is gone after the hub wrote (task %s, title %q)", external, got)
	}
	found := false
	for _, title := range titles {
		if title == "Captured while the hub was running" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the hub's capture is not in the document: %+v", titles)
	}
}
