package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

func (f *fixture) setPriority(t *testing.T, id core.TicketID, priority int) {
	t.Helper()
	if _, err := f.tasks.Set(context.Background(), id, app.TicketSet{Priority: &priority}); err != nil {
		t.Fatalf("Set(%s, priority=%d) error = %v", id, priority, err)
	}
}

// addTicketWithID stores a task under an explicit id so a test can pin the id
// order independently of the rank order it asserts.
func (f *fixture) addTicketWithID(t *testing.T, projectID core.ProjectID, id core.TicketID, kind core.TicketKind, title string) *core.Ticket {
	t.Helper()
	task, err := f.tasks.Add(context.Background(), app.TicketInput{
		ID:        &id,
		ProjectID: projectID,
		Kind:      kind,
		Title:     title,
	})
	if err != nil {
		t.Fatalf("Add(%s) error = %v", id, err)
	}
	return task
}

// groupTask links an executable task to its origin capture, the edge the board
// groups by.
func (f *fixture) groupTask(t *testing.T, task, idea *core.Ticket) {
	t.Helper()
	if _, err := f.tasks.AddDep(context.Background(), task.ID, idea.ID); err != nil {
		t.Fatalf("AddDep(%s, %s) error = %v", task.ID, idea.ID, err)
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
// blocked when any is blocked, in-progress once any promoted task has started,
// and captured when nothing has been promoted or nothing has started yet. Ideas
// are the primary unit, so this is the core read of the dashboard.
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

	// Grooming promotes tasks but is not work: with every promoted task still
	// todo the capture belongs in the backlog, not the in-progress lane.
	groomed := f.addIdea(t, project.ID, "Groomed idea", "promoted but not started")
	f.promote(t, groomed.ID)

	server, err := New(Options{Backend: f.backend, Clock: f.clock, Project: project.ID})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	page, err := server.page(context.Background())
	if err != nil {
		t.Fatalf("page() error = %v", err)
	}

	cases := []struct {
		name  string
		id    core.TicketID
		state string
		from  []ideaView
	}{
		{"finished", finished.ID, "finished", page.FinishedIdeas},
		{"active", active.ID, "active", page.ActiveIdeas},
		{"blocked", blocked.ID, "blocked", page.BlockedIdeas},
		{"captured", captured.ID, "captured", page.CapturedIdeas},
		{"groomed", groomed.ID, "captured", page.CapturedIdeas},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idea, ok := findIdea(tc.from, tc.id)
			if !ok {
				t.Fatalf("idea %s not in %s lane", tc.id, tc.name)
			}
			if idea.State != tc.state {
				t.Fatalf("idea %s state = %q, want %q", tc.id, idea.State, tc.state)
			}
		})
	}

	// A merely-groomed capture must stay out of the active lane even though it
	// has promoted work; only a started (or finished) task makes a capture active.
	if _, ok := findIdea(page.ActiveIdeas, groomed.ID); ok {
		t.Fatalf("groomed capture %s in the active lane, want backlog", groomed.ID)
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
	// The parent idea/bug row carries its own status chip, so the group must
	// expose the idea rollup state (its promoted task is in progress).
	if group.IdeaState != "active" {
		t.Fatalf("group idea state = %q, want %q", group.IdeaState, "active")
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

// TestClassifyInProgress proves an in-progress task gets its own class rather
// than falling through to the generic "waiting" one: the work board's "In
// progress" column already names that status, so chipping the row "waiting"
// mislabels it.
func TestClassifyInProgress(t *testing.T) {
	cases := []struct {
		name   string
		status core.TicketStatus
		class  string
		chip   string
	}{
		{"in progress", core.StatusInProgress, "in-progress", "in progress"},
		{"waiting", core.StatusTodo, "waiting", "waiting"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			class := classify(core.Ticket{Status: tc.status}, false, false, false)
			if class != tc.class {
				t.Fatalf("classify(%s) = %q, want %q", tc.status, class, tc.class)
			}
			if got := chip(class); got != tc.chip {
				t.Fatalf("chip(%q) = %q, want %q", class, got, tc.chip)
			}
		})
	}
}

// TestGroupParentRowCarriesCaptureKind proves the work-board parent row can
// name the origin capture's kind, so a bug-origin group and an idea-origin group
// are not shown identically. Ungrouped tasks have no origin capture, so their
// bucket carries no kind.
func TestGroupParentRowCarriesCaptureKind(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")

	bug := f.addBug(t, project.ID, "Checkout total is wrong", "")
	bugTask := f.promote(t, bug.ID)
	f.setStatus(t, bugTask.ID, core.StatusInProgress)

	idea := f.addIdea(t, project.ID, "Add CSV import", "")
	ideaTask := f.promote(t, idea.ID)
	f.setStatus(t, ideaTask.ID, core.StatusInProgress)

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

	bugGroup, ok := findGroup(page.InFlightGroups, bug.ID)
	if !ok {
		t.Fatalf("InFlightGroups = %v, want a group for %s", groupIDs(page.InFlightGroups), bug.ID)
	}
	if bugGroup.IdeaKind != core.KindBug {
		t.Fatalf("bug group kind = %q, want %q", bugGroup.IdeaKind, core.KindBug)
	}

	ideaGroup, ok := findGroup(page.InFlightGroups, idea.ID)
	if !ok {
		t.Fatalf("InFlightGroups = %v, want a group for %s", groupIDs(page.InFlightGroups), idea.ID)
	}
	if ideaGroup.IdeaKind != core.KindIdea {
		t.Fatalf("idea group kind = %q, want %q", ideaGroup.IdeaKind, core.KindIdea)
	}

	ungrouped, ok := findGroup(page.InFlightGroups, "")
	if !ok {
		t.Fatalf("InFlightGroups = %v, want an ungrouped bucket", groupIDs(page.InFlightGroups))
	}
	if ungrouped.IdeaKind != "" {
		t.Fatalf("ungrouped bucket kind = %q, want empty", ungrouped.IdeaKind)
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
	// A blocked/waiting child makes the parent idea read "blocked" too, so the
	// parent row in the Waiting/blocked column is not ambiguous.
	if group.IdeaState != "blocked" {
		t.Fatalf("group idea state = %q, want %q", group.IdeaState, "blocked")
	}
	waiting, ok := findTask(group.Tasks, target.ID)
	if !ok {
		t.Fatalf("group tasks = %v, want %s", taskIDs(group.Tasks), target.ID)
	}
	if waiting.Reason != ReasonDepUnresolved {
		t.Fatalf("waiting reason = %q, want %q", waiting.Reason, ReasonDepUnresolved)
	}
}

// TestBoardOrdersGroupsByNextRank proves the work board leads with the work
// most worth doing next rather than with the lowest idea id: groups follow the
// same composite rank `ft task next` uses (here the priority term). The
// higher-ranked idea's id sorts after the lower one, so idea-id order would put
// it last.
func TestBoardOrdersGroupsByNextRank(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")

	lowIdea := f.addTicketWithID(t, project.ID, "t-a", core.KindIdea, "Low priority idea")
	lowTask := f.addTicketWithID(t, project.ID, "t-m", core.KindTask, "low work")
	f.groupTask(t, lowTask, lowIdea)
	f.setStatus(t, lowTask.ID, core.StatusInProgress)

	highIdea := f.addTicketWithID(t, project.ID, "t-b", core.KindIdea, "High priority idea")
	highTask := f.addTicketWithID(t, project.ID, "t-n", core.KindTask, "high work")
	f.groupTask(t, highTask, highIdea)
	f.setStatus(t, highTask.ID, core.StatusInProgress)
	f.setPriority(t, highTask.ID, 5)

	server, err := New(Options{Backend: f.backend, Clock: f.clock, Project: project.ID})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	page, err := server.page(context.Background())
	if err != nil {
		t.Fatalf("page() error = %v", err)
	}

	if len(page.InFlightGroups) != 2 {
		t.Fatalf("InFlightGroups = %v, want the two idea groups", groupIDs(page.InFlightGroups))
	}
	if page.InFlightGroups[0].IdeaID != highIdea.ID {
		t.Fatalf("InFlightGroups order = %v, want %s (higher rank) first", groupIDs(page.InFlightGroups), highIdea.ID)
	}
	if page.InFlightGroups[1].IdeaID != lowIdea.ID {
		t.Fatalf("InFlightGroups order = %v, want %s last", groupIDs(page.InFlightGroups), lowIdea.ID)
	}
}

// TestBoardOrdersTasksWithinGroupByNextRank proves the tasks inside one idea
// group are ordered by the same rank, not by id: the higher-ranked sibling
// leads even though its id sorts after the lower-ranked one.
func TestBoardOrdersTasksWithinGroupByNextRank(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")

	idea := f.addTicketWithID(t, project.ID, "t-c", core.KindIdea, "Multi-task idea")
	first := f.addTicketWithID(t, project.ID, "t-p", core.KindTask, "first work")
	f.groupTask(t, first, idea)
	f.setStatus(t, first.ID, core.StatusInProgress)

	second := f.addTicketWithID(t, project.ID, "t-q", core.KindTask, "second work")
	f.groupTask(t, second, idea)
	f.setStatus(t, second.ID, core.StatusInProgress)
	f.setPriority(t, second.ID, 9)

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
	if len(group.Tasks) != 2 {
		t.Fatalf("group tasks = %v, want both promoted tasks", taskIDs(group.Tasks))
	}
	if group.Tasks[0].ID != second.ID {
		t.Fatalf("group task order = %v, want %s (higher rank) first", taskIDs(group.Tasks), second.ID)
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

// TestTaskDetailSplitsCapturesFromDependencies proves the task detail does not
// present a capture (idea or bug) edge as a blocking dependency: only the task
// dependency lands under "Depends on"; the extra capture is grouped_under.
func TestTaskDetailSplitsCapturesFromDependencies(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")

	origin := f.addIdea(t, project.ID, "Origin idea", "")
	umbrella := f.addIdea(t, project.ID, "Umbrella idea", "")
	task := f.promote(t, origin.ID)
	blocker := f.addTask(t, project.ID, "Blocker task")
	if _, err := f.tasks.AddDep(context.Background(), task.ID, umbrella.ID); err != nil {
		t.Fatalf("AddDep(umbrella) error = %v", err)
	}
	if _, err := f.tasks.AddDep(context.Background(), task.ID, blocker.ID); err != nil {
		t.Fatalf("AddDep(blocker) error = %v", err)
	}

	ts := newTestServer(t, f, Options{Project: project.ID})
	var doc taskPageJSON
	getDoc(t, ts.URL+"/api/task/"+string(task.ID), &doc)

	if len(doc.Deps) != 1 || doc.Deps[0].ID != blocker.ID {
		t.Fatalf("task deps = %+v, want only the blocker %s", doc.Deps, blocker.ID)
	}
	if doc.Origin == nil || doc.Origin.ID != origin.ID {
		t.Fatalf("task origin = %+v, want %s", doc.Origin, origin.ID)
	}
	if len(doc.GroupedUnder) != 1 || doc.GroupedUnder[0].ID != umbrella.ID {
		t.Fatalf("task grouped_under = %+v, want %s", doc.GroupedUnder, umbrella.ID)
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

// Khoi 2026-10-12: artifactURL links every non-memory artifact at /doc/<id>,
// including a QA verdict. The doc route must therefore serve a QA artifact
// instead of 404ing with "No such doc". A derived check cache still has no page.
func TestDocRouteServesQAVerdict(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	idea := f.addIdea(t, project.ID, "Feature", "")
	qa := f.addArtifact(t, project.ID, &idea.ID, core.ArtifactQA, "QA pass", "all good")
	check := f.addArtifact(t, project.ID, &idea.ID, core.ArtifactTaskCheck, "check: tests", "cache")
	ts := newTestServer(t, f, Options{Project: project.ID})

	// The idea rollup links the QA artifact at /doc/<id>; that link must resolve.
	var ideaDoc ideaView
	getDoc(t, ts.URL+"/api/idea/"+string(idea.ID), &ideaDoc)
	url := ""
	for _, artifact := range ideaDoc.Artifacts {
		if artifact.ID == qa.ID {
			url = artifact.URL
		}
	}
	if url != "/doc/"+string(qa.ID) {
		t.Fatalf("qa artifact url = %q, want /doc/%s", url, qa.ID)
	}

	// The page fetches the API route for that link: /api + /doc/<id>.
	var got artifactPageJSON
	getDoc(t, ts.URL+"/api"+url, &got)
	if got.Kind != string(core.ArtifactQA) || got.Body != "all good" {
		t.Fatalf("qa doc = %+v, want kind %q body %q", got, core.ArtifactQA, "all good")
	}
	if got.AttachedTo == nil || got.AttachedTo.ID != idea.ID {
		t.Fatalf("qa attached_to = %+v, want idea %s", got.AttachedTo, idea.ID)
	}

	resp, err := http.Get(ts.URL + "/api/doc/" + string(check.ID))
	if err != nil {
		t.Fatalf("Get(check) error = %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("Get(/api/doc/<task_check>) status = %d, want 404", resp.StatusCode)
	}
}

// Khoi 2026-10-12: a rollup links its artifacts at artifact.url and the page
// fetches /api + url, so every artifact link a rollup surfaces must resolve. A
// task_check artifact is a derived cache with no page: the doc route rejects it
// with 404, so linking it from an idea or task rollup is a broken link. The fix
// keeps the cache out of the rollup artifact list rather than serving a page for
// it (t-cv2atkeeu6).
func TestRollupArtifactLinksResolve(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	idea := f.addIdea(t, project.ID, "Feature", "")
	task := f.promote(t, idea.ID)
	// A check cache attached to the capture and to the promoted task, plus a real
	// doc that must keep resolving.
	f.addArtifact(t, project.ID, &idea.ID, core.ArtifactTaskCheck, "check: idea", "cache")
	f.addArtifact(t, project.ID, &task.ID, core.ArtifactTaskCheck, "check: task", "cache")
	doc := f.addArtifact(t, project.ID, &task.ID, core.ArtifactDoc, "Design note", "body")
	ts := newTestServer(t, f, Options{Project: project.ID})

	var ideaDoc ideaView
	getDoc(t, ts.URL+"/api/idea/"+string(idea.ID), &ideaDoc)
	var taskDoc taskPageJSON
	getDoc(t, ts.URL+"/api/task/"+string(task.ID), &taskDoc)

	rollups := map[string][]artifactView{"idea": ideaDoc.Artifacts, "task": taskDoc.Artifacts}
	seenDoc := false
	for name, artifacts := range rollups {
		for _, artifact := range artifacts {
			if artifact.Kind == string(core.ArtifactTaskCheck) {
				t.Fatalf("%s rollup links a task_check artifact at %q, but /doc rejects it", name, artifact.URL)
			}
			if artifact.ID == doc.ID {
				seenDoc = true
			}
			resp, err := http.Get(ts.URL + "/api" + artifact.URL)
			if err != nil {
				t.Fatalf("Get(/api%s) error = %v", artifact.URL, err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s rollup links %q, but Get(/api%s) status = %d, want 200", name, artifact.ID, artifact.URL, resp.StatusCode)
			}
		}
	}
	if !seenDoc {
		t.Fatalf("rollups = %v, want the doc artifact %s linked", rollups, doc.ID)
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

// TestCrossProjectCaptureIsOrigin proves a task whose first capture dep belongs
// to another project still resolves that capture as its origin, on a scoped
// server and an all-projects server alike, matching ft task get. Capture
// provenance is an edge on the task, not a function of server scope, so the two
// surfaces must not disagree. The origin routes to its /idea/ page and is not a
// blocking dependency or a grouping edge.
func TestCrossProjectCaptureIsOrigin(t *testing.T) {
	f := newFixture(t)
	mine := f.addProject(t, "acme", "Acme")
	other := f.addProject(t, "other", "Other")

	task := f.addTask(t, mine.ID, "Depends on an out-of-scope idea")
	foreign := f.addIdea(t, other.ID, "Foreign idea", "")
	if _, err := f.tasks.AddDep(context.Background(), task.ID, foreign.ID); err != nil {
		t.Fatalf("AddDep() error = %v", err)
	}

	cases := []struct {
		name string
		opts Options
	}{
		{"scoped server", Options{Project: mine.ID}},
		{"all projects server", Options{All: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServer(t, f, tc.opts)
			var doc taskPageJSON
			getDoc(t, ts.URL+"/api/task/"+string(task.ID), &doc)

			if len(doc.Deps) != 0 {
				t.Fatalf("task deps = %+v, want a capture edge excluded", doc.Deps)
			}
			if len(doc.GroupedUnder) != 0 {
				t.Fatalf("task grouped_under = %+v, want the cross-project capture as origin", doc.GroupedUnder)
			}
			if doc.Origin == nil || doc.Origin.ID != foreign.ID {
				t.Fatalf("task origin = %+v, want the cross-project idea %s", doc.Origin, foreign.ID)
			}
			if doc.Origin.URL != "/idea/"+string(foreign.ID) {
				t.Fatalf("cross-project origin url = %q, want /idea/%s", doc.Origin.URL, foreign.ID)
			}
			// A capture never blocks, so the task is ready and carries no
			// not-ready reason on a scoped server and an all-projects server
			// alike - the same verdict ft task get and the graph report.
			if doc.Class != "ready-human" {
				t.Fatalf("task class = %q, want ready-human (a capture dep never blocks)", doc.Class)
			}
			if doc.Reason != "" {
				t.Fatalf("task reason = %q, want empty", doc.Reason)
			}
		})
	}
}

// TestCrossProjectCaptureGroupsOnDashboard pins the dashboard side of the
// origin rule: a task whose capture dep belongs to another project is grouped
// under that capture on the kanban, with the capture's title, even when the
// scoped snapshot omits the capture. ft task get, /api/task, and the dashboard
// must agree on the origin.
func TestCrossProjectCaptureGroupsOnDashboard(t *testing.T) {
	f := newFixture(t)
	mine := f.addProject(t, "acme", "Acme")
	other := f.addProject(t, "other", "Other")

	task := f.addTask(t, mine.ID, "Depends on an out-of-scope idea")
	f.setStatus(t, task.ID, core.StatusInProgress)
	foreign := f.addIdea(t, other.ID, "Foreign idea", "")
	if _, err := f.tasks.AddDep(context.Background(), task.ID, foreign.ID); err != nil {
		t.Fatalf("AddDep() error = %v", err)
	}

	cases := []struct {
		name string
		opts Options
	}{
		{"scoped server", Options{Project: mine.ID}},
		{"all projects server", Options{All: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.Backend = f.backend
			opts.Clock = f.clock
			server, err := New(opts)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			page, err := server.page(context.Background())
			if err != nil {
				t.Fatalf("page() error = %v", err)
			}
			group, ok := findGroup(page.InFlightGroups, foreign.ID)
			if !ok {
				t.Fatalf("InFlightGroups = %v, want a group for %s", groupIDs(page.InFlightGroups), foreign.ID)
			}
			if group.IdeaTitle != foreign.Title {
				t.Fatalf("group title = %q, want %q", group.IdeaTitle, foreign.Title)
			}
			if !hasTask(group.Tasks, task.ID) {
				t.Fatalf("group tasks = %v, want %s", taskIDs(group.Tasks), task.ID)
			}
		})
	}
}

// TestCrossProjectOriginSurfacesStoreError proves the read does not swallow a
// store failure while resolving an origin edge: only a dangling dep
// (core.ErrNotFound) is skipped, and any other Get error reaches the handler as
// a 503 rather than silently dropping the origin.
func TestCrossProjectOriginSurfacesStoreError(t *testing.T) {
	f := newFixture(t)
	mine := f.addProject(t, "acme", "Acme")
	other := f.addProject(t, "other", "Other")

	task := f.addTask(t, mine.ID, "Depends on an out-of-scope idea")
	foreign := f.addIdea(t, other.ID, "Foreign idea", "")
	if _, err := f.tasks.AddDep(context.Background(), task.ID, foreign.ID); err != nil {
		t.Fatalf("AddDep() error = %v", err)
	}

	boom := errors.New("get ticket: disk I/O error")
	backend := errTicketsBackend{
		Backend: f.backend,
		repo:    errTicketGet{TicketRepo: f.backend.Tickets(), id: foreign.ID, err: boom},
	}
	server, err := New(Options{Backend: backend, Clock: f.clock, Project: mine.ID, Assets: testAssets()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/task/" + string(task.ID))
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 for a non-NotFound store error", resp.StatusCode)
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
