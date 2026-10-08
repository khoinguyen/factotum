// Package serve hosts the PO dashboard behind `ft serve`. It is a delivery
// adapter: it loads project state through pkg/app (which reuses pkg/graph
// readiness), ranks startable work through pkg/rank, and watches the event log
// to push live updates over SSE. Reads are open.
//
// It is one app with a write side too: /capture stores a natural-language idea
// through the same pkg/app TicketService, gated by a shared token. The token is
// the only authentication; the read side stays open and live. Enrichment of a
// captured idea is deferred to the grooming step, so a capture stores the raw
// sentence immediately.
//
// The dashboard is idea-centric: ideas are the primary unit, rolled up from the
// tasks promoted from them (the origin edge), with drill-down to a task, idea,
// memory, or doc detail page.
package serve

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/graph"
	"github.com/khoinguyen/factotum/pkg/rank"
	"github.com/khoinguyen/factotum/pkg/store"
)

//go:embed dashboard.html detail.html capture.html dashboard.css
var templateFS embed.FS

var templates = template.Must(template.ParseFS(templateFS, "dashboard.html", "detail.html", "capture.html"))

// dashboardCSS is injected verbatim into each page's <style>. It is served from
// the same embedded file so the index and detail pages cannot drift.
var dashboardCSS = func() template.CSS {
	css, err := templateFS.ReadFile("dashboard.css")
	if err != nil {
		panic("serve: embedded dashboard.css: " + err.Error())
	}
	return template.CSS(css)
}()

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
	// Token gates the write side. Empty disables capture (writes fail closed).
	// The read side is never gated by it.
	Token string
	// Tasks is the app service that stores a captured idea. Nil disables
	// capture. It is required only for writes, so a read-only server may omit it.
	Tasks *app.TicketService
	// Poll is how often the event log is checked for a change. It defaults to
	// 500ms, comfortably under the one-second freshness target.
	Poll time.Duration
	// Recent is how many recent events the updates section shows.
	Recent int
}

// Server is the dashboard HTTP handler: open reads plus the token-gated
// capture write.
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

// Handler returns the dashboard HTTP handler. The read pages accept GET and
// HEAD; the capture page accepts GET/HEAD and POST, and every other method is
// rejected with 405.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.route)
	return mux
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == "/":
		s.getOrHead(s.handleIndex)(w, r)
	case path == "/fragment":
		s.getOrHead(s.handleFragment)(w, r)
	case path == "/events":
		s.getOnly(s.handleEvents)(w, r)
	case path == "/capture":
		s.routeCapture(w, r)
	case strings.HasPrefix(path, "/idea/"):
		s.getOrHead(s.handleIdea)(w, r)
	case strings.HasPrefix(path, "/task/"):
		s.getOrHead(s.handleTask)(w, r)
	case strings.HasPrefix(path, "/memory/"):
		s.getOrHead(s.handleMemory)(w, r)
	case strings.HasPrefix(path, "/doc/"):
		s.getOrHead(s.handleDoc)(w, r)
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
	s.render(w, "dashboard.html", page)
}

