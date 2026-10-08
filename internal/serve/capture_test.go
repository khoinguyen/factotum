package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

const testToken = "s3cret"

// recordingController is a CaptureController that remembers what the write side
// handed it, so a test can assert the trigger fired (or did not) and with which
// capture.
type recordingController struct {
	mu       sync.Mutex
	captures []*core.Ticket
	ctx      context.Context
}

func (c *recordingController) Submit(ctx context.Context, capture *core.Ticket) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.captures = append(c.captures, capture)
	c.ctx = ctx
}

func (c *recordingController) stored() []*core.Ticket {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*core.Ticket(nil), c.captures...)
}

func (c *recordingController) context() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ctx
}

// postCapture sends a JSON capture write with the token in the Authorization
// header, the only credential the write side accepts.
func postCapture(t *testing.T, target, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	return resp
}

func decodeCapture(t *testing.T, resp *http.Response) captureResult {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	var result captureResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Decode(capture) error = %v", err)
	}
	return result
}

func ideas(t *testing.T, f *fixture, project core.ProjectID) []*core.Ticket {
	t.Helper()
	kind := core.KindIdea
	list, err := f.tasks.List(context.Background(), store.TicketFilter{ProjectID: project, Kind: &kind})
	if err != nil {
		t.Fatalf("List(ideas) error = %v", err)
	}
	return list
}

// TestCapturePageServesSPA proves the capture page is a page of the same app:
// GET /capture returns the shadcn/ui shell so the client router renders the
// form, not the retired server-rendered page.
func TestCapturePageServesSPA(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	if body := getBody(t, ts.URL+"/capture"); !strings.Contains(body, `id="root"`) {
		t.Fatalf("GET /capture is not the SPA shell:\n%s", body)
	}
}

// TestCaptureConfigReportsState proves the app can tell whether capture is
// enabled before showing a form: the config endpoint mirrors captureEnabled.
func TestCaptureConfigReportsState(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")

	cases := []struct {
		name    string
		opts    Options
		enabled bool
	}{
		{"scoped with token", Options{Project: project.ID, Token: testToken, Tasks: f.tasks}, true},
		{"no token", Options{Project: project.ID, Tasks: f.tasks}, false},
		{"all projects", Options{All: true, Token: testToken, Tasks: f.tasks}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServer(t, f, tc.opts)
			var config captureConfig
			getDoc(t, ts.URL+"/api/capture", &config)
			if config.Enabled != tc.enabled {
				t.Fatalf("config.enabled = %v, want %v", config.Enabled, tc.enabled)
			}
		})
	}
}

// TestCaptureRequiresToken proves the gate: a write with no token or a wrong
// token is rejected and stores nothing. The token is the whole auth boundary.
func TestCaptureRequiresToken(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	cases := []struct {
		name  string
		token string
	}{
		{"missing token", ""},
		{"wrong token", "nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postCapture(t, ts.URL+"/api/capture", tc.token, `{"text":"an idea"}`)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.StatusCode)
			}
		})
	}

	if got := ideas(t, f, project.ID); len(got) != 0 {
		t.Fatalf("rejected writes created %d ideas, want 0", len(got))
	}
}

// TestCaptureQueryTokenIsRejected proves the token is header-only: a token in
// the query string never authenticates, so a shared token cannot leak through
// logs or a Referer header.
func TestCaptureQueryTokenIsRejected(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	resp := postCapture(t, ts.URL+"/api/capture?token="+testToken, "", `{"text":"sneaky"}`)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if got := ideas(t, f, project.ID); len(got) != 0 {
		t.Fatalf("query-token write created %d ideas, want 0", len(got))
	}
}

