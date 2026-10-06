package serve

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/rank"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/memory"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

type seqIDs struct{ n int }

func (s *seqIDs) NewID(prefix string) string {
	s.n++
	return fmt.Sprintf("%s-%d", prefix, s.n)
}

type fixture struct {
	backend  store.Backend
	projects *app.ProjectService
	tasks    *app.TaskService
	actors   *app.ActorService
	clock    fixedClock
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	backend := memory.New()
	t.Cleanup(func() { _ = backend.Close() })
	clock := fixedClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ids := &seqIDs{}
	return &fixture{
		backend:  backend,
		projects: app.NewProjectService(backend, clock, ids),
		tasks:    app.NewTaskService(backend, clock, ids),
		actors:   app.NewActorService(backend, clock, ids),
		clock:    clock,
	}
}

func (f *fixture) addProject(t *testing.T, id, name string) *core.Project {
	t.Helper()
	project, err := f.projects.CreateWithID(context.Background(), core.ProjectID(id), name, "", nil)
	if err != nil {
		t.Fatalf("CreateWithID(%s) error = %v", id, err)
	}
	return project
}

func (f *fixture) addTask(t *testing.T, projectID core.ProjectID, title string) *core.Task {
	t.Helper()
	task, err := f.tasks.Add(context.Background(), app.TaskInput{ProjectID: projectID, Title: title})
	if err != nil {
		t.Fatalf("Add(%q) error = %v", title, err)
	}
	return task
}

func newTestServer(t *testing.T, f *fixture, opts Options) *httptest.Server {
	t.Helper()
	opts.Backend = f.backend
	opts.Clock = f.clock
	if opts.Ranker == nil {
		ranker, err := rank.Default()
		if err != nil {
			t.Fatalf("rank.Default() error = %v", err)
		}
		opts.Ranker = ranker
	}
	if opts.Poll == 0 {
		opts.Poll = 10 * time.Millisecond
	}
	server, err := New(opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	return string(data)
}

func TestHandlerIsReadOnly(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	f.addTask(t, project.ID, "Fix the widget")
	ts := newTestServer(t, f, Options{Project: project.ID})

	for _, path := range []string{"/", "/fragment", "/events"} {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			req, err := http.NewRequest(method, ts.URL+path, strings.NewReader("{}"))
			if err != nil {
				t.Fatalf("NewRequest(%s %s) error = %v", method, path, err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s %s error = %v", method, path, err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s status = %d, want 405", method, path, resp.StatusCode)
			}
			if got := resp.Header.Get("Allow"); !strings.Contains(got, "GET") {
				t.Fatalf("%s %s Allow = %q, want GET", method, path, got)
			}
		}
	}
}

func TestUnknownPathIsNotFound(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID})

	resp, err := http.Get(ts.URL + "/nope")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestIndexServesLiveDashboard(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	f.addTask(t, project.ID, "Fix the widget")
	ts := newTestServer(t, f, Options{Project: project.ID})

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	body := readBody(t, resp)
	for _, want := range []string{"Fix the widget", `id="dash"`, "EventSource"} {
		if !strings.Contains(body, want) {
			t.Fatalf("index body missing %q", want)
		}
	}
}

func TestFragmentIsBodyOnly(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	f.addTask(t, project.ID, "Fix the widget")
	ts := newTestServer(t, f, Options{Project: project.ID})

	resp, err := http.Get(ts.URL + "/fragment")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "Fix the widget") {
		t.Fatalf("fragment body missing task title:\n%s", body)
	}
	if strings.Contains(body, "<!doctype") || strings.Contains(body, "<html") {
		t.Fatalf("fragment should not be a full document:\n%s", body)
	}
}