func (s *Server) handleFragment(w http.ResponseWriter, r *http.Request) {
	page, err := s.page(r.Context())
	if err != nil {
		http.Error(w, "dashboard: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "dashboard-body", page)
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := templates.ExecuteTemplate(w, name, data); err != nil {
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
	Ideas      int
	Done       int
	ReadyAgent int
	ReadyHuman int
	Blocked    int
	Cycles     int
	Waves      int
}

// taskView is one executable task projected for the work board and next-up lists.
type taskView struct {
	ID          core.TicketID
	Title       string
	Repo        string
	Class       string
	Chip        string
	Reason      string
	Detail      string
	Assignee    string
	Wave        int
	Unblocks    int
	Milestone   bool
	Score       float64
	URL         string
	Origin      core.TicketID
	OriginTitle string
}

type updateView struct {
	Time    string
	Summary string
}

// artifactView is one artifact projected for a link chip or a detail page.
type artifactView struct {
	ID    core.ArtifactID
	Kind  string
	Title string
	Brief string
	Body  string
	URL   string
}

// taskLink is a compact reference to a task, used inside idea and task detail
// pages where the full work-board projection is unnecessary.
type taskLink struct {
	ID     core.TicketID
	Title  string
	Class  string
	Chip   string
	Reason string
	Detail string
	URL    string
}

// noteView is one note rendered on a task detail page.
type noteView struct {
	Author    string
	Body      string
	CreatedAt string
	System    bool
	Links     []linkView
}

type linkView struct {
	Kind  string
	URL   string
	Title string
}

// ideaView is one idea rolled up from the tasks promoted from it. State is one
// of finished, active, blocked, or captured.
type ideaView struct {
	ID          core.TicketID
	Title       string
	Description string
	Repo        string
	State       string
	Chip        string
	URL         string
	Total       int
	Done        int
	Active      int
	Blocked     int
	Tasks       []taskLink
	Artifacts   []artifactView
}

// taskGroup is a set of tasks sharing an origin idea. Ungrouped marks the bucket
// for tasks with no origin idea.
type taskGroup struct {
	IdeaID    core.TicketID
	IdeaTitle string
	Ungrouped bool
	Tasks     []taskView
}

// laneView is one ideas-board lane (blocked, active, finished, captured).
type laneView struct {
	Name  string
	Key   string
	Ideas []ideaView
}

// columnView is one work-board column (in-progress or waiting).
type columnView struct {
	Name   string
	Key    string
	Count  int
	Groups []taskGroup
}

// pageData is the fully-derived dashboard state. It carries no storage handle.
type pageData struct {
	Title          string
	Project        string
	Snapshot       string
	CSS            template.CSS
	Stats          pageStats
	NextAgent      []taskView
	NextHuman      []taskView
	InFlight       []taskView
	Waiting        []taskView
	InFlightGroups []taskGroup
	WaitingGroups  []taskGroup
	BlockedIdeas   []ideaView
	ActiveIdeas    []ideaView
	FinishedIdeas  []ideaView
	CapturedIdeas  []ideaView
	Updates        []updateView
	Flags          []string
}

// Lane returns the ideas-board lane for key. It keeps the template free of
// conditionals over the four fixed lanes.
func (p *pageData) Lane(key string) laneView {
	switch key {
	case "blocked":
		return laneView{Name: "Blocked — needs unblock", Key: key, Ideas: p.BlockedIdeas}
	case "active":
		return laneView{Name: "In progress", Key: key, Ideas: p.ActiveIdeas}
	case "finished":
		return laneView{Name: "Finished", Key: key, Ideas: p.FinishedIdeas}
	default:
		return laneView{Name: "Captured", Key: key, Ideas: p.CapturedIdeas}
	}
}

// Column returns the work-board column for key.
func (p *pageData) Column(key string) columnView {
	if key == "waiting" {
		return columnView{Name: "Waiting / blocked", Key: key, Count: len(p.Waiting), Groups: p.WaitingGroups}
	}
	return columnView{Name: "In progress", Key: key, Count: len(p.InFlight), Groups: p.InFlightGroups}
}

// page loads and projects the current state. Readiness and ranking are reused
// from pkg/graph and pkg/rank; nothing here re-scores tasks.
func (s *Server) page(ctx context.Context) (*pageData, error) {
	now := s.options.Clock.Now()
	var (
		snapshot *app.Snapshot
		scope    core.ProjectID
	)
	if s.options.All {
		var err error
		snapshot, err = app.LoadAllSnapshot(ctx, s.options.Backend, now)
		if err != nil {
			return nil, err
		}
	} else {
		scope = s.options.Project
		var err error
		snapshot, err = app.LoadSnapshot(ctx, s.options.Backend, scope, now)
		if err != nil {
			return nil, err
		}
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

	artifacts, err := s.options.Backend.Artifacts().List(ctx, store.ArtifactFilter{ProjectID: scope})
	if err != nil {
		return nil, err
	}

	vs := s.buildViews(snapshot, artifacts)
	page := &pageData{
		Title:    s.title(snapshot),
		Project:  s.projectLabel(snapshot),
		Snapshot: now.UTC().Format(time.RFC3339),
		CSS:      dashboardCSS,
	}
	page.Stats = pageStats{
		Scope:  len(vs.ids),
		Cycles: len(vs.cycles),
	}
	if deep, err := snapshot.Graph.WavesDeep(); err == nil {
		page.Stats.Waves = deep
	}
	for _, id := range vs.ids {
		switch vs.byID[id].Status {
		case core.StatusDone:
			page.Stats.Done++
		case core.StatusBlocked:
			page.Stats.Blocked++
		}
		if vs.byID[id].IsIdea() {
			page.Stats.Ideas++
		}
	}

	for _, score := range scored {
		view := vs.views[score.TicketID]
		view.Score = score.Score
		switch {
		case vs.agentSet[score.TicketID]:
			page.NextAgent = append(page.NextAgent, view)
		case vs.humanSet[score.TicketID]:
			page.NextHuman = append(page.NextHuman, view)
		}
	}
	// The header stats count the rows the sections actually list, so a ranker
	// that omits a ready task cannot make a count exceed its list.
	page.Stats.ReadyAgent = len(page.NextAgent)
	page.Stats.ReadyHuman = len(page.NextHuman)

	for _, id := range vs.ids {
		task := vs.byID[id]
		view := vs.views[id]
		ready, reason := snapshot.Graph.Readiness(id)
		switch {
		case task.IsIdea():
		case task.Status == core.StatusInProgress || task.Status == core.StatusReadyForReview:
			page.InFlight = append(page.InFlight, view)
		case ready:
		case reason != nil:
			view.Reason = string(reason.Code)
			view.Detail = reason.Detail
			page.Waiting = append(page.Waiting, view)
		}
	}

	page.InFlightGroups = groupByOrigin(page.InFlight)
	page.WaitingGroups = groupByOrigin(page.Waiting)

	for _, id := range vs.ids {
		task := vs.byID[id]
		if !task.IsIdea() {
			continue
		}
		idea := s.rollupIdea(task, vs)
		switch idea.State {
		case "blocked":
			page.BlockedIdeas = append(page.BlockedIdeas, idea)
		case "finished":
			page.FinishedIdeas = append(page.FinishedIdeas, idea)
		case "active":
			page.ActiveIdeas = append(page.ActiveIdeas, idea)
		default:
			page.CapturedIdeas = append(page.CapturedIdeas, idea)
		}
	}

	for _, cycle := range vs.cycles {
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

// viewSet is the shared projection of one snapshot used by both the dashboard
// index and the drill-down pages, so grouping and rollups cannot drift.
type viewSet struct {
	snapshot        *app.Snapshot
	ids             []core.TicketID
	byID            map[core.TicketID]core.Ticket
	views           map[core.TicketID]taskView
	origin          map[core.TicketID]core.TicketID
	artifactsByTask map[core.TicketID][]artifactView
	waves           map[core.TicketID]int
	actors          map[core.ActorID]string
	agentSet        map[core.TicketID]bool
	humanSet        map[core.TicketID]bool
	cycles          [][]core.TicketID
}

// buildViews projects a snapshot into the ids, readiness-annotated task views,
// origin edges, and task-attached artifacts the pages render.
func (s *Server) buildViews(snapshot *app.Snapshot, artifacts []*core.Artifact) viewSet {
	ids := snapshot.Graph.IDs()
	byID := make(map[core.TicketID]core.Ticket, len(snapshot.Tasks))
	for _, task := range snapshot.Tasks {
		byID[task.ID] = *task
	}
	waves, _ := snapshot.Graph.Waves()
	cycles := snapshot.Graph.Cycles()
	cycleIDs := make(map[core.TicketID]bool)
	for _, cycle := range cycles {
		for _, id := range cycle {
			cycleIDs[id] = true
		}
	}
	vs := viewSet{
		snapshot: snapshot,
		ids:      ids,
		byID:     byID,
		views:    make(map[core.TicketID]taskView, len(ids)),
		origin:   make(map[core.TicketID]core.TicketID, len(ids)),
		waves:    waves,
		actors:   make(map[core.ActorID]string, len(snapshot.Actors)),
		agentSet: idSet(snapshot.Ready.Agent),
		humanSet: idSet(snapshot.Ready.Human),
		cycles:   cycles,
	}
	for id, actor := range snapshot.Actors {
		vs.actors[id] = actor.Name
	}
	// The origin edge is each task's first dependency that is an idea; promotion
	// writes exactly that edge, so it is what groups tasks under ideas.
	for _, id := range ids {
		if byID[id].IsIdea() {
			continue
		}
		if o, ok := firstIdeaDep(byID[id], byID); ok {
			vs.origin[id] = o
		}
	}
	for _, id := range ids {
		task := byID[id]
		class := classify(task, vs.agentSet[id], vs.humanSet[id], cycleIDs[id])
		view := taskView{
			ID:        task.ID,
			Title:     task.Title,
			Repo:      task.Repo,
			Class:     class,
			Chip:      chip(class),
			Assignee:  actorName(snapshot.Actors, task.AssigneeID),
			Wave:      waves[id],
			Unblocks:  snapshot.Graph.UnblockCount(id),
			Milestone: task.IsMilestone(),
			URL:       taskOrIdeaURL(task),
		}
		if !task.IsIdea() {
			if _, reason := snapshot.Graph.Readiness(id); reason != nil && reason.Code != graph.ReasonInProgress {
				view.Reason = string(reason.Code)
				view.Detail = reason.Detail
			}
		}
		if o, ok := vs.origin[id]; ok {
			view.Origin = o
			if oTask, ok := byID[o]; ok {
				view.OriginTitle = oTask.Title
			}
		}
		vs.views[id] = view
	}
	vs.artifactsByTask = make(map[core.TicketID][]artifactView)
	for _, artifact := range artifacts {
		if artifact.TicketID == nil {
			continue
		}
		vs.artifactsByTask[*artifact.TicketID] = append(vs.artifactsByTask[*artifact.TicketID], artifactViewOf(artifact))
	}
	return vs
}

// rollupIdea builds the idea card from the tasks promoted from it and the
// artifacts attached to the idea or any of those tasks.
func (s *Server) rollupIdea(idea core.Ticket, vs viewSet) ideaView {
	out := ideaView{
		ID:          idea.ID,
		Title:       idea.Title,
		Description: idea.Description,
		Repo:        idea.Repo,
		URL:         ideaURL(idea.ID),
	}
	var promoted []core.TicketID
	for _, id := range vs.ids {
		if vs.origin[id] == idea.ID {
			promoted = append(promoted, id)
		}
	}
	for _, id := range promoted {
		out.Tasks = append(out.Tasks, taskLinkOf(vs.views[id]))
		out.Artifacts = append(out.Artifacts, vs.artifactsByTask[id]...)
		switch taskState(vs.byID[id], vs.views[id]) {
		case "finished":
			out.Done++
		case "blocked":
			out.Blocked++
		default:
			out.Active++
		}
	}
	out.Artifacts = append(out.Artifacts, vs.artifactsByTask[idea.ID]...)
	out.Total = len(promoted)
	out.State, out.Chip = ideaRollup(out)
	return out
}

// taskState reduces one promoted task to its rollup contribution. A task is
// finished once it resolves, blocked when it is explicitly blocked or waiting on
// an unresolved condition, and active otherwise.
func taskState(task core.Ticket, view taskView) string {
	switch {
	case task.Status == core.StatusDone || task.Status == core.StatusCancelled:
		return "finished"
	case task.Status == core.StatusBlocked:
		return "blocked"
	case view.Reason != "":
		return "blocked"
	default:
		return "active"
	}
}

// ideaRollup classifies an idea from the state of its promoted tasks. An idea
// with no promoted work is captured; all done is finished; any blocked task
// makes it blocked (a needs-unblock signal outranks work still moving);
// otherwise it is active.
func ideaRollup(idea ideaView) (state, chip string) {
	switch {
	case idea.Total == 0:
		return "captured", "capture"
	case idea.Done == idea.Total:
		return "finished", "done"
	case idea.Blocked > 0:
		return "blocked", "blocked"
	default:
		return "active", "ready-agent"
	}
}

// firstIdeaDep returns the first dependency of task that is an idea, matching
// pkg/app's origin rule. It is skipped when the dependency dangles.
func firstIdeaDep(task core.Ticket, byID map[core.TicketID]core.Ticket) (core.TicketID, bool) {
	for _, dep := range task.Deps {
		if depTask, ok := byID[dep]; ok && depTask.IsIdea() {
			return dep, true
		}
	}
	return "", false
}

// groupByOrigin groups tasks under the idea they were promoted from, with the
// ungrouped bucket last. Groups are ordered deterministically by idea id.
func groupByOrigin(tasks []taskView) []taskGroup {
	order := make([]core.TicketID, 0)
	byIdea := make(map[core.TicketID]*taskGroup)
	for _, task := range tasks {
		group, ok := byIdea[task.Origin]
		if !ok {
			group = &taskGroup{IdeaID: task.Origin, IdeaTitle: task.OriginTitle, Ungrouped: task.Origin == ""}
			byIdea[task.Origin] = group
			order = append(order, task.Origin)
		}
		group.Tasks = append(group.Tasks, task)
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i] == "" {
			return false
		}
		if order[j] == "" {
			return true
		}
		return order[i] < order[j]
	})
	out := make([]taskGroup, 0, len(order))
	for _, id := range order {
		out = append(out, *byIdea[id])
	}
	return out
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
func classify(task core.Ticket, readyAgent, readyHuman, cycle bool) string {
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

func idSet(ids []core.TicketID) map[core.TicketID]bool {
	set := make(map[core.TicketID]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}
func taskURL(id core.TicketID) string { return "/task/" + string(id) }
func ideaURL(id core.TicketID) string { return "/idea/" + string(id) }

// taskOrIdeaURL routes an idea to its own detail page and executable work to
// the task page.
func taskOrIdeaURL(task core.Ticket) string {
	if task.IsIdea() {
		return ideaURL(task.ID)
	}
	return taskURL(task.ID)
}

func artifactURL(artifact *core.Artifact) string {
	if artifact.Kind == core.ArtifactMemory {
		return "/memory/" + string(artifact.ID)
	}
	return "/doc/" + string(artifact.ID)
}

func artifactViewOf(artifact *core.Artifact) artifactView {
	return artifactView{
		ID:    artifact.ID,
		Kind:  string(artifact.Kind),
		Title: artifact.Title,
		Brief: artifact.Brief,
		Body:  artifact.Body,
		URL:   artifactURL(artifact),
	}
}

func taskLinkOf(view taskView) taskLink {
	return taskLink{
		ID:     view.ID,
		Title:  view.Title,
		Class:  view.Class,
		Chip:   view.Chip,
		Reason: view.Reason,
		Detail: view.Detail,
		URL:    view.URL,
	}
}
