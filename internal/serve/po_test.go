package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

func artifactTitles(artifacts []artifactView) []string {
	out := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		out = append(out, artifact.Title)
	}
	return out
}

// getDoc fetches a read-side JSON document and decodes it into v, failing on any
// non-200 so a missing or rejected endpoint is loud.
func getDoc(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("Get(%s) error = %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Get(%s) status = %d, want 200", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("Decode(%s) error = %v", url, err)
	}
}

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

func (f *fixture) addBug(t *testing.T, projectID core.ProjectID, title, body string) *core.Ticket {
	t.Helper()
	task, err := f.tasks.Add(context.Background(), app.TicketInput{
		ProjectID:   projectID,
		Kind:        core.KindBug,
		Title:       title,
		Description: body,
	})
	if err != nil {
		t.Fatalf("Add(bug %q) error = %v", title, err)
	}
	return task
}

func (f *fixture) promote(t *testing.T, ideaID core.TicketID) *core.Ticket {
	t.Helper()
	agent, err := f.actors.Resolve(context.Background(), "claude")
	if err != nil {
		agent, err = f.actors.Add(context.Background(), core.ActorAgent, "claude")
		if err != nil {
			t.Fatalf("Add(agent) error = %v", err)
		}
	}
	task, err := f.tasks.Promote(context.Background(), ideaID, app.PromoteInput{
		AssigneeID:         &agent.ID,
		AcceptanceCriteria: []string{"it works"},
	})
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

// TestIdeaDetailAPI proves the drill-down from an idea to its promoted tasks
// and artifacts: /api/idea/<id> returns the idea, each promoted task linking to
// /task/<id>, and each attached artifact linking to its detail page.
func TestIdeaDetailAPI(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")

	idea := f.addIdea(t, project.ID, "Spark of a plan", "the full idea body")
	promoted := f.promote(t, idea.ID)
	f.addArtifact(t, project.ID, &idea.ID, core.ArtifactDoc, "Design note", "artifact body")
	f.addArtifact(t, project.ID, &promoted.ID, core.ArtifactMemory, "Gotcha memory", "remember this")

	ts := newTestServer(t, f, Options{Project: project.ID})
	var doc ideaView
	getDoc(t, ts.URL+"/api/idea/"+string(idea.ID), &doc)

	if doc.Title != "Spark of a plan" || doc.Description != "the full idea body" {
		t.Fatalf("idea doc = %+v", doc)
	}
	var task taskLink
	for _, candidate := range doc.Tasks {
		if candidate.ID == promoted.ID {
			task = candidate
		}
	}
	if task.ID != promoted.ID {
		t.Fatalf("idea tasks = %+v, want promoted %s", doc.Tasks, promoted.ID)
	}
	if task.URL != "/task/"+string(promoted.ID) {
		t.Fatalf("promoted task url = %q, want /task/%s", task.URL, promoted.ID)
	}
	titles := artifactTitles(doc.Artifacts)
	for _, want := range []string{"Design note", "Gotcha memory"} {
		if !slices.Contains(titles, want) {
			t.Fatalf("idea artifacts = %v, want %q", titles, want)
		}
	}
}

// TestBugIsACaptureOnTheBoard proves the dashboard treats a bug like an idea: it
// rolls up under the captures board, its detail page renders at /idea/<bug>, and
// /task/<bug> is rejected so a capture never masquerades as executable work.
func TestBugIsACaptureOnTheBoard(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	bug := f.addBug(t, project.ID, "It crashes on save", "repro: open, edit, save")

	server, err := New(Options{Backend: f.backend, Clock: f.clock, Project: project.ID})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	page, err := server.page(context.Background())
	if err != nil {
		t.Fatalf("page() error = %v", err)
	}
	if _, ok := findIdea(page.CapturedIdeas, bug.ID); !ok {
		t.Fatalf("bug %s not in the captures lane: %v", bug.ID, page.CapturedIdeas)
	}
	if page.Stats.Ideas != 1 {
		t.Fatalf("Stats.Ideas = %d, want the bug counted as a capture", page.Stats.Ideas)
	}

	ts := newTestServer(t, f, Options{Project: project.ID})
	var doc ideaView
	getDoc(t, ts.URL+"/api/idea/"+string(bug.ID), &doc)
	if doc.Title != "It crashes on save" || doc.Kind != core.KindBug {
		t.Fatalf("bug detail = %+v, want the bug capture", doc)
	}
	resp, err := http.Get(ts.URL + "/api/task/" + string(bug.ID))
	if err != nil {
		t.Fatalf("GET /api/task/<bug> error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /api/task/<bug> status = %d, want 404 (a capture is not a task)", resp.StatusCode)
	}
}

// TestBugRollsUpWithPromotedTasks proves a bug is the origin a promoted task
// groups under, exactly like an idea.
func TestBugRollsUpWithPromotedTasks(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	bug := f.addBug(t, project.ID, "It crashes on save", "")
	task := f.promote(t, bug.ID)
	f.setStatus(t, task.ID, core.StatusInProgress)

	server, err := New(Options{Backend: f.backend, Clock: f.clock, Project: project.ID})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	page, err := server.page(context.Background())
	if err != nil {
		t.Fatalf("page() error = %v", err)
	}
	capture, ok := findIdea(page.ActiveIdeas, bug.ID)
	if !ok {
		t.Fatalf("bug %s not in the active captures lane: %v", bug.ID, page.ActiveIdeas)
	}
	if capture.Total != 1 {
		t.Fatalf("bug rollup total = %d, want the promoted task counted", capture.Total)
	}
	group, ok := findGroup(page.InFlightGroups, bug.ID)
	if !ok || group.IdeaTitle != bug.Title {
		t.Fatalf("InFlightGroups = %v, want the task grouped under the bug", groupIDs(page.InFlightGroups))
	}
}

func TestTaskDetailAPI(t *testing.T) {
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
	var doc taskPageJSON
	getDoc(t, ts.URL+"/api/task/"+string(task.ID), &doc)

	if doc.Title != task.Title {
		t.Fatalf("task title = %q, want %q", doc.Title, task.Title)
	}
	var dep taskLink
	for _, candidate := range doc.Deps {
		if candidate.ID == blocker.ID {
			dep = candidate
		}
	}
	if dep.ID != blocker.ID || dep.URL != "/task/"+string(blocker.ID) {
		t.Fatalf("task deps = %+v, want blocker %s", doc.Deps, blocker.ID)
	}
	if doc.Origin == nil || doc.Origin.ID != idea.ID || doc.Origin.URL != "/idea/"+string(idea.ID) {
		t.Fatalf("task origin = %+v, want idea %s", doc.Origin, idea.ID)
	}
	if !slices.Contains(artifactTitles(doc.Artifacts), "Task memory") {
		t.Fatalf("task artifacts = %v, want Task memory", artifactTitles(doc.Artifacts))
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

	var memoryDoc artifactPageJSON
	getDoc(t, ts.URL+"/api/memory/"+string(memory.ID), &memoryDoc)
	if memoryDoc.Title != "A memory" || memoryDoc.Body != "memory body" {
		t.Fatalf("memory doc = %+v", memoryDoc)
	}
	if memoryDoc.AttachedTo == nil || memoryDoc.AttachedTo.ID != task.ID {
		t.Fatalf("memory attached_to = %+v, want task %s", memoryDoc.AttachedTo, task.ID)
	}

	for _, artifact := range []struct {
		path string
		id   core.ArtifactID
		body string
	}{
		{"/api/doc/" + string(doc.ID), doc.ID, "doc body"},
		{"/api/doc/" + string(spec.ID), spec.ID, "spec body"},
	} {
		var got artifactPageJSON
		getDoc(t, ts.URL+artifact.path, &got)
		if got.Body != artifact.body {
			t.Fatalf("artifact %s body = %q, want %q", artifact.id, got.Body, artifact.body)
		}
	}

	// Kind is enforced by the route: a memory is not a doc, and vice versa.
	for _, path := range []string{"/api/doc/" + string(memory.ID), "/api/memory/" + string(doc.ID)} {
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

	for _, path := range []string{"/api/idea/t-nope", "/api/task/t-nope", "/api/memory/art-nope", "/api/doc/art-nope", "/api/idea/", "/api/task/"} {
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

// TestDetailRoutesRespectProjectScope proves a scoped server cannot reach
// another project's task, idea, or artifact through the detail routes: each
// 404s when the id falls outside the server's project, while in-scope ids still
// resolve.
func TestDetailRoutesRespectProjectScope(t *testing.T) {
	f := newFixture(t)
	mine := f.addProject(t, "acme", "Acme")
	other := f.addProject(t, "other", "Other")

	mineIdea := f.addIdea(t, mine.ID, "Mine idea", "")
	mineTask := f.promote(t, mineIdea.ID)
	mineDoc := f.addArtifact(t, mine.ID, nil, core.ArtifactDoc, "Mine doc", "")

	otherIdea := f.addIdea(t, other.ID, "Other idea", "")
	otherTask := f.promote(t, otherIdea.ID)
	otherDoc := f.addArtifact(t, other.ID, nil, core.ArtifactDoc, "Other doc", "")

	ts := newTestServer(t, f, Options{Project: mine.ID})

	for _, path := range []string{
		"/api/task/" + string(mineTask.ID),
		"/api/idea/" + string(mineIdea.ID),
		"/api/doc/" + string(mineDoc.ID),
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("Get(%s) error = %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Get(%s) status = %d, want 200 (in scope)", path, resp.StatusCode)
		}
	}

	for _, path := range []string{
		"/api/task/" + string(otherTask.ID),
		"/api/idea/" + string(otherIdea.ID),
		"/api/doc/" + string(otherDoc.ID),
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("Get(%s) error = %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("Get(%s) status = %d, want 404 (out of scope)", path, resp.StatusCode)
		}
	}
}

// TestDanglingDepRendersMissingID proves a dependency that is not present in the
// scoped snapshot still renders its id on the task detail, rather than a blank
// row. A cross-project dependency is the concrete dangling case the scoped view
// sees.
func TestDanglingDepRendersMissingID(t *testing.T) {
	f := newFixture(t)
	mine := f.addProject(t, "acme", "Acme")
	other := f.addProject(t, "other", "Other")

	task := f.addTask(t, mine.ID, "Depends on out-of-scope work")
	foreign := f.addTask(t, other.ID, "Foreign blocker")
	if _, err := f.tasks.AddDep(context.Background(), task.ID, foreign.ID); err != nil {
		t.Fatalf("AddDep() error = %v", err)
	}

	ts := newTestServer(t, f, Options{Project: mine.ID})
	var doc taskPageJSON
	getDoc(t, ts.URL+"/api/task/"+string(task.ID), &doc)

	var dep taskLink
	for _, candidate := range doc.Deps {
		if candidate.ID == foreign.ID {
			dep = candidate
		}
	}
	if dep.ID != foreign.ID {
		t.Fatalf("task deps = %+v, want the dangling id %s", doc.Deps, foreign.ID)
	}
	if dep.URL != "/task/"+string(foreign.ID) {
		t.Fatalf("dangling dep url = %q, want /task/%s", dep.URL, foreign.ID)
	}
}

// TestAllProjectsCrossProjectDepRendersTitleClass proves that in all-projects
// mode a dependency in another project renders with its title and class instead
// of a blank link, because the detail view loads every project rather than just
// the task's own.
func TestAllProjectsCrossProjectDepRendersTitleClass(t *testing.T) {
	f := newFixture(t)
	mine := f.addProject(t, "acme", "Acme")
	other := f.addProject(t, "other", "Other")

	task := f.addTask(t, mine.ID, "Depends on another project")
	foreign := f.addTask(t, other.ID, "Foreign blocker")
	if _, err := f.tasks.AddDep(context.Background(), task.ID, foreign.ID); err != nil {
		t.Fatalf("AddDep() error = %v", err)
	}

	ts := newTestServer(t, f, Options{All: true})
	var doc taskPageJSON
	getDoc(t, ts.URL+"/api/task/"+string(task.ID), &doc)

	var dep taskLink
	for _, candidate := range doc.Deps {
		if candidate.ID == foreign.ID {
			dep = candidate
		}
	}
	if dep.ID != foreign.ID {
		t.Fatalf("task deps = %+v, want the cross-project id %s", doc.Deps, foreign.ID)
	}
	if dep.Title != foreign.Title {
		t.Fatalf("cross-project dep title = %q, want %q", dep.Title, foreign.Title)
	}
	if dep.Class == "" {
		t.Fatalf("cross-project dep class is empty, want a class")
	}
	if dep.URL != "/task/"+string(foreign.ID) {
		t.Fatalf("cross-project dep url = %q, want /task/%s", dep.URL, foreign.ID)
	}
}

// TestDanglingIdeaDepRoutesToIdea proves a dependency outside the scoped
// snapshot that is an idea links to its /idea/ page rather than /task/, even
// though the idea itself is out of scope.
func TestDanglingIdeaDepRoutesToIdea(t *testing.T) {
	f := newFixture(t)
	mine := f.addProject(t, "acme", "Acme")
	other := f.addProject(t, "other", "Other")

	task := f.addTask(t, mine.ID, "Depends on an out-of-scope idea")
	foreign := f.addIdea(t, other.ID, "Foreign idea", "")
	if _, err := f.tasks.AddDep(context.Background(), task.ID, foreign.ID); err != nil {
		t.Fatalf("AddDep() error = %v", err)
	}

	ts := newTestServer(t, f, Options{Project: mine.ID})
	var doc taskPageJSON
	getDoc(t, ts.URL+"/api/task/"+string(task.ID), &doc)

	var dep taskLink
	for _, candidate := range doc.Deps {
		if candidate.ID == foreign.ID {
			dep = candidate
		}
	}
	if dep.ID != foreign.ID {
		t.Fatalf("task deps = %+v, want the dangling idea id %s", doc.Deps, foreign.ID)
	}
	if dep.URL != "/idea/"+string(foreign.ID) {
		t.Fatalf("dangling idea dep url = %q, want /idea/%s", dep.URL, foreign.ID)
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
		"/api/idea/" + string(idea.ID),
		"/api/task/" + string(task.ID),
		"/api/doc/" + string(artifact.ID),
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
// rather than frozen at page load: a status change is reflected in the snapshot
// the app refetches over SSE.
func TestFiguresAreLive(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	idea := f.addIdea(t, project.ID, "Live idea", "")
	task := f.promote(t, idea.ID)
	ts := newTestServer(t, f, Options{Project: project.ID})

	var before snapshotJSON
	getDoc(t, ts.URL+"/api/snapshot", &before)
	f.setStatus(t, task.ID, core.StatusInProgress)
	var after snapshotJSON
	getDoc(t, ts.URL+"/api/snapshot", &after)
	if after.Stats == before.Stats {
		t.Fatalf("snapshot figures did not change after a status change: %+v", before.Stats)
	}
}

func groupIDs(groups []taskGroup) []core.TicketID {
	out := make([]core.TicketID, 0, len(groups))
	for _, group := range groups {
		out = append(out, group.IdeaID)
	}
	return out
}
