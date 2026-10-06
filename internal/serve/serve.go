// Package serve hosts the read-only, auto-reloading factory dashboard behind
// `ft serve`. It is a delivery adapter: it loads project state through pkg/app
// (which reuses pkg/graph readiness), ranks startable work through pkg/rank, and
// watches the event log to push live updates over SSE. It exposes no mutating
// endpoint.
package serve

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/graph"
	"github.com/khoinguyen/factotum/pkg/rank"
	"github.com/khoinguyen/factotum/pkg/store"
)

//go:embed dashboard.html
var templateFS embed.FS

var dashboardTemplate = template.Must(template.ParseFS(templateFS, "dashboard.html"))

const (
	defaultPoll   = 500 * time.Millisecond
	defaultRecent = 20
)

// ReasonDepUnresolved is the readiness reason for a task waiting on an
// unresolved dependency. It mirrors pkg/graph so a caller need not reach into
// the graph package to name the waiting state.
const ReasonDepUnresolved = string(graph.ReasonDepUnresolved)

// Options configures the dashboard server.
type Options struct {
	// Backend is the store to read. It is required and never written.
	Backend store.Backend
	// Clock supplies the "now" used to evaluate not_before readiness.
	Clock app.Clock
	// Ranker scores startable tasks. Nil falls back to the default composite
	// ranker, so ranking is never reimplemented here.
	Ranker rank.Ranker
	// Project scopes the dashboard to one project. It is ignored when All is set.
	Project core.ProjectID
	// All widens the dashboard to every registered project. With neither a
	// project nor All, the dashboard serves all projects.
	All bool
	// Poll is how often the event log is checked for a change. It defaults to
	// 500ms, comfortably under the one-second freshness target.
	Poll time.Duration
	// Recent is how many recent events the updates section shows.
	Recent int
}

// Server is the read-only dashboard HTTP handler.
type Server struct {
	options Options
	broker  *eventBroker
}

// New validates the options and returns a dashboard server.
func New(opts Options) (*Server, error) {
	if opts.Backend == nil {
		return nil, errors.New("serve: backend is required")
	}
	if opts.Clock == nil {
		opts.Clock = app.SystemClock{}
	}
	if opts.Ranker == nil {
		ranker, err := rank.Default()
		if err != nil {
			return nil, fmt.Errorf("serve: default ranker: %w", err)
		}
		opts.Ranker = ranker
	}
	if opts.Poll <= 0 {
		opts.Poll = defaultPoll
	}
	if opts.Recent <= 0 {
		opts.Recent = defaultRecent
	}
	if !opts.All && opts.Project == "" {
		opts.All = true
	}
	server := &Server{options: opts}
	server.broker = newEventBroker(opts.Poll, server.latestEventID)
	return server, nil
}

// Handler returns the read-only HTTP handler. Only GET (and HEAD, for the page)
// is accepted; every other method is rejected with 405.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.route)
	return mux
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/":
		s.getOrHead(s.handleIndex)(w, r)
	case "/fragment":
		s.getOrHead(s.handleFragment)(w, r)
	case "/events":
		s.getOnly(s.handleEvents)(w, r)
	default:
		http.NotFound(w, r)
	}
}

// getOrHead rejects any method other than GET or HEAD. It is the read-only
// boundary for the rendered pages.
func (s *Server) getOrHead(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w)
			return
		}
		next(w, r)
	}
}

// getOnly rejects any method other than GET. The SSE stream never answers a
// HEAD, which would otherwise block.
func (s *Server) getOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "read-only dashboard: method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next(w, r)
	}
}

func methodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", "GET, HEAD")
	http.Error(w, "read-only dashboard: method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	page, err := s.page(r.Context())
	if err != nil {
		http.Error(w, "dashboard: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := dashboardTemplate.ExecuteTemplate(w, "dashboard.html", page); err != nil {
		http.Error(w, "dashboard: "+err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleFragment(w http.ResponseWriter, r *http.Request) {
	page, err := s.page(r.Context())
	if err != nil {
		http.Error(w, "dashboard: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := dashboardTemplate.ExecuteTemplate(w, "dashboard-body", page); err != nil {
		http.Error(w, "dashboard: "+err.Error(), http.StatusInternalServerError)
	}
}

// handleEvents streams server-sent events. It sends a hello on connect, then an
// update whenever the newest event id changes, which is how a task status change
// reaches the page without a manual reload. Every client shares one broker poll,
// so many open dashboards do not multiply the load on the event log.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "dashboard: streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	_, _ = fmt.Fprint(w, "retry: 1000\nevent: hello\ndata: {}\n\n")
	flusher.Flush()

	updates, cancel := s.broker.subscribe()
	defer cancel()
	for {
		select {
		case <-r.Context().Done():
			return
		case id := <-updates:
			_, _ = fmt.Fprintf(w, "event: update\ndata: %s\n\n", id)
			flusher.Flush()
		}
	}
}

// latestEventID returns the id of the newest event in scope, or "" when the log
// is empty. A new mutation appends an event, so a changed id means the state the
// page shows is stale.
func (s *Server) latestEventID(ctx context.Context) (string, error) {
	filter := store.EventFilter{Limit: 1}
	if !s.options.All {
		filter.ProjectID = s.options.Project
	}
	events, err := s.options.Backend.Events().List(ctx, filter)
	if err != nil {
		return "", err
	}
	if len(events) == 0 {
		return "", nil
	}
	return string(events[0].ID), nil
}

// pageStats are the unambiguous graph counts shown in the header.
type pageStats struct {
	Scope      int
	Done       int
	ReadyAgent int
	ReadyHuman int
	Blocked    int
	Cycles     int
	Waves      int
}

// taskView is one task projected for display.
type taskView struct {
	ID        core.TaskID
	Title     string
	Repo      string
	Class     string
	Chip      string
	Reason    string
	Detail    string
	Assignee  string
	Wave      int
	Unblocks  int
	Milestone bool
	Score     float64
}

type updateView struct {
	Time    string
	Summary string
}

// pageData is the fully-derived dashboard state. It carries no storage handle.
type pageData struct {
	Title     string
	Project   string
	Snapshot  string
	Stats     pageStats
	NextAgent []taskView
	NextHuman []taskView
	InFlight  []taskView
	Waiting   []taskView
	Capture   []taskView
	Updates   []updateView
	Flags     []string
}

// page loads and projects the current state. Readiness and ranking are reused
// from pkg/graph and pkg/rank; nothing here re-scores tasks.
func (s *Server) page(ctx context.Context) (*pageData, error) {
	now := s.options.Clock.Now()
	var (
		snapshot *app.Snapshot
		scope    core.ProjectID
		err      error
	)
	if s.options.All {
		snapshot, err = app.LoadAllSnapshot(ctx, s.options.Backend, now)
	} else {
		scope = s.options.Project
		snapshot, err = app.LoadSnapshot(ctx, s.options.Backend, scope, now)
	}
	if err != nil {
		return nil, err
	}

	events, err := s.options.Backend.Events().List(ctx, store.EventFilter{
		ProjectID: scope,
		Limit:     s.options.Recent,
	})
	if err != nil {
		return nil, err
	}

	scored, err := s.options.Ranker.Rank(ctx, rank.Request{Graph: snapshot.Graph, Tasks: snapshot.Tasks})
	if err != nil {
		return nil, err
	}

	ids := snapshot.Graph.IDs()
	byID := make(map[core.TaskID]core.Task, len(snapshot.Tasks))
	for _, task := range snapshot.Tasks {
		byID[task.ID] = *task
	}
	waves, _ := snapshot.Graph.Waves()
	cycles := snapshot.Graph.Cycles()
	cycleIDs := make(map[core.TaskID]bool)
	for _, cycle := range cycles {
		for _, id := range cycle {
			cycleIDs[id] = true
		}
	}
	agentSet := idSet(snapshot.Ready.Agent)
	humanSet := idSet(snapshot.Ready.Human)

	page := &pageData{
		Title:    s.title(snapshot),
		Project:  s.projectLabel(snapshot),
		Snapshot: now.UTC().Format(time.RFC3339),
	}
	page.Stats = pageStats{
		Scope:      len(ids),
		ReadyAgent: len(snapshot.Ready.Agent),
		ReadyHuman: len(snapshot.Ready.Human),
		Cycles:     len(cycles),
	}
	if deep, err := snapshot.Graph.WavesDeep(); err == nil {
		page.Stats.Waves = deep
	}
	for _, id := range ids {
		if byID[id].Status == core.StatusDone {
			page.Stats.Done++
		}
		if byID[id].Status == core.StatusBlocked {
			page.Stats.Blocked++
		}
	}

	views := make(map[core.TaskID]taskView, len(ids))
	for _, id := range ids {
		task := byID[id]
		class := classify(task, agentSet[id], humanSet[id], cycleIDs[id])
		views[id] = taskView{
			ID:        task.ID,
			Title:     task.Title,
			Repo:      task.Repo,
			Class:     class,
			Chip:      chip(class),
			Assignee:  actorName(snapshot.Actors, task.AssigneeID),
			Wave:      waves[id],
			Unblocks:  snapshot.Graph.UnblockCount(id),
			Milestone: task.IsMilestone(),
		}
	}

	for _, score := range scored {
		view := views[score.TaskID]
		view.Score = score.Score
		switch {
		case agentSet[score.TaskID]:
			page.NextAgent = append(page.NextAgent, view)
		case humanSet[score.TaskID]:
			page.NextHuman = append(page.NextHuman, view)
		}
	}

	for _, id := range ids {
		task := byID[id]
		view := views[id]
		ready, reason := snapshot.Graph.Readiness(id)
		switch {
		case task.IsIdea():
			page.Capture = append(page.Capture, view)
		case task.Status == core.StatusInProgress || task.Status == core.StatusReadyForReview:
			page.InFlight = append(page.InFlight, view)
		case ready:
		case reason != nil:
			view.Reason = string(reason.Code)
			view.Detail = reason.Detail
			page.Waiting = append(page.Waiting, view)
		}
	}

	for _, cycle := range cycles {
		parts := make([]string, 0, len(cycle))
		for _, id := range cycle {
			parts = append(parts, string(id))
		}
		page.Flags = append(page.Flags, "cycle: "+strings.Join(parts, " -> "))
	}
	if external := snapshot.Graph.ExternalDeps(); len(external) > 0 {
		parts := make([]string, 0, len(external))
		for _, id := range external {
			parts = append(parts, string(id))
		}
		page.Flags = append(page.Flags, "external dependencies: "+strings.Join(parts, ", "))
	}

	for _, event := range events {
		page.Updates = append(page.Updates, updateView{
			Time:    event.CreatedAt.UTC().Format(time.RFC3339),
			Summary: event.Summary,
		})
	}
	return page, nil
}

func (s *Server) title(snapshot *app.Snapshot) string {
	if s.options.All || snapshot.Project == nil {
		return "All projects"
	}
	return snapshot.Project.Name + " dashboard"
}

func (s *Server) projectLabel(snapshot *app.Snapshot) string {
	if s.options.All || snapshot.Project == nil {
		return "all projects"
	}
	return snapshot.Project.Name
}

// classify maps a task to its visual class, reusing the readiness buckets the
// graph already computed rather than re-deriving readiness. It intentionally
// mirrors pkg/render's presentation classes instead of calling render.Classify,
// which would re-derive the whole view on every task; the class strings are CSS
// keys local to this template, so there is no behavior to keep in sync.
func classify(task core.Task, readyAgent, readyHuman, cycle bool) string {
	switch {
	case cycle:
		return "cycle"
	case task.IsIdea():
		return "capture"
	case task.Status == core.StatusCancelled:
		return "cancelled"
	case task.Status == core.StatusDone:
		return "done"
	case task.Status == core.StatusReadyForReview:
		return "review"
	case readyAgent:
		return "ready-agent"
	case readyHuman:
		return "ready-human"
	case task.Status == core.StatusBlocked:
		return "blocked"
	default:
		return "waiting"
	}
}

func chip(class string) string {
	switch class {
	case "done":
		return "done"
	case "cancelled":
		return "cancelled"
	case "review":
		return "in review"
	case "ready-agent":
		return "agent next"
	case "ready-human":
		return "human next"
	case "blocked":
		return "blocked"
	case "cycle":
		return "cycle"
	case "capture":
		return "capture"
	default:
		return "waiting"
	}
}

func actorName(actors map[core.ActorID]core.Actor, id *core.ActorID) string {
	if id == nil {
		return ""
	}
	if actor, ok := actors[*id]; ok {
		return actor.Name
	}
	return string(*id)
}

func idSet(ids []core.TaskID) map[core.TaskID]bool {
	set := make(map[core.TaskID]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}
