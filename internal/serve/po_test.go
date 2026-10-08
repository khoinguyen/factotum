package serve

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

func (f *fixture) addIdea(t *testing.T, projectID core.ProjectID, title, body string) *core.Ticket {
	t.Helper()
	task, err := f.tasks.Add(context.Background(), app.TicketInput{
		ProjectID:   projectID,
		Kind:        core.KindIdea,
		Title:       title,
		Description: body,
	})
	if err != nil {
		t.Fatalf("Add(idea %q) error = %v", title, err)
	}
	return task
}

func (f *fixture) promote(t *testing.T, ideaID core.TicketID) *core.Ticket {
	t.Helper()
	task, err := f.tasks.Promote(context.Background(), ideaID)
	if err != nil {
		t.Fatalf("Promote(%s) error = %v", ideaID, err)
	}
	return task
}

func (f *fixture) setStatus(t *testing.T, id core.TicketID, status core.TicketStatus) {
	t.Helper()
	if _, err := f.tasks.SetStatus(context.Background(), id, status); err != nil {
		t.Fatalf("SetStatus(%s, %s) error = %v", id, status, err)
	}
}

func (f *fixture) addArtifact(t *testing.T, projectID core.ProjectID, taskID *core.TicketID, kind core.ArtifactKind, title, body string) *core.Artifact {
	t.Helper()
	artifact, err := f.artifacts.Add(context.Background(), app.ArtifactInput{
		ProjectID: projectID,
		TicketID:  taskID,
		Kind:      kind,
		Title:     title,
		Body:      body,
	})
	if err != nil {
		t.Fatalf("Add(artifact %q) error = %v", title, err)
	}
	return artifact
}

func findIdea(ideas []ideaView, id core.TicketID) (ideaView, bool) {
	for _, idea := range ideas {
		if idea.ID == id {
			return idea, true
		}
	}
	return ideaView{}, false
}

func findGroup(groups []taskGroup, id core.TicketID) (taskGroup, bool) {
	for _, group := range groups {
		if group.IdeaID == id {
			return group, true
		}
	}
	return taskGroup{}, false
}

// TestIdeaRollupStates proves the ideas board classifies each idea by the state
// of the work promoted from it: finished when all promoted tasks are done,
// blocked when any is blocked, in-progress otherwise, and captured when nothing
// has been promoted yet. Ideas are the primary unit, so this is the core read
// of the dashboard.
func TestIdeaRollupStates(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")

	finished := f.addIdea(t, project.ID, "Finished idea", "shipped")
	finishedTask := f.promote(t, finished.ID)
	f.setStatus(t, finishedTask.ID, core.StatusDone)

	active := f.addIdea(t, project.ID, "Active idea", "being built")
	activeTask := f.promote(t, active.ID)
	f.setStatus(t, activeTask.ID, core.StatusInProgress)

	blocked := f.addIdea(t, project.ID, "Blocked idea", "stuck")
	blockedTask := f.promote(t, blocked.ID)
	f.setStatus(t, blockedTask.ID, core.StatusBlocked)

	captured := f.addIdea(t, project.ID, "Raw idea", "not refined yet")

	server, err := New(Options{Backend: f.backend, Clock: f.clock, Project: project.ID})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	page, err := server.page(context.Background())
	if err != nil {
		t.Fatalf("page() error = %v", err)
	}

	cases := []struct {
		name string
		id   core.TicketID
		from []ideaView
	}{
		{"finished", finished.ID, page.FinishedIdeas},
		{"active", active.ID, page.ActiveIdeas},
		{"blocked", blocked.ID, page.BlockedIdeas},
		{"captured", captured.ID, page.CapturedIdeas},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idea, ok := findIdea(tc.from, tc.id)
			if !ok {
				t.Fatalf("idea %s not in %s lane", tc.id, tc.name)
			}
			if idea.State != tc.name {
				t.Fatalf("idea %s state = %q, want %q", tc.id, idea.State, tc.name)
			}
		})
	}

	// The block is a needs-unblock signal: it outranks a sibling still being built.
	if got := page.ActiveIdeas[0].State; got != "active" {
		t.Fatalf("active lane state = %q", got)
	}
}