// TestCaptureStoresIdea proves an authenticated JSON write stores an idea and
// returns the detail URL the app navigates to.
func TestCaptureStoresIdea(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	resp := postCapture(t, ts.URL+"/api/capture", testToken, `{"text":"Add a dark mode"}`)
	if resp.StatusCode != http.StatusCreated {
		_ = resp.Body.Close()
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	result := decodeCapture(t, resp)
	if result.Kind != core.KindIdea {
		t.Fatalf("result.kind = %q, want idea", result.Kind)
	}
	if result.ID == "" || !strings.HasPrefix(result.URL, "/idea/") {
		t.Fatalf("result = %+v, want an id and /idea URL", result)
	}

	task, err := f.tasks.Get(context.Background(), result.ID)
	if err != nil {
		t.Fatalf("Get(captured) error = %v", err)
	}
	if task.Kind != core.KindIdea || task.Status != core.StatusTodo {
		t.Fatalf("captured task = %+v, want idea in todo", task)
	}
	if task.ProjectID != project.ID {
		t.Fatalf("captured project = %q, want %q", task.ProjectID, project.ID)
	}
	if task.Title != "Add a dark mode" {
		t.Fatalf("title = %q, want the sentence", task.Title)
	}
	if result.URL != "/idea/"+string(task.ID) {
		t.Fatalf("result.url = %q, want the captured idea page", result.URL)
	}
}

// TestCaptureStoresBug proves the same write side captures a defect: a bug kind
// is stored and returned, so the app can offer idea-or-bug capture.
func TestCaptureStoresBug(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	resp := postCapture(t, ts.URL+"/api/capture", testToken, `{"kind":"bug","text":"It crashes on save"}`)
	if resp.StatusCode != http.StatusCreated {
		_ = resp.Body.Close()
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	result := decodeCapture(t, resp)
	if result.Kind != core.KindBug {
		t.Fatalf("result.kind = %q, want bug", result.Kind)
	}
	task, err := f.tasks.Get(context.Background(), result.ID)
	if err != nil {
		t.Fatalf("Get(captured) error = %v", err)
	}
	if task.Kind != core.KindBug {
		t.Fatalf("captured task kind = %q, want bug", task.Kind)
	}
}

// TestCaptureTriggersController proves the factory trigger is wired to the write
// path: a capture that is durably stored is handed to the controller, exactly
// once, with the stored item, so a daemon can start the capture's pipeline.
func TestCaptureTriggersController(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")

	for _, tc := range []struct {
		name string
		body string
		kind core.TicketKind
	}{
		{"idea", `{"text":"Add a dark mode"}`, core.KindIdea},
		{"bug", `{"kind":"bug","text":"It crashes on save"}`, core.KindBug},
	} {
		t.Run(tc.name, func(t *testing.T) {
			controller := &recordingController{}
			ts := newTestServer(t, f, Options{
				Project: project.ID, Token: testToken, Tasks: f.tasks,
				AutoGroom: true, Controller: controller,
			})
			resp := postCapture(t, ts.URL+"/api/capture", testToken, tc.body)
			if resp.StatusCode != http.StatusCreated {
				_ = resp.Body.Close()
				t.Fatalf("status = %d, want 201", resp.StatusCode)
			}
			result := decodeCapture(t, resp)
			stored, err := f.tasks.Get(context.Background(), result.ID)
			if err != nil {
				t.Fatalf("Get(%s) error = %v", result.ID, err)
			}
			if stored.Kind != tc.kind {
				t.Fatalf("stored kind = %q, want %q", stored.Kind, tc.kind)
			}
			got := controller.stored()
			if len(got) != 1 {
				t.Fatalf("controller received %d captures, want 1", len(got))
			}
			if got[0].ID != result.ID {
				t.Fatalf("controller capture id = %q, want %q", got[0].ID, result.ID)
			}
		})
	}
}

// TestCaptureTriggerOptOut proves the off switch: with auto-groom disabled the
// capture still stores, but the controller is never invoked.
func TestCaptureTriggerOptOut(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	controller := &recordingController{}
	ts := newTestServer(t, f, Options{
		Project: project.ID, Token: testToken, Tasks: f.tasks,
		AutoGroom: false, Controller: controller,
	})

	resp := postCapture(t, ts.URL+"/api/capture", testToken, `{"text":"Add a dark mode"}`)
	if resp.StatusCode != http.StatusCreated {
		_ = resp.Body.Close()
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	_ = resp.Body.Close()
	if got := controller.stored(); len(got) != 0 {
		t.Fatalf("opt-out still triggered %d captures, want 0", len(got))
	}
}

// TestCaptureWithoutController proves a plain read+capture server (no
// controller, the ft serve default) stores a capture without a trigger: the
// hook is optional and its absence is a valid opt-out. It reads the item back,
// so a regression that skipped the store when the trigger is absent fails here.
func TestCaptureWithoutController(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks, AutoGroom: true})

	resp := postCapture(t, ts.URL+"/api/capture", testToken, `{"text":"Add a dark mode"}`)
	if resp.StatusCode != http.StatusCreated {
		_ = resp.Body.Close()
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	result := decodeCapture(t, resp)
	stored, err := f.tasks.Get(context.Background(), result.ID)
	if err != nil {
		t.Fatalf("Get(%s) error = %v", result.ID, err)
	}
	if stored.Kind != core.KindIdea || stored.Status != core.StatusTodo {
		t.Fatalf("stored = %+v, want idea in todo", stored)
	}
}

// TestCaptureTriggerContextSurvivesRequest proves the context handed to the
// controller is detached from the request: net/http cancels r.Context() when the
// handler returns, so a controller that retains the context for async work must
// still see it live after the request is done.
func TestCaptureTriggerContextSurvivesRequest(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	controller := &recordingController{}
	server, err := New(Options{
		Backend: f.backend, Clock: f.clock, Assets: testAssets(),
		Project: project.ID, Token: testToken, Tasks: f.tasks,
		AutoGroom: true, Controller: controller,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/capture", strings.NewReader(`{"text":"Add a dark mode"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	server.Handler().ServeHTTP(httptest.NewRecorder(), req)

	cancel()
	if got := controller.context(); got == nil {
		t.Fatal("controller did not receive a context")
	} else if err := got.Err(); err != nil {
		t.Fatalf("controller context canceled with the request: %v", err)
	}
}

// TestCaptureTriggerNotCalledOnRejected proves the trigger only fires for a
// stored capture: a rejected write (bad token) never reaches the controller.
func TestCaptureTriggerNotCalledOnRejected(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	controller := &recordingController{}
	ts := newTestServer(t, f, Options{
		Project: project.ID, Token: testToken, Tasks: f.tasks,
		AutoGroom: true, Controller: controller,
	})

	resp := postCapture(t, ts.URL+"/api/capture", "wrong", `{"text":"sneaky"}`)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if got := controller.stored(); len(got) != 0 {
		t.Fatalf("rejected capture triggered %d, want 0", len(got))
	}
}

// TestCaptureRejectsUnknownKind proves the kind is validated at the boundary: a
// client cannot ask the capture write side to store executable work.
func TestCaptureRejectsUnknownKind(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	for _, kind := range []string{"task", "milestone", "nonsense"} {
		resp := postCapture(t, ts.URL+"/api/capture", testToken, `{"kind":"`+kind+`","text":"x"}`)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("kind %q status = %d, want 400", kind, resp.StatusCode)
		}
	}
	if got := ideas(t, f, project.ID); len(got) != 0 {
		t.Fatalf("invalid-kind captures created %d ideas, want 0", len(got))
	}
}

// TestCaptureRejectsEmptyText proves a capture with no idea text is a bad
// request, not an empty idea.
func TestCaptureRejectsEmptyText(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	for _, text := range []string{"", "   ", "\n\t"} {
		body, err := json.Marshal(captureRequest{Text: text})
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		resp := postCapture(t, ts.URL+"/api/capture", testToken, string(body))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("text %q status = %d, want 400", text, resp.StatusCode)
		}
	}
	if got := ideas(t, f, project.ID); len(got) != 0 {
		t.Fatalf("empty captures created %d ideas, want 0", len(got))
	}
}

// TestCaptureRejectsMalformedBody proves a non-JSON body is a bad request, not
// a panic or a 500.
func TestCaptureRejectsMalformedBody(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	resp := postCapture(t, ts.URL+"/api/capture", testToken, "not json")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestCaptureRejectsOversizedBody proves the body is bounded: a runaway client
// cannot make the server buffer an unbounded payload.
func TestCaptureRejectsOversizedBody(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	big, err := json.Marshal(captureRequest{Text: strings.Repeat("x", maxCaptureBytes+1)})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	resp := postCapture(t, ts.URL+"/api/capture", testToken, string(big))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestCaptureRejectsBearerLengthMismatch is the cheap companion to the
// constant-time comparison: a token that only shares a prefix never passes.
func TestCaptureRejectsBearerLengthMismatch(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	resp := postCapture(t, ts.URL+"/api/capture", testToken+"x", `{"text":"sneaky"}`)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

// TestCaptureDisabledWithoutToken proves writes fail closed: with no configured
// token the endpoint refuses rather than allowing an anonymous write.
func TestCaptureDisabledWithoutToken(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Tasks: f.tasks})

	resp := postCapture(t, ts.URL+"/api/capture", testToken, `{"text":"an idea"}`)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 when capture is not configured", resp.StatusCode)
	}
	if got := ideas(t, f, project.ID); len(got) != 0 {
		t.Fatalf("disabled capture created %d ideas, want 0", len(got))
	}
}

// TestCaptureDisabledForAllProjects proves capture needs one scoped project to
// attribute the idea to; all-projects serving has no capture target.
func TestCaptureDisabledForAllProjects(t *testing.T) {
	f := newFixture(t)
	one := f.addProject(t, "one", "One")
	ts := newTestServer(t, f, Options{All: true, Token: testToken, Tasks: f.tasks})

	resp := postCapture(t, ts.URL+"/api/capture", testToken, `{"text":"an idea"}`)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 in all-projects mode", resp.StatusCode)
	}
	if got := ideas(t, f, one.ID); len(got) != 0 {
		t.Fatalf("all-projects capture created %d ideas, want 0", len(got))
	}
}

func TestCaptureMethodNotAllowed(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req, err := http.NewRequest(method, ts.URL+"/api/capture", strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("NewRequest(%s) error = %v", method, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s error = %v", method, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want 405", method, resp.StatusCode)
		}
		if got := resp.Header.Get("Allow"); !strings.Contains(got, "POST") {
			t.Fatalf("%s Allow = %q, want POST", method, got)
		}
	}
}

