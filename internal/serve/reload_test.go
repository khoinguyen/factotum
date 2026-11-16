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
	"github.com/khoinguyen/factotum/pkg/store/jsondir"
	"github.com/khoinguyen/factotum/pkg/store/jsonfile"
)

// A long-running `ft serve` hub opens a file store once while a local `ft`
// invocation writes to the same path. The hub must pick up a write another
// process makes; a snapshot taken at Open would let the dashboard show, and the
// capture write erase, state it never saw. Both file backends share the path
// with the CLI and both must satisfy this, though they detect the change
// differently: jsonfile re-reads its document on every operation, jsondir
// re-reads its directory when the store's generation changed.
//
// Only a real second process proves it: goroutines in this package share one
// handle, whose in-process mutex would pass whether or not the store re-reads.
// The child re-executes this test binary and opens its own handle, the same
// pattern the store's own cross-process tests use.

const (
	reloadChildEnv   = "FACTOTUM_SERVE_RELOAD_CHILD"
	reloadStoreEnv   = "FACTOTUM_SERVE_RELOAD_STORE"
	reloadBackendEnv = "FACTOTUM_SERVE_RELOAD_BACKEND"
	reloadProjectEnv = "FACTOTUM_SERVE_RELOAD_PROJECT"
	reloadTitleEnv   = "FACTOTUM_SERVE_RELOAD_TITLE"
)

// reloadBackends are the file stores a hub can be pointed at. Both take a store
// path: a document for jsonfile, a directory for jsondir.
var reloadBackends = []string{"jsonfile", "jsondir"}

func openReloadStore(t *testing.T, backend, path string) store.Backend {
	t.Helper()
	var (
		be  store.Backend
		err error
	)
	cfg := store.Config{Backend: backend, Options: map[string]string{"path": path}}
	switch backend {
	case "jsonfile":
		be, err = jsonfile.Open(context.Background(), cfg)
	case "jsondir":
		be, err = jsondir.Open(context.Background(), cfg)
	default:
		t.Fatalf("unknown backend %q", backend)
	}
	if err != nil {
		t.Fatalf("%s.Open(%s) error = %v", backend, path, err)
	}
	t.Cleanup(func() { _ = be.Close() })
	return be
}

// TestReloadStoreChild is the second process the reload tests start: it opens
// the store at the path and backend the parent passes and adds one task through
// the same TicketService the CLI uses, so the write and its event look exactly
// like a local `ft` invocation. It skips in an ordinary run.
func TestReloadStoreChild(t *testing.T) {
	path := os.Getenv(reloadStoreEnv)
	backend := os.Getenv(reloadBackendEnv)
	if os.Getenv(reloadChildEnv) == "" || path == "" {
		t.Skip("only runs as the second process of the reload tests")
	}
	ctx := context.Background()
	store := openReloadStore(t, backend, path)
	tasks := app.NewTicketService(store, app.SystemClock{}, app.RandomIDGen{})
	task, err := tasks.Add(ctx, app.TicketInput{
		ProjectID: core.ProjectID(os.Getenv(reloadProjectEnv)),
		Title:     os.Getenv(reloadTitleEnv),
	})
	if err != nil {
		t.Fatalf("Add(%q) error = %v", os.Getenv(reloadTitleEnv), err)
	}
	if _, err := fmt.Fprintf(os.Stdout, "%s\n", task.ID); err != nil {
		t.Fatalf("print task id: %v", err)
	}
}

// reloadDashboard is a running dashboard over a file store, with its SSE stream
// already open so a caller can observe the hub's next push.
type reloadDashboard struct {
	backend string
	path    string
	url     string
	project core.ProjectID
	events  <-chan string
}

// newReloadDashboard seeds one project and task through the handle the server
// will read, starts the dashboard on it, and subscribes to /events. The seeded
// task is written before the server starts, so it is part of the store the hub
// opened - the writes under test always come after.
func newReloadDashboard(t *testing.T, backend string) *reloadDashboard {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store")
	store := openReloadStore(t, backend, path)
	clock := app.SystemClock{}
	ids := app.RandomIDGen{}

	projects := app.NewProjectService(store, clock, ids)
	tasks := app.NewTicketService(store, clock, ids)
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
		Backend: store,
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

	return &reloadDashboard{backend: backend, path: path, url: ts.URL, project: project.ID, events: events}
}

// writeFromAnotherProcess starts this test binary as a second process, adds a
// task through a handle it opened itself, and returns the id it wrote.
func writeFromAnotherProcess(t *testing.T, dashboard *reloadDashboard, title string) core.TicketID {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestReloadStoreChild$", "-test.timeout=60s")
	cmd.Env = append(os.Environ(),
		reloadChildEnv+"=1",
		reloadBackendEnv+"="+dashboard.backend,
		reloadStoreEnv+"="+dashboard.path,
		reloadProjectEnv+"="+string(dashboard.project),
		reloadTitleEnv+"="+title,
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

// TestServeSnapshotReadsAnotherProcessWrite proves a hub that opened the store
// before a second process wrote serves that write: the dashboard snapshot lists
// it and its detail route resolves it by id.
func TestServeSnapshotReadsAnotherProcessWrite(t *testing.T) {
	for _, backend := range reloadBackends {
		t.Run(backend, func(t *testing.T) {
			dashboard := newReloadDashboard(t, backend)
			id := writeFromAnotherProcess(t, dashboard, "Written by the second process")

			snapshot := getBody(t, dashboard.url+"/api/snapshot")
			if !strings.Contains(snapshot, "Written by the second process") {
				t.Fatalf("/api/snapshot does not list the second process's task:\n%s", snapshot)
			}

			detail := getBody(t, dashboard.url+"/api/task/"+string(id))
			if !strings.Contains(detail, "Written by the second process") {
				t.Fatalf("/api/task/%s does not resolve the second process's task:\n%s", id, detail)
			}
		})
	}
}

// TestServeSSEReportsAnotherProcessWrite proves the live channel the app
// refetches on fires for a write made outside the hub, so an open dashboard
// updates without a manual reload.
func TestServeSSEReportsAnotherProcessWrite(t *testing.T) {
	for _, backend := range reloadBackends {
		t.Run(backend, func(t *testing.T) {
			dashboard := newReloadDashboard(t, backend)
			writeFromAnotherProcess(t, dashboard, "Written by the second process")
			expectEvent(t, dashboard.events, "update", 10*time.Second)
		})
	}
}

// TestServeCaptureKeepsAnotherProcessWrite proves the other half of sharing the
// store: the hub's own capture write must apply to what is on disk, not to the
// snapshot it opened, so it does not erase the second process's task.
func TestServeCaptureKeepsAnotherProcessWrite(t *testing.T) {
	for _, backend := range reloadBackends {
		t.Run(backend, func(t *testing.T) {
			dashboard := newReloadDashboard(t, backend)
			external := writeFromAnotherProcess(t, dashboard, "Written by the second process")

			resp := postCapture(t, dashboard.url+"/api/capture", testToken, `{"kind":"idea","text":"Captured while the hub was running"}`)
			if resp.StatusCode != http.StatusCreated {
				_ = resp.Body.Close()
				t.Fatalf("status = %d, want 201", resp.StatusCode)
			}
			_ = resp.Body.Close()

			// A fresh handle reads the store the way the next `ft` invocation would.
			fresh := openReloadStore(t, dashboard.backend, dashboard.path)
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
				t.Fatalf("the hub's capture is not in the store: %+v", titles)
			}
		})
	}
}
