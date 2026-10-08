package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/khoinguyen/factotum/pkg/core"
)

// testAssets is a stand-in for the built shadcn/ui app: an index document, one
// hashed asset, and nothing else. The real bundle is embedded from web/dist and
// exercised end to end by `mise run smoke-web`.
func testAssets() fstest.MapFS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!doctype html><html><body><div id="root"></div>` +
				`<script type="module" src="/app/assets/app.js"></script></body></html>`),
		},
		"assets/app.js": &fstest.MapFile{Data: []byte("console.log(\"factotum\")")},
	}
}

func TestAppServesEmbeddedSPA(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	f.addTask(t, project.ID, "Fix the widget")
	ts := newTestServer(t, f, Options{Project: project.ID, Assets: testAssets()})

	// The app root and its bare form both serve the index document.
	for _, path := range []string{"/app", "/app/"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("Get(%s) error = %v", path, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Get(%s) status = %d, want 200", path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("Get(%s) Content-Type = %q, want text/html", path, ct)
		}
		if body := readBody(t, resp); !strings.Contains(body, `id="root"`) {
			t.Fatalf("Get(%s) body is not the SPA index:\n%s", path, body)
		}
	}

	// A hashed asset is served with its real type.
	resp, err := http.Get(ts.URL + "/app/assets/app.js")
	if err != nil {
		t.Fatalf("Get(asset) error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Get(asset) status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("Get(asset) Content-Type = %q, want javascript", ct)
	}
	if body := readBody(t, resp); !strings.Contains(body, "factotum") {
		t.Fatalf("Get(asset) body = %q", body)
	}
}

// TestAppFallsBackToIndexForClientRoutes proves a deep link with no file behind
// it (a client-side route) returns the index so the SPA router can take over,
// while a missing hashed asset is a hard 404 so a broken build is visible.
func TestAppFallsBackToIndexForClientRoutes(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Assets: testAssets()})

	resp, err := http.Get(ts.URL + "/app/idea/t-123")
	if err != nil {
		t.Fatalf("Get(client route) error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Get(client route) status = %d, want 200", resp.StatusCode)
	}
	if body := readBody(t, resp); !strings.Contains(body, `id="root"`) {
		t.Fatalf("client route body is not the SPA index:\n%s", body)
	}

	resp, err = http.Get(ts.URL + "/app/assets/missing.js")
	if err != nil {
		t.Fatalf("Get(missing asset) error = %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("Get(missing asset) status = %d, want 404", resp.StatusCode)
	}
}

func TestAppRejectsNonReadMethods(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Assets: testAssets()})

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/app/", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /app/ status = %d, want 405", resp.StatusCode)
	}
}

// snapshotDoc is the slice of /api/snapshot the SPA depends on.
type snapshotDoc struct {
	Title    string `json:"title"`
	Project  string `json:"project"`
	Snapshot string `json:"snapshot"`
	Stats    struct {
		Scope      int `json:"scope"`
		ReadyAgent int `json:"ready_agent"`
		ReadyHuman int `json:"ready_human"`
	} `json:"stats"`
	NextAgent []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Chip  string `json:"chip"`
	} `json:"next_agent"`
	NextHuman []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"next_human"`
	Updates []struct {
		Time    string `json:"time"`
		Summary string `json:"summary"`
	} `json:"updates"`
}

func TestSnapshotAPIReportsState(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	task := f.addTask(t, project.ID, "Fix the widget")
	ts := newTestServer(t, f, Options{Project: project.ID})

	resp, err := http.Get(ts.URL + "/api/snapshot")
	if err != nil {
		t.Fatalf("Get(/api/snapshot) error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var doc snapshotDoc
	if err := json.Unmarshal([]byte(readBody(t, resp)), &doc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if doc.Title == "" || doc.Snapshot == "" {
		t.Fatalf("snapshot missing title/snapshot: %+v", doc)
	}
	if doc.Stats.Scope != 1 {
		t.Fatalf("stats.scope = %d, want 1", doc.Stats.Scope)
	}
	found := false
	for _, view := range doc.NextHuman {
		if view.ID == string(task.ID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("next_human = %+v, want task %s", doc.NextHuman, task.ID)
	}

	// A mutation appends an event, so the next read surfaces it in updates: the
	// SPA's SSE handler refetches exactly this to stay live.
	if _, err := f.tasks.SetStatus(context.Background(), task.ID, core.StatusReadyForReview); err != nil {
		t.Fatalf("SetStatus() error = %v", err)
	}
	resp, err = http.Get(ts.URL + "/api/snapshot")
	if err != nil {
		t.Fatalf("Get(/api/snapshot) error = %v", err)
	}
	if err := json.Unmarshal([]byte(readBody(t, resp)), &doc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(doc.Updates) == 0 {
		t.Fatal("updates empty after a mutation; the snapshot does not reflect the event log")
	}
}

func TestSnapshotAPIRejectsNonReadMethods(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID})

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/snapshot", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/snapshot status = %d, want 405", resp.StatusCode)
	}
}