// TestCaptureDoesNotCloseReadSide proves the write gate never leaks onto reads:
// with a token configured, the dashboard is still open.
func TestCaptureDoesNotCloseReadSide(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	f.addTask(t, project.ID, "Fix the widget")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	for _, path := range []string{"/", "/api/snapshot"} {
		if body := getBody(t, ts.URL+path); body == "" {
			t.Fatalf("GET %s returned an empty body", path)
		}
	}
}

// TestIdeaFromSentence pins the capture split: the first line becomes the
// title, the rest is kept verbatim as the body, and a single-line capture keeps
// the whole sentence so nothing typed is lost.
func TestIdeaFromSentence(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		title string
		body  string
	}{
		{"single line", "Add a dark mode", "Add a dark mode", "Add a dark mode"},
		{"multi line", "Add a dark mode\n\nUsers want it at night.", "Add a dark mode", "Users want it at night."},
		{"collapses title spacing", "Fix   the   widget", "Fix the widget", "Fix   the   widget"},
		{"trims blank edges", "\n\n  Ship the thing  \n\n", "Ship the thing", "Ship the thing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, body := ideaFromSentence(tc.raw)
			if title != tc.title || body != tc.body {
				t.Fatalf("ideaFromSentence(%q) = (%q, %q), want (%q, %q)", tc.raw, title, body, tc.title, tc.body)
			}
		})
	}
}

// TestCaptureTruncatesLongTitle keeps a whole paragraph from becoming an
// unbounded title while the raw body keeps every word.
func TestCaptureTruncatesLongTitle(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	long := strings.Repeat("word ", 60)
	body, err := json.Marshal(captureRequest{Text: long})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	resp := postCapture(t, ts.URL+"/api/capture", testToken, string(body))
	if resp.StatusCode != http.StatusCreated {
		_ = resp.Body.Close()
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	result := decodeCapture(t, resp)
	task, err := f.tasks.Get(context.Background(), result.ID)
	if err != nil {
		t.Fatalf("Get(captured) error = %v", err)
	}
	if len([]rune(task.Title)) > 100 {
		t.Fatalf("title has %d runes, want <= 100: %q", len([]rune(task.Title)), task.Title)
	}
	if task.Description != strings.TrimSpace(long) {
		t.Fatalf("description lost the raw sentence")
	}
}
