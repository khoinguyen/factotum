package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

// stepClock is a mutable clock so a test can advance time between a session's
// start, its retirement, and the moment the server projects it.
type stepClock struct{ t time.Time }

func (c *stepClock) Now() time.Time { return c.t }

// sessionDoc mirrors the /api/sessions document so the test asserts the wire
// contract, not the unexported projection structs.
type sessionDoc struct {
	Sessions []sessionJSON `json:"sessions"`
}

type sessionJSON struct {
	ID              string        `json:"id"`
	State           string        `json:"state"`
	StartedAt       string        `json:"started_at"`
	StoppedAt       string        `json:"stopped_at"`
	DurationSeconds int           `json:"duration_seconds"`
	Reason          string        `json:"reason"`
	Successor       string        `json:"successor"`
	Delivered       deliveredJSON `json:"delivered"`
}

type deliveredJSON struct {
	TasksDone        int           `json:"tasks_done"`
	PRsMerged        int           `json:"prs_merged"`
	CapturesAdvanced int           `json:"captures_advanced"`
	Tasks            []sessionTask `json:"tasks"`
	PRs              []sessionPR   `json:"prs"`
}

type sessionTask struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

type sessionPR struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

func findSession(t *testing.T, doc sessionDoc, id string) sessionJSON {
	t.Helper()
	for _, s := range doc.Sessions {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("session %s not in %+v", id, doc.Sessions)
	return sessionJSON{}
}

// clockServer builds a dashboard server on the given clock, for tests that need
// a controllable "now" (the shared newTestServer pins the fixture clock).
func clockServer(t *testing.T, f *fixture, clock app.Clock, opts Options) *httptest.Server {
	t.Helper()
	opts.Backend = f.backend
	opts.Clock = clock
	if opts.Assets == nil {
		opts.Assets = testAssets()
	}
	server, err := New(opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// TestSessionsListLifecycle proves the sessions page reads the session lifecycle
// off the event log: each session is alive or retired, carries when it started
// and (when retired) stopped, how long it lived, and why it stopped.
func TestSessionsListLifecycle(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ctx := context.Background()

	clock := &stepClock{t: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	sessions := app.NewSessionService(f.backend, clock, f.ids)
	if _, err := sessions.Start(ctx, project.ID, "s-old"); err != nil {
		t.Fatalf("Start(s-old) error = %v", err)
	}
	clock.t = clock.t.Add(2 * time.Hour)
	if _, err := sessions.Start(ctx, project.ID, "s-new"); err != nil {
		t.Fatalf("Start(s-new) error = %v", err)
	}
	if _, err := sessions.Retire(ctx, project.ID, "s-old", "s-new"); err != nil {
		t.Fatalf("Retire(s-old) error = %v", err)
	}
	clock.t = clock.t.Add(time.Hour)

	ts := clockServer(t, f, clock, Options{Project: project.ID})
	var doc sessionDoc
	getDoc(t, ts.URL+"/api/sessions", &doc)

	if len(doc.Sessions) != 2 {
		t.Fatalf("sessions = %+v, want 2", doc.Sessions)
	}
	// Newest first: the successor session leads.
	if doc.Sessions[0].ID != "s-new" {
		t.Fatalf("sessions order = %+v, want s-new first", doc.Sessions)
	}

	old := findSession(t, doc, "s-old")
	if old.State != "retired" {
		t.Fatalf("s-old state = %q, want retired", old.State)
	}
	if old.StartedAt != "2026-01-01T09:00:00Z" {
		t.Fatalf("s-old started_at = %q", old.StartedAt)
	}
	if old.StoppedAt != "2026-01-01T11:00:00Z" {
		t.Fatalf("s-old stopped_at = %q", old.StoppedAt)
	}
	if old.DurationSeconds != 7200 {
		t.Fatalf("s-old duration = %d, want 7200", old.DurationSeconds)
	}
	if old.Reason != "retired for s-new" || old.Successor != "s-new" {
		t.Fatalf("s-old reason = %q successor = %q", old.Reason, old.Successor)
	}

	fresh := findSession(t, doc, "s-new")
	if fresh.State != "alive" {
		t.Fatalf("s-new state = %q, want alive", fresh.State)
	}
	if fresh.StoppedAt != "" {
		t.Fatalf("s-new stopped_at = %q, want empty", fresh.StoppedAt)
	}
	if fresh.DurationSeconds != 3600 {
		t.Fatalf("s-new duration = %d, want 3600 (now - start)", fresh.DurationSeconds)
	}
}

// TestSessionsDeliveredWork proves each session reports the work its events
// delivered: the tasks driven to done, the pull requests recorded on those
// tasks, and the captures advanced by delivering their promoted work.
func TestSessionsDeliveredWork(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ctx := context.Background()

	if _, err := f.sessions.Start(ctx, project.ID, "s1"); err != nil {
		t.Fatalf("Start(s1) error = %v", err)
	}
	agent, err := f.actors.Add(ctx, core.ActorAgent, "claude")
	if err != nil {
		t.Fatalf("Add(agent) error = %v", err)
	}
	sessCtx := app.WithCaller(ctx, agent.ID, "s1")

	idea := f.addIdea(t, project.ID, "Idea one", "")
	task := f.promote(t, idea.ID)
	if _, err := f.tasks.SetStatus(sessCtx, task.ID, core.StatusDone); err != nil {
		t.Fatalf("SetStatus(task) error = %v", err)
	}
	if _, err := f.tasks.AddNote(sessCtx, task.ID, app.NoteInput{
		Body:  "opened the PR",
		Links: []core.Link{{Kind: core.LinkPR, URL: "https://example.com/pr/1", Title: "PR one"}},
	}); err != nil {
		t.Fatalf("AddNote(task) error = %v", err)
	}

	standalone := f.addTask(t, project.ID, "Standalone work")
	if _, err := f.tasks.SetStatus(sessCtx, standalone.ID, core.StatusDone); err != nil {
		t.Fatalf("SetStatus(standalone) error = %v", err)
	}

	// A task only started did not deliver, so it must not be counted.
	started := f.addTask(t, project.ID, "Still going")
	if _, err := f.tasks.SetStatus(sessCtx, started.ID, core.StatusInProgress); err != nil {
		t.Fatalf("SetStatus(started) error = %v", err)
	}

	ts := newTestServer(t, f, Options{Project: project.ID})
	var doc sessionDoc
	getDoc(t, ts.URL+"/api/sessions", &doc)

	s := findSession(t, doc, "s1")
	if s.Delivered.TasksDone != 2 {
		t.Fatalf("tasks_done = %d, want 2", s.Delivered.TasksDone)
	}
	if s.Delivered.PRsMerged != 1 {
		t.Fatalf("prs_merged = %d, want 1", s.Delivered.PRsMerged)
	}
	if s.Delivered.CapturesAdvanced != 1 {
		t.Fatalf("captures_advanced = %d, want 1", s.Delivered.CapturesAdvanced)
	}
	if len(s.Delivered.PRs) != 1 || s.Delivered.PRs[0].URL != "https://example.com/pr/1" {
		t.Fatalf("prs = %+v, want the recorded pull request", s.Delivered.PRs)
	}
	var ids []string
	for _, task := range s.Delivered.Tasks {
		ids = append(ids, task.ID)
	}
	if !contains(ids, string(task.ID)) || !contains(ids, string(standalone.ID)) {
		t.Fatalf("delivered tasks = %v, want %s and %s", ids, task.ID, standalone.ID)
	}
}

// TestSessionsRespectProjectScope proves a scoped server lists only its own
// project's sessions, while the all-projects server spans every registered one.
func TestSessionsRespectProjectScope(t *testing.T) {
	f := newFixture(t)
	mine := f.addProject(t, "acme", "Acme")
	other := f.addProject(t, "other", "Other")
	ctx := context.Background()
	if _, err := f.sessions.Start(ctx, mine.ID, "s-mine"); err != nil {
		t.Fatalf("Start(s-mine) error = %v", err)
	}
	if _, err := f.sessions.Start(ctx, other.ID, "s-other"); err != nil {
		t.Fatalf("Start(s-other) error = %v", err)
	}

	scoped := newTestServer(t, f, Options{Project: mine.ID})
	var mineDoc sessionDoc
	getDoc(t, scoped.URL+"/api/sessions", &mineDoc)
	if len(mineDoc.Sessions) != 1 || mineDoc.Sessions[0].ID != "s-mine" {
		t.Fatalf("scoped sessions = %+v, want only s-mine", mineDoc.Sessions)
	}

	all := newTestServer(t, f, Options{All: true})
	var allDoc sessionDoc
	getDoc(t, all.URL+"/api/sessions", &allDoc)
	if len(allDoc.Sessions) != 2 {
		t.Fatalf("all-projects sessions = %+v, want both", allDoc.Sessions)
	}
}

// TestSessionsEmpty proves an empty log projects an empty list, not null, so the
// page can render its empty state.
func TestSessionsEmpty(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID})

	resp, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	body := readBody(t, resp)
	if body != "{\"sessions\":[]}\n" {
		t.Fatalf("empty sessions body = %q, want an empty list", body)
	}
}

// TestSessionsSurfaceIsReadOnly proves the sessions surface stays inside the
// read-only boundary and that the client route serves the SPA shell.
func TestSessionsSurfaceIsReadOnly(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID})

	resp, err := http.Get(ts.URL + "/sessions")
	if err != nil {
		t.Fatalf("Get(/sessions) error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /sessions status = %d, want 200", resp.StatusCode)
	}
	if body := readBody(t, resp); !strings.Contains(body, `id="root"`) {
		t.Fatalf("GET /sessions is not the SPA shell:\n%s", body)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/sessions error = %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/sessions status = %d, want 405", resp.StatusCode)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