// TestTasksGroupedByOriginEdge proves the kanban groups executable tasks under
// the idea they were promoted from, and puts tasks with no origin idea in a
// separate ungrouped bucket.
func TestTasksGroupedByOriginEdge(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")

	idea := f.addIdea(t, project.ID, "Grouping idea", "")
	promoted := f.promote(t, idea.ID)
	f.setStatus(t, promoted.ID, core.StatusInProgress)

	plain := f.addTask(t, project.ID, "Standalone task")
	f.setStatus(t, plain.ID, core.StatusInProgress)

	server, err := New(Options{Backend: f.backend, Clock: f.clock, Project: project.ID})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	page, err := server.page(context.Background())
	if err != nil {
		t.Fatalf("page() error = %v", err)
	}

	group, ok := findGroup(page.InFlightGroups, idea.ID)
	if !ok {
		t.Fatalf("InFlightGroups = %v, want a group for %s", groupIDs(page.InFlightGroups), idea.ID)
	}
	if group.IdeaTitle != idea.Title {
		t.Fatalf("group title = %q, want %q", group.IdeaTitle, idea.Title)
	}
	if !hasTask(group.Tasks, promoted.ID) {
		t.Fatalf("group tasks = %v, want promoted %s", taskIDs(group.Tasks), promoted.ID)
	}

	ungrouped, ok := findGroup(page.InFlightGroups, "")
	if !ok {
		t.Fatalf("InFlightGroups = %v, want an ungrouped bucket", groupIDs(page.InFlightGroups))
	}
	if !ungrouped.Ungrouped {
		t.Fatal("bucket for empty origin must be marked ungrouped")
	}
	if !hasTask(ungrouped.Tasks, plain.ID) {
		t.Fatalf("ungrouped tasks = %v, want %s", taskIDs(ungrouped.Tasks), plain.ID)
	}

	// The promoted task carries its origin for the chip rendered on its card.
	view, _ := findTask(page.InFlight, promoted.ID)
	if view.Origin != idea.ID || view.OriginTitle != idea.Title {
		t.Fatalf("promoted view origin = (%s, %q), want (%s, %q)", view.Origin, view.OriginTitle, idea.ID, idea.Title)
	}
}

// TestWaitingTasksGroupedByOriginEdge covers the second kanban column.
func TestWaitingTasksGroupedByOriginEdge(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")

	idea := f.addIdea(t, project.ID, "Waiting idea", "")
	target := f.promote(t, idea.ID)
	blocker := f.addTask(t, project.ID, "blocker")
	if _, err := f.tasks.AddDep(context.Background(), target.ID, blocker.ID); err != nil {
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

	group, ok := findGroup(page.WaitingGroups, idea.ID)
	if !ok {
		t.Fatalf("WaitingGroups = %v, want a group for %s", groupIDs(page.WaitingGroups), idea.ID)
	}
	waiting, ok := findTask(group.Tasks, target.ID)
	if !ok {
		t.Fatalf("group tasks = %v, want %s", taskIDs(group.Tasks), target.ID)
	}
	if waiting.Reason != ReasonDepUnresolved {
		t.Fatalf("waiting reason = %q, want %q", waiting.Reason, ReasonDepUnresolved)
	}
}

// TestIdeaDetailPage proves the drill-down from an idea to its promoted tasks
// and artifacts: /idea/<id> renders the idea, each promoted task linking to
// /task/<id>, and each attached artifact linking to its detail page.
func TestIdeaDetailPage(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")

	idea := f.addIdea(t, project.ID, "Spark of a plan", "the full idea body")
	promoted := f.promote(t, idea.ID)
	f.addArtifact(t, project.ID, &idea.ID, core.ArtifactDoc, "Design note", "artifact body")
	f.addArtifact(t, project.ID, &promoted.ID, core.ArtifactMemory, "Gotcha memory", "remember this")

	ts := newTestServer(t, f, Options{Project: project.ID})
	body := getBody(t, ts.URL+"/idea/"+string(idea.ID))

	for _, want := range []string{
		"Spark of a plan",
		"the full idea body",
		string(promoted.ID),
		"/task/" + string(promoted.ID),
		"Design note",
		"Gotcha memory",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("idea detail missing %q:\n%s", want, body)
		}
	}
}

func TestTaskDetailPage(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")

	idea := f.addIdea(t, project.ID, "Origin idea", "")
	task := f.promote(t, idea.ID)
	blocker := f.addTask(t, project.ID, "Blocker task")
	if _, err := f.tasks.AddDep(context.Background(), task.ID, blocker.ID); err != nil {
		t.Fatalf("AddDep() error = %v", err)
	}
	f.addArtifact(t, project.ID, &task.ID, core.ArtifactMemory, "Task memory", "the memory body")

	ts := newTestServer(t, f, Options{Project: project.ID})
	body := getBody(t, ts.URL+"/task/"+string(task.ID))

	for _, want := range []string{
		task.Title,
		string(blocker.ID),
		"/task/" + string(blocker.ID),
		"Origin idea",
		"/idea/" + string(idea.ID),
		"Task memory",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("task detail missing %q:\n%s", want, body)
		}
	}
}

