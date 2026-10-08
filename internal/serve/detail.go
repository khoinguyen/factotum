package serve

import (
	"errors"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// detailData is the state one drill-down page renders. Exactly one of Idea,
// Task, or Artifact is set.
type detailData struct {
	CSS        template.CSS
	Project    string
	Title      string
	Idea       *ideaView
	Ticket     *taskDetail
	Origin     *taskLink
	Artifact   *artifactDetail
	AttachedTo *taskLink
}

// taskDetail is a full task projection for /task/<id>.
type taskDetail struct {
	taskLink
	Kind        string
	Repo        string
	Assignee    string
	Groomed     bool
	Description string
	Acceptance  []string
	Deps        []taskLink
	Dependents  []taskLink
	Notes       []noteView
	Artifacts   []artifactView
	CreatedAt   string
	UpdatedAt   string
}

// artifactDetail is a full artifact projection for /memory/<id> and /doc/<id>.
type artifactDetail struct {
	artifactView
	Links     []linkView
	CreatedAt string
	UpdatedAt string
}

// idFromPath returns the single path segment after prefix, or "" when it is
// missing or carries extra segments.
func idFromPath(path, prefix string) string {
	id := strings.TrimPrefix(path, prefix)
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

// handleIdea renders the drill-down from an idea to its promoted tasks and
// artifacts. Only an idea id resolves here.
func (s *Server) handleIdea(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/idea/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	task, err := s.options.Backend.Tickets().Get(r.Context(), core.TicketID(id))
	if errors.Is(err, core.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "dashboard: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !task.IsIdea() {
		http.NotFound(w, r)
		return
	}

	vs, err := s.loadViews(r, task.ProjectID)
	if err != nil {
		http.Error(w, "dashboard: "+err.Error(), http.StatusInternalServerError)
		return
	}
	idea := s.rollupIdea(*task, vs)
	s.render(w, "idea-page", &detailData{
		CSS:     dashboardCSS,
		Project: string(task.ProjectID),
		Title:   task.Title,
		Idea:    &idea,
	})
}

// handleTask renders the full detail of one executable task. Ideas use
// /idea/<id>; a task id under /task is rejected so the two stay distinct.
func (s *Server) handleTask(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/task/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	task, err := s.options.Backend.Tickets().Get(r.Context(), core.TicketID(id))
	if errors.Is(err, core.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "dashboard: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if task.IsIdea() {
		http.NotFound(w, r)
		return
	}

	vs, err := s.loadViews(r, task.ProjectID)
	if err != nil {
		http.Error(w, "dashboard: "+err.Error(), http.StatusInternalServerError)
		return
	}
	detail := &detailData{
		CSS:     dashboardCSS,
		Project: string(task.ProjectID),
		Title:   task.Title,
		Ticket:  taskDetailOf(*task, vs),
	}
	if o, ok := vs.origin[task.ID]; ok {
		view := vs.views[o]
		link := taskLinkOf(view)
		detail.Origin = &link
	}
	s.render(w, "task-page", detail)
}

// handleMemory renders a memory artifact. Only memory kind resolves.
func (s *Server) handleMemory(w http.ResponseWriter, r *http.Request) {
	s.handleArtifact(w, r, "/memory/", core.ArtifactMemory)
}

// handleDoc renders a spec or doc artifact. Only those kinds resolve.
func (s *Server) handleDoc(w http.ResponseWriter, r *http.Request) {
	s.handleArtifact(w, r, "/doc/", core.ArtifactSpec, core.ArtifactDoc)
}

func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request, prefix string, kinds ...core.ArtifactKind) {
	id := idFromPath(r.URL.Path, prefix)
	if id == "" {
		http.NotFound(w, r)
		return
	}
	artifact, err := s.options.Backend.Artifacts().Get(r.Context(), core.ArtifactID(id))
	if errors.Is(err, core.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "dashboard: "+err.Error(), http.StatusInternalServerError)
		return
	}
	allowed := false
	for _, kind := range kinds {
		if artifact.Kind == kind {
			allowed = true
			break
		}
	}
	if !allowed {
		http.NotFound(w, r)
		return
	}

	// Only memory and doc artifacts have a page; a derived check cache does not.
	detail := &detailData{
		CSS:      dashboardCSS,
		Project:  string(artifact.ProjectID),
		Title:    artifact.Title,
		Artifact: artifactDetailOf(artifact),
	}
	if artifact.TicketID != nil {
		if task, err := s.options.Backend.Tickets().Get(r.Context(), *artifact.TicketID); err == nil {
			link := taskRefLink(task)
			detail.AttachedTo = &link
		} else if !errors.Is(err, core.ErrNotFound) {
			http.Error(w, "dashboard: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	s.render(w, "artifact-page", detail)
}

// loadViews loads the project's tasks and artifacts and projects them. Both the
// idea and task drill-downs read through it so they agree with the index.
func (s *Server) loadViews(r *http.Request, projectID core.ProjectID) (viewSet, error) {
	snapshot, err := app.LoadSnapshot(r.Context(), s.options.Backend, projectID, s.options.Clock.Now())
	if err != nil {
		return viewSet{}, err
	}
	artifacts, err := s.options.Backend.Artifacts().List(r.Context(), store.ArtifactFilter{ProjectID: projectID})
	if err != nil {
		return viewSet{}, err
	}
	return s.buildViews(snapshot, artifacts), nil
}

// taskDetailOf projects one task into its full detail, resolving links through
// the already-loaded view set.
func taskDetailOf(task core.Ticket, vs viewSet) *taskDetail {
	view := vs.views[task.ID]
	detail := &taskDetail{
		taskLink:    taskLinkOf(view),
		Kind:        string(task.Kind),
		Repo:        task.Repo,
		Assignee:    view.Assignee,
		Groomed:     task.Groomed,
		Description: task.Description,
		Acceptance:  task.AcceptanceCriteria,
		Artifacts:   vs.artifactsByTask[task.ID],
		CreatedAt:   task.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   task.UpdatedAt.UTC().Format(time.RFC3339),
	}
	for _, dep := range task.Deps {
		detail.Deps = append(detail.Deps, taskLinkOf(vs.views[dep]))
	}
	for _, dependent := range vs.snapshot.Graph.Dependents(task.ID) {
		detail.Dependents = append(detail.Dependents, taskLinkOf(vs.views[dependent]))
	}
	for _, note := range task.Notes {
		detail.Notes = append(detail.Notes, noteViewOf(note, vs))
	}
	return detail
}

func taskRefLink(task *core.Ticket) taskLink {
	class := classify(*task, false, false, false)
	return taskLink{
		ID:    task.ID,
		Title: task.Title,
		Class: class,
		Chip:  chip(class),
		URL:   taskOrIdeaURL(*task),
	}
}

func artifactDetailOf(artifact *core.Artifact) *artifactDetail {
	detail := &artifactDetail{
		artifactView: artifactViewOf(artifact),
		CreatedAt:    artifact.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:    artifact.UpdatedAt.UTC().Format(time.RFC3339),
	}
	for _, link := range artifact.Links {
		detail.Links = append(detail.Links, linkView{Kind: string(link.Kind), URL: link.URL, Title: link.Title})
	}
	return detail
}

func noteViewOf(note core.Note, vs viewSet) noteView {
	out := noteView{
		Author:    noteAuthor(vs.actors, note.Author),
		Body:      note.Body,
		CreatedAt: note.CreatedAt.UTC().Format(time.RFC3339),
		System:    note.System,
	}
	for _, link := range note.Links {
		out.Links = append(out.Links, linkView{Kind: string(link.Kind), URL: link.URL, Title: link.Title})
	}
	return out
}

func noteAuthor(actors map[core.ActorID]string, id core.ActorID) string {
	if name, ok := actors[id]; ok {
		return name
	}
	return string(id)
}
