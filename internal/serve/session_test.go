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
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
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
	LastEventAt     string        `json:"last_event_at"`
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
	// Read the dashboard five minutes after the new session started, inside the
	// idle window, so it still counts as alive.
	clock.t = clock.t.Add(5 * time.Minute)

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
	if old.LastEventAt != "2026-01-01T11:00:00Z" {
		t.Fatalf("s-old last_event_at = %q, want the retirement time", old.LastEventAt)
	}

	fresh := findSession(t, doc, "s-new")
	if fresh.State != "alive" {
		t.Fatalf("s-new state = %q, want alive", fresh.State)
	}
	if fresh.StoppedAt != "" {
		t.Fatalf("s-new stopped_at = %q, want empty", fresh.StoppedAt)
	}
	if fresh.DurationSeconds != 300 {
		t.Fatalf("s-new duration = %d, want 300 (now - start)", fresh.DurationSeconds)
	}
	if fresh.LastEventAt != "2026-01-01T11:00:00Z" {
		t.Fatalf("s-new last_event_at = %q", fresh.LastEventAt)
	}
}

// TestSessionsShowAFinishedRunAsRetired proves a run's minted session is bounded
// on the sessions page: the run records the session's start and retirement, so
// the session reads as retired at the moment the run ended, its duration stops
// growing, and the document keeps its world-facing shape.
func TestSessionsShowAFinishedRunAsRetired(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	project, err := f.projects.CreateWithID(ctx, core.ProjectID("acme"), "Acme", "", []core.Repository{
		{Name: "web", Path: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("CreateWithID(acme) error = %v", err)
	}
	task, err := f.tasks.Add(ctx, app.TicketInput{ProjectID: project.ID, Repo: "web", Title: "Add a widget"})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	clock := &stepClock{t: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n")})
	outcome, err := app.NewRunService(f.backend, f.tasks, clock, f.ids).Run(ctx, app.RunInput{
		TicketID:      task.ID,
		Backend:       backend,
		Harness:       harnessfake.New("opencode"),
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// A live session's duration keeps growing with the clock; a retired one is
	// frozen at its stop.
	clock.t = clock.t.Add(time.Hour)

	ts := clockServer(t, f, clock, Options{Project: project.ID})
	var doc sessionDoc
	getDoc(t, ts.URL+"/api/sessions", &doc)

	if len(doc.Sessions) != 1 {
		t.Fatalf("sessions = %+v, want the run's one session", doc.Sessions)
	}
	view := doc.Sessions[0]
	if view.ID != outcome.Session {
		t.Fatalf("session id = %q, want the run session %q", view.ID, outcome.Session)
	}
	if view.State != "retired" {
		t.Fatalf("state = %q, want retired for a finished run", view.State)
	}
	if view.StartedAt != "2026-01-01T09:00:00Z" || view.StoppedAt != "2026-01-01T09:00:00Z" {
		t.Fatalf("started_at = %q stopped_at = %q, want the run's start and end", view.StartedAt, view.StoppedAt)
	}
	if view.DurationSeconds != 0 {
		t.Fatalf("duration = %d, want 0: the clock moved but the retired session must not keep growing", view.DurationSeconds)
	}
	// The projection's shape is unchanged: the delivered block is present with
	// its arrays empty, not absent.
	if view.Delivered.Tasks == nil || view.Delivered.PRs == nil {
		t.Fatalf("delivered = %+v, want empty arrays", view.Delivered)
	}
}

// TestSessionsGoStaleWithoutActivity proves liveness is derived from evidence,
// not trusted from the record: a session whose last event is older than the
// idle window reads stale with its duration frozen at that last event, so a
// chief that exits, is killed, or dies with its surface cannot sit on the
// dashboard as alive forever. Retiring it still works, and wins over staleness.
func TestSessionsGoStaleWithoutActivity(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ctx := context.Background()

	clock := &stepClock{t: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	sessions := app.NewSessionService(f.backend, clock, f.ids)
	if _, err := sessions.Start(ctx, project.ID, "s-dead"); err != nil {
		t.Fatalf("Start(s-dead) error = %v", err)
	}

	agent, err := f.actors.Add(ctx, core.ActorAgent, "claude")
	if err != nil {
		t.Fatalf("Add(agent) error = %v", err)
	}
	task := f.addTask(t, project.ID, "Work the session did")
	clock.t = clock.t.Add(5 * time.Minute)
	// The fixture's services run on the fixed fixture clock; this session's
	// events must land on the step clock the dashboard reads.
	tasks := app.NewTicketService(f.backend, clock, f.ids)
	if _, err := tasks.SetStatus(app.WithCaller(ctx, agent.ID, "s-dead"), task.ID, core.StatusInProgress); err != nil {
		t.Fatalf("SetStatus() error = %v", err)
	}
	lastSeen := clock.t

	// One session's start is old, the other's is inside the window: only the
	// old one is stale at the moment the dashboard reads.
	clock.t = lastSeen.Add(app.DefaultSessionIdle - time.Minute)
	if _, err := sessions.Start(ctx, project.ID, "s-live"); err != nil {
		t.Fatalf("Start(s-live) error = %v", err)
	}
	clock.t = clock.t.Add(2 * time.Minute)

	ts := clockServer(t, f, clock, Options{Project: project.ID})
	var doc sessionDoc
	getDoc(t, ts.URL+"/api/sessions", &doc)

	dead := findSession(t, doc, "s-dead")
	if dead.State != "stale" {
		t.Fatalf("s-dead state = %q, want stale: a session with no events inside the window is not alive", dead.State)
	}
	if dead.LastEventAt != lastSeen.UTC().Format(time.RFC3339) {
		t.Fatalf("s-dead last_event_at = %q, want %q", dead.LastEventAt, lastSeen.UTC().Format(time.RFC3339))
	}
	if dead.DurationSeconds != 300 {
		t.Fatalf("s-dead duration = %d, want 300 (last event - start, frozen)", dead.DurationSeconds)
	}

	live := findSession(t, doc, "s-live")
	if live.State != "alive" {
		t.Fatalf("s-live state = %q, want alive inside the window", live.State)
	}

	// The explicit path stays available: retiring a stale session makes it
	// retired, not stale, and stops its clock at the retirement.
	if _, err := sessions.Retire(ctx, project.ID, "s-dead", ""); err != nil {
		t.Fatalf("Retire(s-dead) error = %v", err)
	}
	var retiredDoc sessionDoc
	getDoc(t, ts.URL+"/api/sessions", &retiredDoc)
	retired := findSession(t, retiredDoc, "s-dead")
	if retired.State != "retired" {
		t.Fatalf("s-dead after retire state = %q, want retired", retired.State)
	}
	if retired.StoppedAt == "" {
		t.Fatalf("s-dead after retire stopped_at is empty, want the retirement time")
	}
}

// TestSessionsLiveRunKeepsSessionAlive proves the msg-run lease is the primary
// liveness signal: a receiver still heartbeating its session keeps it alive even
// when no event has been recorded for longer than the idle window, and once the
// lease lapses with nothing else happening the session goes stale.
func TestSessionsLiveRunKeepsSessionAlive(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ctx := context.Background()

	clock := &stepClock{t: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	sessions := app.NewSessionService(f.backend, clock, f.ids)
	if _, err := sessions.Start(ctx, project.ID, "s-run"); err != nil {
		t.Fatalf("Start(s-run) error = %v", err)
	}
	clock.t = clock.t.Add(2 * app.DefaultSessionIdle)

	if _, err := app.NewMessageService(f.backend, clock, f.ids).RegisterRun(ctx, app.RegisterRunInput{
		ProjectID: project.ID,
		ActorID:   core.ActorID("chief"),
		Session:   "s-run",
		TTL:       time.Minute,
	}); err != nil {
		t.Fatalf("RegisterRun() error = %v", err)
	}

	ts := clockServer(t, f, clock, Options{Project: project.ID})
	var doc sessionDoc
	getDoc(t, ts.URL+"/api/sessions", &doc)

	alive := findSession(t, doc, "s-run")
	if alive.State != "alive" {
		t.Fatalf("state = %q, want alive: a live run lease keeps its session alive", alive.State)
	}
	if alive.StoppedAt != "" {
		t.Fatalf("stopped_at = %q, want empty for a live session", alive.StoppedAt)
	}

	// The process dies: no heartbeat renews the lease, and with no fresh event
	// the session stops counting as alive.
	clock.t = clock.t.Add(2 * time.Minute)
	getDoc(t, ts.URL+"/api/sessions", &doc)
	stale := findSession(t, doc, "s-run")
	if stale.State != "stale" {
		t.Fatalf("state = %q, want stale after the run lease lapsed", stale.State)
	}
}

// TestSessionsLiveRunWithoutEventsAppears proves a session known only from a
// live run lease is on the page at all. A receiver registers its session and
// heartbeats while it works its first turn, before any event carries its id, so
// a page that lists events alone would show a working role as absent - the
// dashboard lying by omission. The run's registration is the only start evidence
// there is; once its lease lapses with no event to freeze, the session has no
// evidence left and leaves the page rather than sitting there as a zombie.
func TestSessionsLiveRunWithoutEventsAppears(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ctx := context.Background()

	clock := &stepClock{t: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	if _, err := app.NewMessageService(f.backend, clock, f.ids).RegisterRun(ctx, app.RegisterRunInput{
		ProjectID: project.ID,
		ActorID:   core.ActorID("chief"),
		Session:   "s-run-only",
		TTL:       time.Minute,
	}); err != nil {
		t.Fatalf("RegisterRun() error = %v", err)
	}
	clock.t = clock.t.Add(30 * time.Second)

	ts := clockServer(t, f, clock, Options{Project: project.ID})
	var doc sessionDoc
	getDoc(t, ts.URL+"/api/sessions", &doc)

	if len(doc.Sessions) != 1 {
		t.Fatalf("sessions = %+v, want the live run's event-free session", doc.Sessions)
	}
	view := findSession(t, doc, "s-run-only")
	if view.State != "alive" {
		t.Fatalf("state = %q, want alive: the run's lease is live", view.State)
	}
	if view.StartedAt != "2026-01-01T09:00:00Z" {
		t.Fatalf("started_at = %q, want the run's registration time", view.StartedAt)
	}
	if view.DurationSeconds != 30 {
		t.Fatalf("duration = %d, want 30 (now - registration)", view.DurationSeconds)
	}
	if view.LastEventAt != "" {
		t.Fatalf("last_event_at = %q, want empty: the session has no events", view.LastEventAt)
	}
	if view.Delivered.Tasks == nil || view.Delivered.PRs == nil {
		t.Fatalf("delivered = %+v, want empty arrays", view.Delivered)
	}

	// The lease lapses with no event to fall back on, so nothing on the page
	// claims the session is still there.
	clock.t = clock.t.Add(2 * time.Minute)
	getDoc(t, ts.URL+"/api/sessions", &doc)
	if len(doc.Sessions) != 0 {
		t.Fatalf("sessions = %+v, want none once the run-only lease lapsed", doc.Sessions)
	}
}

// TestSessionsLiveRunMergesWithEventSession proves the run-derived and
// event-derived evidence is unioned by session id, not concatenated: two live
// runs under one session (a chief and its builder) plus the session's own events
// make one entry, and the event timeline still owns the start and last activity
// rather than a later registration rewriting them. A retirement also still wins
// over the live runs, so folding them in cannot resurrect a dead session.
func TestSessionsLiveRunMergesWithEventSession(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ctx := context.Background()

	clock := &stepClock{t: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	sessions := app.NewSessionService(f.backend, clock, f.ids)
	if _, err := sessions.Start(ctx, project.ID, "s-both"); err != nil {
		t.Fatalf("Start(s-both) error = %v", err)
	}
	clock.t = clock.t.Add(5 * time.Minute)

	messages := app.NewMessageService(f.backend, clock, f.ids)
	for _, actor := range []core.ActorID{"chief", "builder-t-x"} {
		if _, err := messages.RegisterRun(ctx, app.RegisterRunInput{
			ProjectID: project.ID,
			ActorID:   actor,
			Session:   "s-both",
			TTL:       time.Minute,
		}); err != nil {
			t.Fatalf("RegisterRun(%s) error = %v", actor, err)
		}
	}

	ts := clockServer(t, f, clock, Options{Project: project.ID})
	var doc sessionDoc
	getDoc(t, ts.URL+"/api/sessions", &doc)

	if len(doc.Sessions) != 1 {
		t.Fatalf("sessions = %+v, want one entry for the session's two runs and its events", doc.Sessions)
	}
	view := findSession(t, doc, "s-both")
	if view.StartedAt != "2026-01-01T09:00:00Z" {
		t.Fatalf("started_at = %q, want the recorded session start", view.StartedAt)
	}
	if view.LastEventAt != "2026-01-01T09:00:00Z" {
		t.Fatalf("last_event_at = %q, want the session's own last event", view.LastEventAt)
	}

	// The runs are still within their leases when the session is retired, and
	// the explicit retirement wins over them.
	if _, err := sessions.Retire(ctx, project.ID, "s-both", ""); err != nil {
		t.Fatalf("Retire() error = %v", err)
	}
	getDoc(t, ts.URL+"/api/sessions", &doc)
	retired := findSession(t, doc, "s-both")
	if retired.State != "retired" {
		t.Fatalf("state = %q, want retired: a live run does not resurrect a retired session", retired.State)
	}
}

// TestSessionsRemoteRegisterKeepsSessionAlive proves the hub's HTTP register is
// a liveness signal on the remote path too: a receiver that registers its chief
// session over /api/msg/register and only heartbeats - no events for longer than
// the idle window - keeps that session alive, and it goes stale once the
// receiver stops and the lease lapses.
func TestSessionsRemoteRegisterKeepsSessionAlive(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ctx := context.Background()

	clock := &stepClock{t: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	if _, err := app.NewSessionService(f.backend, clock, f.ids).Start(ctx, project.ID, "s-remote"); err != nil {
		t.Fatalf("Start(s-remote) error = %v", err)
	}
	// A remote receiver talks to this hub over HTTP instead of the local store,
	// so registration and heartbeats go through the transport. Its own service
	// shares the test clock, which is what makes the idle window measurable.
	messages := app.NewMessageService(f.backend, clock, f.ids)
	ts := clockServer(t, f, clock, Options{Project: project.ID, Token: testToken, Messages: messages})

	// The chief delegates and goes quiet past the idle window.
	clock.t = clock.t.Add(2 * app.DefaultSessionIdle)
	registered := decodeMsg[msgRegisterResponse](t, postMsg(t, ts.URL+"/api/msg/register", testToken,
		`{"actor":"chief","session":"s-remote","harness":"opencode2","ttl":"1m"}`))
	if registered.RunID == "" {
		t.Fatalf("register = %+v, want a run id", registered)
	}

	var doc sessionDoc
	getDoc(t, ts.URL+"/api/sessions", &doc)
	alive := findSession(t, doc, "s-remote")
	if alive.State != "alive" {
		t.Fatalf("state = %q, want alive: the remote run's session is a live lease", alive.State)
	}

	// It keeps working, heartbeating past another whole idle window without
	// recording an event of its own.
	clock.t = clock.t.Add(2 * app.DefaultSessionIdle)
	if out := decodeMsg[msgOKResponse](t, postMsg(t, ts.URL+"/api/msg/heartbeat", testToken,
		`{"run_id":"`+registered.RunID+`","ttl":"1m"}`)); !out.OK {
		t.Fatalf("heartbeat = %+v, want ok", out)
	}
	getDoc(t, ts.URL+"/api/sessions", &doc)
	if again := findSession(t, doc, "s-remote"); again.State != "alive" {
		t.Fatalf("state = %q, want alive after the heartbeat", again.State)
	}

	// The remote process dies: no heartbeat renews the lease, and with no fresh
	// event the session stops counting as alive.
	clock.t = clock.t.Add(2 * time.Minute)
	getDoc(t, ts.URL+"/api/sessions", &doc)
	if stale := findSession(t, doc, "s-remote"); stale.State != "stale" {
		t.Fatalf("state = %q, want stale after the lease lapsed", stale.State)
	}
}

// TestSessionsQuietSessionStaysAlive proves the idle window is wider than a
// chief's normal quiet spell: a chief that delegated a task and is waiting for
// the builder emits no events for 20-40 minutes, and must not read as dead.
func TestSessionsQuietSessionStaysAlive(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ctx := context.Background()

	clock := &stepClock{t: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	sessions := app.NewSessionService(f.backend, clock, f.ids)
	if _, err := sessions.Start(ctx, project.ID, "s-quiet"); err != nil {
		t.Fatalf("Start(s-quiet) error = %v", err)
	}
	clock.t = clock.t.Add(30 * time.Minute)

	ts := clockServer(t, f, clock, Options{Project: project.ID})
	var doc sessionDoc
	getDoc(t, ts.URL+"/api/sessions", &doc)

	quiet := findSession(t, doc, "s-quiet")
	if quiet.State != "alive" {
		t.Fatalf("state = %q, want alive for a session quiet for 30m", quiet.State)
	}
	if quiet.LastEventAt != "2026-01-01T09:00:00Z" {
		t.Fatalf("last_event_at = %q, want the start event", quiet.LastEventAt)
	}
}

// TestSessionsRetiredWinsOverLiveRun pins the reincarnation teardown shape: the
// outgoing chief's receiver can still be heartbeating when the successor retires
// it, and the explicit retirement must win over the run lease.
func TestSessionsRetiredWinsOverLiveRun(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ctx := context.Background()

	clock := &stepClock{t: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	sessions := app.NewSessionService(f.backend, clock, f.ids)
	if _, err := sessions.Start(ctx, project.ID, "s-outgoing"); err != nil {
		t.Fatalf("Start(s-outgoing) error = %v", err)
	}
	run, err := app.NewMessageService(f.backend, clock, f.ids).RegisterRun(ctx, app.RegisterRunInput{
		ProjectID: project.ID,
		ActorID:   core.ActorID("chief"),
		Session:   "s-outgoing",
		TTL:       time.Minute,
	})
	if err != nil {
		t.Fatalf("RegisterRun() error = %v", err)
	}
	if !run.Live(clock.t) {
		t.Fatalf("run lease = %v, want live at %v", run.LeaseUntil, clock.t)
	}
	if _, err := sessions.Retire(ctx, project.ID, "s-outgoing", "s-next"); err != nil {
		t.Fatalf("Retire() error = %v", err)
	}

	ts := clockServer(t, f, clock, Options{Project: project.ID})
	var doc sessionDoc
	getDoc(t, ts.URL+"/api/sessions", &doc)

	outgoing := findSession(t, doc, "s-outgoing")
	if outgoing.State != "retired" {
		t.Fatalf("state = %q, want retired: the explicit retirement wins over a live run", outgoing.State)
	}
	if outgoing.Successor != "s-next" {
		t.Fatalf("successor = %q, want s-next", outgoing.Successor)
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