func TestArtifactDetailPages(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	task := f.addTask(t, project.ID, "linked task")

	memory := f.addArtifact(t, project.ID, &task.ID, core.ArtifactMemory, "A memory", "memory body")
	doc := f.addArtifact(t, project.ID, nil, core.ArtifactDoc, "A doc", "doc body")
	spec := f.addArtifact(t, project.ID, nil, core.ArtifactSpec, "A spec", "spec body")

	ts := newTestServer(t, f, Options{Project: project.ID})

	memoryBody := getBody(t, ts.URL+"/memory/"+string(memory.ID))
	for _, want := range []string{"A memory", "memory body", "/task/" + string(task.ID)} {
		if !strings.Contains(memoryBody, want) {
			t.Fatalf("memory detail missing %q:\n%s", want, memoryBody)
		}
	}

	for _, artifact := range []struct {
		path string
		id   core.ArtifactID
		body string
	}{
		{"/doc/" + string(doc.ID), doc.ID, "doc body"},
		{"/doc/" + string(spec.ID), spec.ID, "spec body"},
	} {
		body := getBody(t, ts.URL+artifact.path)
		if !strings.Contains(body, artifact.body) {
			t.Fatalf("artifact %s detail missing body:\n%s", artifact.id, body)
		}
	}

	// Kind is enforced by the route: a memory is not a doc, and vice versa.
	for _, path := range []string{"/doc/" + string(memory.ID), "/memory/" + string(doc.ID)} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("Get(%s) error = %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("Get(%s) status = %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestDetailPagesRejectUnknownIDs(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID})

	for _, path := range []string{"/idea/t-nope", "/task/t-nope", "/memory/art-nope", "/doc/art-nope", "/idea/", "/task/"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("Get(%s) error = %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("Get(%s) status = %d, want 404", path, resp.StatusCode)
		}
	}
}

// TestDetailPagesAreReadOnly proves the drill-down stays inside the read-only
// boundary: no detail page accepts a mutation.
func TestDetailPagesAreReadOnly(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	idea := f.addIdea(t, project.ID, "Read only idea", "")
	task := f.promote(t, idea.ID)
	artifact := f.addArtifact(t, project.ID, nil, core.ArtifactDoc, "Read only doc", "")
	ts := newTestServer(t, f, Options{Project: project.ID})

	paths := []string{
		"/idea/" + string(idea.ID),
		"/task/" + string(task.ID),
		"/doc/" + string(artifact.ID),
	}
	for _, path := range paths {
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
		}
	}
}

// TestFiguresAreLive proves the header figures are recomputed on every refresh
// rather than frozen at page load: a status change is reflected in /fragment.
func TestFiguresAreLive(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	idea := f.addIdea(t, project.ID, "Live idea", "")
	task := f.promote(t, idea.ID)
	ts := newTestServer(t, f, Options{Project: project.ID})

	before := fragmentStats(t, ts.URL)
	if !strings.Contains(before, ">0</dd>") {
		t.Fatalf("expected a zero figure before any work starts:\n%s", before)
	}
	f.setStatus(t, task.ID, core.StatusInProgress)
	after := fragmentStats(t, ts.URL)
	if after == before {
		t.Fatal("fragment figures did not change after a status change")
	}
}

// fragmentStats returns the stats section of the fragment, which is the live
// region refreshed over SSE.
func fragmentStats(t *testing.T, base string) string {
	t.Helper()
	body := getBody(t, base+"/fragment")
	start := strings.Index(body, `<dl class="stats">`)
	if start < 0 {
		t.Fatalf("fragment has no stats block:\n%s", body)
	}
	end := strings.Index(body[start:], "</dl>")
	if end < 0 {
		t.Fatalf("stats block is not closed:\n%s", body)
	}
	return body[start : start+end]
}

func groupIDs(groups []taskGroup) []core.TicketID {
	out := make([]core.TicketID, 0, len(groups))
	for _, group := range groups {
		out = append(out, group.IdeaID)
	}
	return out
}
