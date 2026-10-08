package serve

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// taskDetail is the full task projection for /api/task/<id>.
type taskDetail struct {
	taskLink
	Kind        string         `json:"kind"`
	Repo        string         `json:"repo,omitempty"`
	Assignee    string         `json:"assignee,omitempty"`
	Groomed     bool           `json:"groomed"`
	Description string         `json:"description,omitempty"`
	Acceptance  []string       `json:"acceptance"`
	Deps        []taskLink     `json:"deps"`
	Dependents  []taskLink     `json:"dependents"`
	Notes       []noteView     `json:"notes"`
	Artifacts   []artifactView `json:"artifacts"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
}

// taskPageJSON is the /api/task/<id> document: the task detail plus the idea it
// was promoted from, when any.
type taskPageJSON struct {
	taskDetail
	Origin *taskLink `json:"origin,omitempty"`
}

// artifactDetail is the full artifact projection for /api/memory/<id> and
// /api/doc/<id>.
type artifactDetail struct {
	artifactView
	Links     []linkView `json:"links"`
	CreatedAt string     `json:"created_at"`
	UpdatedAt string     `json:"updated_at"`
}

// artifactPageJSON is the /api/memory/<id> or /api/doc/<id> document: the
// artifact plus the task it is attached to, when any.
type artifactPageJSON struct {
	artifactDetail
	AttachedTo *taskLink `json:"attached_to,omitempty"`
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

// handleIdea serves the drill-down from an idea to its promoted tasks and
// artifacts as JSON. Only an idea id resolves here.
func (s *Server) handleIdea(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/idea/")
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
	if !task.Kind.CapturedByHuman() {
		http.NotFound(w, r)
		return
	}

	vs, err := s.loadViews(r, task.ProjectID)
	if err != nil {
		http.Error(w, "dashboard: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, s.rollupIdea(*task, vs))
}

// handleTask serves the full detail of one executable task as JSON. Ideas use
// /api/idea/<id>; a task id under /api/task is rejected so the two stay distinct.
func (s *Server) handleTask(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/task/")
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
	if task.Kind.CapturedByHuman() {
		http.NotFound(w, r)
		return
	}

	vs, err := s.loadViews(r, task.ProjectID)
	if err != nil {
		http.Error(w, "dashboard: "+err.Error(), http.StatusInternalServerError)
		return
	}
	page := taskPageJSON{taskDetail: *taskDetailOf(*task, vs)}
	if o, ok := vs.origin[task.ID]; ok {
		link := taskLinkOf(vs.views[o])
		page.Origin = &link
	}
	s.writeJSON(w, page)
}

// handleMemory serves a memory artifact. Only memory kind resolves.
func (s *Server) handleMemory(w http.ResponseWriter, r *http.Request) {
	s.handleArtifact(w, r, "/api/memory/", core.ArtifactMemory)
}

// handleDoc serves a spec or doc artifact. Only those kinds resolve.
func (s *Server) handleDoc(w http.ResponseWriter, r *http.Request) {
	s.handleArtifact(w, r, "/api/doc/", core.ArtifactSpec, core.ArtifactDoc)
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
	page := artifactPageJSON{artifactDetail: *artifactDetailOf(artifact)}
	if artifact.TicketID != nil {
		if task, err := s.options.Backend.Tickets().Get(r.Context(), *artifact.TicketID); err == nil {
			link := taskRefLink(task)
			page.AttachedTo = &link
		} else if !errors.Is(err, core.ErrNotFound) {
			http.Error(w, "dashboard: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	s.writeJSON(w, page)
}

// loadViews loads the project's tasks and artifacts and projects them. Both the
// idea and task drill-downs read through it so they agree with the dashboard.
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
		Acceptance:  nonNil(task.AcceptanceCriteria),
		Artifacts:   nonNil(vs.artifactsByTask[task.ID]),
		Deps:        []taskLink{},
		Dependents:  []taskLink{},
		Notes:       []noteView{},
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
		Links:        []linkView{},
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
		Links:     []linkView{},
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