func TestSSEPushesUpdateOnStatusChange(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	task := f.addTask(t, project.ID, "Fix the widget")
	ts := newTestServer(t, f, Options{Project: project.ID})

	resp, err := http.Get(ts.URL + "/events")
	if err != nil {
		t.Fatalf("Get(/events) error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	events := eventNames(resp.Body)
	expectEvent(t, events, "hello", 2*time.Second)

	if _, err := f.tasks.SetStatus(context.Background(), task.ID, core.StatusReadyForReview); err != nil {
		t.Fatalf("SetStatus() error = %v", err)
	}
	expectEvent(t, events, "update", 2*time.Second)
}

func TestPageProjectsReadinessAndRank(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	first := f.addTask(t, project.ID, "First ready task")
	second := f.addTask(t, project.ID, "Second blocked task")
	if _, err := f.tasks.AddDep(context.Background(), second.ID, first.ID); err != nil {
		t.Fatalf("AddDep() error = %v", err)
	}

	server, err := New(Options{Backend: f.backend, Clock: f.clock, Project: project.ID})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	page, err := server.page(context.Background())
	if err != nil {
		t.Fatalf("page() error = %v", err)
	}

	if !hasTask(page.NextHuman, first.ID) {
		t.Fatalf("NextHuman = %v, want %s", taskIDs(page.NextHuman), first.ID)
	}
	waiting, ok := findTask(page.Waiting, second.ID)
	if !ok {
		t.Fatalf("Waiting = %v, want %s", taskIDs(page.Waiting), second.ID)
	}
	if waiting.Reason != ReasonDepUnresolved {
		t.Fatalf("waiting reason = %q, want %q", waiting.Reason, ReasonDepUnresolved)
	}
}

func TestPageShowsInFlight(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	task := f.addTask(t, project.ID, "Building now")
	if _, err := f.tasks.SetStatus(context.Background(), task.ID, core.StatusInProgress); err != nil {
		t.Fatalf("SetStatus() error = %v", err)
	}
	server, err := New(Options{Backend: f.backend, Clock: f.clock, Project: project.ID})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	page, err := server.page(context.Background())
	if err != nil {
		t.Fatalf("page() error = %v", err)
	}
	if !hasTask(page.InFlight, task.ID) {
		t.Fatalf("InFlight = %v, want %s", taskIDs(page.InFlight), task.ID)
	}
}

func TestAllProjectsMode(t *testing.T) {
	f := newFixture(t)
	one := f.addProject(t, "one", "One")
	two := f.addProject(t, "two", "Two")
	f.addTask(t, one.ID, "Task in one")
	f.addTask(t, two.ID, "Task in two")
	ts := newTestServer(t, f, Options{All: true})

	resp, err := http.Get(ts.URL + "/fragment")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	body := readBody(t, resp)
	for _, want := range []string{"Task in one", "Task in two"} {
		if !strings.Contains(body, want) {
			t.Fatalf("all-projects fragment missing %q", want)
		}
	}
}

func TestNoProjectFallsBackToAll(t *testing.T) {
	f := newFixture(t)
	server, err := New(Options{Backend: f.backend, Clock: f.clock})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if !server.options.All {
		t.Fatal("server without a project should serve all projects")
	}
}

func TestNewRequiresBackend(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("New() without a backend should fail")
	}
}

// TestReasonChipWraps guards the mobile regression where a long, nowrap reason
// chip (a task with several unresolved deps) overflowed a phone-width viewport
// and was clipped by overflow-x:hidden. The chip must be allowed to wrap and
// break long dependency lists anywhere.
func TestReasonChipWraps(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	f.addTask(t, project.ID, "Fix the widget")
	ts := newTestServer(t, f, Options{Project: project.ID})

	css := cssRule(getBody(t, ts.URL+"/"), ".chip.reason")
	if css == "" {
		t.Fatal("no .chip.reason rule found in the dashboard stylesheet")
	}
	for _, decl := range []string{"white-space:normal", "overflow-wrap:anywhere"} {
		if !strings.Contains(css, decl) {
			t.Fatalf(".chip.reason does not %q, so a long reason will overflow on phones:\n%s", decl, css)
		}
	}
}

// TestWaitingReasonRendersEveryBlocker ensures the server sends the full
// unresolved-dependency list to the page rather than truncating it, so the
// wrapping fix above has content to wrap.
func TestWaitingReasonRendersEveryBlocker(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	var blockers []core.TaskID
	for i := 0; i < 6; i++ {
		blocker := f.addTask(t, project.ID, fmt.Sprintf("blocker %d", i))
		blockers = append(blockers, blocker.ID)
	}
	target := f.addTask(t, project.ID, "target")
	for _, blocker := range blockers {
		if _, err := f.tasks.AddDep(context.Background(), target.ID, blocker); err != nil {
			t.Fatalf("AddDep(%s) error = %v", blocker, err)
		}
	}

	server, err := New(Options{Backend: f.backend, Clock: f.clock, Project: project.ID})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	page, err := server.page(context.Background())
	if err != nil {
		t.Fatalf("page() error = %v", err)
	}
	waiting, ok := findTask(page.Waiting, target.ID)
	if !ok {
		t.Fatalf("Waiting = %v, want %s", taskIDs(page.Waiting), target.ID)
	}
	for _, blocker := range blockers {
		if !strings.Contains(waiting.Detail, string(blocker)) {
			t.Fatalf("waiting detail %q is missing blocker %s", waiting.Detail, blocker)
		}
	}
}

func getBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("Get(%s) error = %v", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Get(%s) status = %d, want 200", url, resp.StatusCode)
	}
	return readBody(t, resp)
}

// cssRule returns the declaration block for selector in css, or "".
func cssRule(css, selector string) string {
	idx := strings.Index(css, selector)
	if idx < 0 {
		return ""
	}
	rest := css[idx:]
	end := strings.IndexByte(rest, '}')
	if end < 0 {
		return rest
	}
	return rest[:end]
}

func eventNames(body io.Reader) <-chan string {
	out := make(chan string)
	go func() {
		defer close(out)
		scanner := bufio.NewScanner(body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				out <- strings.TrimSpace(strings.TrimPrefix(line, "event: "))
			}
		}
	}()
	return out
}

func expectEvent(t *testing.T, events <-chan string, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatalf("event stream closed before %q", want)
			}
			if event == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for event %q", want)
		}
	}
}

func hasTask(tasks []taskView, id core.TaskID) bool {
	_, ok := findTask(tasks, id)
	return ok
}

func findTask(tasks []taskView, id core.TaskID) (taskView, bool) {
	for _, task := range tasks {
		if task.ID == id {
			return task, true
		}
	}
	return taskView{}, false
}

func taskIDs(tasks []taskView) []core.TaskID {
	out := make([]core.TaskID, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, task.ID)
	}
	return out
}
