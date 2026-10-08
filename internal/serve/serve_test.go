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
	backend   store.Backend
	projects  *app.ProjectService
	tasks     *app.TicketService
	actors    *app.ActorService
	artifacts *app.ArtifactService
	messages  *app.MessageService
	clock     fixedClock
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	backend := memory.New()
	t.Cleanup(func() { _ = backend.Close() })
	clock := fixedClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ids := &seqIDs{}
	return &fixture{
		backend:   backend,
		projects:  app.NewProjectService(backend, clock, ids),
		tasks:     app.NewTicketService(backend, clock, ids),
		actors:    app.NewActorService(backend, clock, ids),
		artifacts: app.NewArtifactService(backend, clock, ids),
		messages:  app.NewMessageService(backend, clock, ids),
		clock:     clock,
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

func (f *fixture) addTask(t *testing.T, projectID core.ProjectID, title string) *core.Ticket {
	t.Helper()
	task, err := f.tasks.Add(context.Background(), app.TicketInput{ProjectID: projectID, Title: title})
	if err != nil {
		t.Fatalf("Add(%q) error = %v", title, err)
	}
	return task
}

func newTestServer(t *testing.T, f *fixture, opts Options) *httptest.Server {
	t.Helper()
	opts.Backend = f.backend
	opts.Clock = f.clock
	if opts.Assets == nil {
		opts.Assets = testAssets()
	}
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

	for _, path := range []string{"/", "/api/snapshot", "/events"} {
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

// TestIndexServesSPAApp proves the dashboard root is the shadcn/ui app shell,
// not the retired server-rendered page: the read side is now the SPA, which
// fetches state from /api/snapshot and refetches on /events.
func TestIndexServesSPAApp(t *testing.T) {
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
	if !strings.Contains(body, `id="root"`) {
		t.Fatalf("index body is not the SPA shell:\n%s", body)
	}
}

// TestReadRoutesServeSPA proves every read-side drill-down route returns the app
// shell so the client router can resolve it; the id data comes from /api.
func TestReadRoutesServeSPA(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID})

	for _, path := range []string{"/idea/t-1", "/task/t-1", "/memory/art-1", "/doc/art-1"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("Get(%s) error = %v", path, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Get(%s) status = %d, want 200", path, resp.StatusCode)
		}
		if body := readBody(t, resp); !strings.Contains(body, `id="root"`) {
			t.Fatalf("Get(%s) is not the SPA shell:\n%s", path, body)
		}
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

// TestSSEFansOutToManyClients proves the shared broker delivers one mutation to
// every connected client, not just the first.
func TestSSEFansOutToManyClients(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	task := f.addTask(t, project.ID, "Fix the widget")
	ts := newTestServer(t, f, Options{Project: project.ID})

	const clients = 3
	streams := make([]<-chan string, clients)
	for i := range streams {
		resp, err := http.Get(ts.URL + "/events")
		if err != nil {
			t.Fatalf("Get(/events) error = %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		streams[i] = eventNames(resp.Body)
		expectEvent(t, streams[i], "hello", 2*time.Second)
	}

	if _, err := f.tasks.SetStatus(context.Background(), task.ID, core.StatusReadyForReview); err != nil {
		t.Fatalf("SetStatus() error = %v", err)
	}
	for _, stream := range streams {
		expectEvent(t, stream, "update", 2*time.Second)
	}
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

// omitRanker drops one ready task from its ranking, reproducing a ranker that
// omits a startable task the readiness buckets still count.
type omitRanker struct{ omit core.TicketID }

func (r omitRanker) Name() string { return "omit" }

func (r omitRanker) Rank(_ context.Context, req rank.Request) ([]rank.Scored, error) {
	var out []rank.Scored
	for _, id := range req.Graph.ReadySet() {
		if id == r.omit {
			continue
		}
		out = append(out, rank.Scored{TicketID: id, Score: 1})
	}
	return out, nil
}

// TestNextUpStatsMatchListedRows guards the dashboard bug where the "Agent
// next"/"Human next" stats counted the full ready buckets while the sections
// listed only the ranker's scored subset, so a count exceeded its rows whenever
// the ranker omitted a ready task.
func TestNextUpStatsMatchListedRows(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	agent, err := f.actors.Add(context.Background(), core.ActorAgent, "claude")
	if err != nil {
		t.Fatalf("Add(agent) error = %v", err)
	}
	humanListed, err := f.tasks.Add(context.Background(), app.TicketInput{ProjectID: project.ID, Title: "Human listed"})
	if err != nil {
		t.Fatalf("Add(human listed) error = %v", err)
	}
	humanOmitted, err := f.tasks.Add(context.Background(), app.TicketInput{ProjectID: project.ID, Title: "Human omitted"})
	if err != nil {
		t.Fatalf("Add(human omitted) error = %v", err)
	}
	agentTask, err := f.tasks.Add(context.Background(), app.TicketInput{
		ProjectID:          project.ID,
		Title:              "Agent task",
		AssigneeID:         &agent.ID,
		Groomed:            true,
		AcceptanceCriteria: []string{"works"},
	})
	if err != nil {
		t.Fatalf("Add(agent task) error = %v", err)
	}

	server, err := New(Options{
		Backend: f.backend,
		Clock:   f.clock,
		Project: project.ID,
		Ranker:  omitRanker{omit: humanOmitted.ID},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	page, err := server.page(context.Background())
	if err != nil {
		t.Fatalf("page() error = %v", err)
	}

	if !hasTask(page.NextHuman, humanListed.ID) {
		t.Fatalf("NextHuman = %v, want %s", taskIDs(page.NextHuman), humanListed.ID)
	}
	if hasTask(page.NextHuman, humanOmitted.ID) {
		t.Fatalf("NextHuman = %v, omitted task %s must not be listed", taskIDs(page.NextHuman), humanOmitted.ID)
	}
	if !hasTask(page.NextAgent, agentTask.ID) {
		t.Fatalf("NextAgent = %v, want %s", taskIDs(page.NextAgent), agentTask.ID)
	}
	if page.Stats.ReadyHuman != len(page.NextHuman) {
		t.Fatalf("ReadyHuman stat = %d, want len(NextHuman) = %d", page.Stats.ReadyHuman, len(page.NextHuman))
	}
	if page.Stats.ReadyAgent != len(page.NextAgent) {
		t.Fatalf("ReadyAgent stat = %d, want len(NextAgent) = %d", page.Stats.ReadyAgent, len(page.NextAgent))
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

	resp, err := http.Get(ts.URL + "/api/snapshot")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	body := readBody(t, resp)
	for _, want := range []string{"Task in one", "Task in two"} {
		if !strings.Contains(body, want) {
			t.Fatalf("all-projects snapshot missing %q:\n%s", want, body)
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

// TestWaitingReasonRendersEveryBlocker ensures the server sends the full
// unresolved-dependency list to the page rather than truncating it, so the
// wrapping fix above has content to wrap.
func TestWaitingReasonRendersEveryBlocker(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	var blockers []core.TicketID
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

func hasTask(tasks []taskView, id core.TicketID) bool {
	_, ok := findTask(tasks, id)
	return ok
}

func findTask(tasks []taskView, id core.TicketID) (taskView, bool) {
	for _, task := range tasks {
		if task.ID == id {
			return task, true
		}
	}
	return taskView{}, false
}

func taskIDs(tasks []taskView) []core.TicketID {
	out := make([]core.TicketID, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, task.ID)
	}
	return out
}
