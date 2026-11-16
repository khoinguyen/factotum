package serve

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

// Khoi 2026-10-14: "human next should show up as next, because it is block the
// agent". A capture whose remaining work is a human-next task reads as blocked
// and needs human attention - not as captured/backlog - and its card counts that
// task in its own bucket while naming it, so done + todo + active + blocked +
// human still sums to the total.
func TestIdeaCardCountsReadyHumanWorkAndNamesTheBlocker(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	project := f.addProject(t, "acme", "Acme")
	agent, err := f.actors.Add(ctx, core.ActorAgent, "bot")
	if err != nil {
		t.Fatalf("Add(actor) error = %v", err)
	}
	idea, err := f.tasks.Add(ctx, app.TicketInput{ProjectID: project.ID, Kind: core.KindIdea, Title: "capture"})
	if err != nil {
		t.Fatalf("Add(idea) error = %v", err)
	}
	finished, err := f.tasks.Promote(ctx, idea.ID, app.PromoteInput{AssigneeID: &agent.ID, AcceptanceCriteria: []string{"it works"}})
	if err != nil {
		t.Fatalf("Promote() error = %v", err)
	}
	if _, err := f.tasks.SetStatus(ctx, finished.ID, core.StatusDone); err != nil {
		t.Fatalf("SetStatus(done) error = %v", err)
	}
	// A human-next slice: unassigned ready work promoted from the capture, so it
	// is the next action for a person and the capture is blocked on them.
	human, err := f.tasks.Add(ctx, app.TicketInput{ProjectID: project.ID, Kind: core.KindTask, Title: "decide the scope"})
	if err != nil {
		t.Fatalf("Add(human) error = %v", err)
	}
	if _, err := f.tasks.AddDep(ctx, human.ID, idea.ID); err != nil {
		t.Fatalf("AddDep(human) error = %v", err)
	}

	server, err := New(Options{Backend: f.backend, Clock: f.clock, Project: project.ID})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	page, err := server.page(ctx)
	if err != nil {
		t.Fatalf("page() error = %v", err)
	}
	got, ok := findIdeaInPage(page, idea.ID)
	if !ok {
		t.Fatalf("idea %s missing from page", idea.ID)
	}
	if got.Total != 2 || got.Done != 1 || got.Human != 1 {
		t.Fatalf("Total/Done/Human = %d/%d/%d, want 2/1/1", got.Total, got.Done, got.Human)
	}
	if got.Todo != 0 || got.Active != 0 || got.Blocked != 0 {
		t.Fatalf("Todo/Active/Blocked = %d/%d/%d, want 0 each", got.Todo, got.Active, got.Blocked)
	}
	if sum := got.Done + got.Todo + got.Active + got.Blocked + got.Human; sum != got.Total {
		t.Fatalf("displayed buckets = %d, want total %d", sum, got.Total)
	}
	if got.State != "blocked" || got.Chip != "needs-human" {
		t.Fatalf("State/Chip = %q/%q, want blocked/needs-human", got.State, got.Chip)
	}
	if len(got.HumanTasks) != 1 || got.HumanTasks[0].ID != human.ID || got.HumanTasks[0].Title != human.Title {
		t.Fatalf("HumanTasks = %+v, want the blocking %s named", got.HumanTasks, human.ID)
	}

	// The snapshot the web app fetches carries the same count and named blocker.
	raw, err := json.Marshal(page.snapshot())
	if err != nil {
		t.Fatalf("Marshal(snapshot) error = %v", err)
	}
	var doc struct {
		Ideas struct {
			Blocked []struct {
				ID         string `json:"id"`
				Human      int    `json:"human"`
				HumanTasks []struct {
					ID    string `json:"id"`
					Title string `json:"title"`
				} `json:"human_tasks"`
			} `json:"blocked"`
		} `json:"ideas"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("snapshot json invalid: %v\n%s", err, raw)
	}
	if len(doc.Ideas.Blocked) != 1 {
		t.Fatalf("blocked lane = %d ideas, want the capture:\n%s", len(doc.Ideas.Blocked), raw)
	}
	card := doc.Ideas.Blocked[0]
	if card.ID != string(idea.ID) || card.Human != 1 {
		t.Fatalf("blocked card = %+v, want %s with human=1", card, idea.ID)
	}
	if len(card.HumanTasks) != 1 || card.HumanTasks[0].ID != string(human.ID) {
		t.Fatalf("blocked card human_tasks = %+v, want %s", card.HumanTasks, human.ID)
	}
}

// An agent task gated by a human-next item is blocked by that item, and the
// board says so: class "needs-human" with a needs_human reason naming the human
// task, instead of a ready-agent row or a generic blocked one with no detail.
func TestAgentTaskGatedByHumanNextIsClassifiedNeedsHuman(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	project := f.addProject(t, "acme", "Acme")
	agent, err := f.actors.Add(ctx, core.ActorAgent, "bot")
	if err != nil {
		t.Fatalf("Add(actor) error = %v", err)
	}
	human, err := f.tasks.Add(ctx, app.TicketInput{ProjectID: project.ID, Kind: core.KindTask, Title: "pick the approach"})
	if err != nil {
		t.Fatalf("Add(human) error = %v", err)
	}
	gated, err := f.tasks.Add(ctx, app.TicketInput{
		ProjectID: project.ID, Kind: core.KindTask, Title: "build it",
		AssigneeID: &agent.ID, Groomed: true, AcceptanceCriteria: []string{"it works"},
	})
	if err != nil {
		t.Fatalf("Add(gated) error = %v", err)
	}
	if _, err := f.tasks.AddDep(ctx, gated.ID, human.ID); err != nil {
		t.Fatalf("AddDep(gated) error = %v", err)
	}

	server, err := New(Options{Backend: f.backend, Clock: f.clock, Project: project.ID})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	page, err := server.page(ctx)
	if err != nil {
		t.Fatalf("page() error = %v", err)
	}
	if hasTask(page.NextAgent, gated.ID) || hasTask(page.NextHuman, gated.ID) {
		t.Fatalf("gated task %s must not be offered as next work", gated.ID)
	}
	if !hasTask(page.NextHuman, human.ID) {
		t.Fatalf("NextHuman = %v, want the blocking %s", taskIDs(page.NextHuman), human.ID)
	}
	view, ok := findTask(page.Waiting, gated.ID)
	if !ok {
		t.Fatalf("Waiting = %v, want the gated %s", taskIDs(page.Waiting), gated.ID)
	}
	if view.Class != "needs-human" || view.Chip != "needs human" {
		t.Fatalf("gated view = (class %q, chip %q), want (needs-human, needs human)", view.Class, view.Chip)
	}
	if view.Reason != ReasonNeedsHuman {
		t.Fatalf("gated reason = %q, want %q", view.Reason, ReasonNeedsHuman)
	}
	if !strings.Contains(view.Detail, string(human.ID)) {
		t.Fatalf("gated detail = %q, want it to name %s", view.Detail, human.ID)
	}
}

// The needs-human reason names the human, but it must not hide the task's other
// unresolved blockers: the detail stays the graph's full unresolved-dependency
// list, so the board and `task next --explain` agree on what is still missing.
func TestNeedsHumanDetailKeepsTheOtherBlockers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	project := f.addProject(t, "acme", "Acme")
	agent, err := f.actors.Add(ctx, core.ActorAgent, "bot")
	if err != nil {
		t.Fatalf("Add(actor) error = %v", err)
	}
	human, err := f.tasks.Add(ctx, app.TicketInput{ProjectID: project.ID, Kind: core.KindTask, Title: "pick the approach"})
	if err != nil {
		t.Fatalf("Add(human) error = %v", err)
	}
	other, err := f.tasks.Add(ctx, app.TicketInput{ProjectID: project.ID, Kind: core.KindTask, Title: "still moving"})
	if err != nil {
		t.Fatalf("Add(other) error = %v", err)
	}
	if _, err := f.tasks.SetStatus(ctx, other.ID, core.StatusInProgress); err != nil {
		t.Fatalf("SetStatus(other) error = %v", err)
	}
	gated, err := f.tasks.Add(ctx, app.TicketInput{
		ProjectID: project.ID, Kind: core.KindTask, Title: "build it",
		AssigneeID: &agent.ID, Groomed: true, AcceptanceCriteria: []string{"it works"},
	})
	if err != nil {
		t.Fatalf("Add(gated) error = %v", err)
	}
	for _, dep := range []core.TicketID{human.ID, other.ID} {
		if _, err := f.tasks.AddDep(ctx, gated.ID, dep); err != nil {
			t.Fatalf("AddDep(%s) error = %v", dep, err)
		}
	}

	server, err := New(Options{Backend: f.backend, Clock: f.clock, Project: project.ID})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	page, err := server.page(ctx)
	if err != nil {
		t.Fatalf("page() error = %v", err)
	}
	view, ok := findTask(page.Waiting, gated.ID)
	if !ok {
		t.Fatalf("Waiting = %v, want the gated %s", taskIDs(page.Waiting), gated.ID)
	}
	if view.Reason != ReasonNeedsHuman {
		t.Fatalf("gated reason = %q, want %q", view.Reason, ReasonNeedsHuman)
	}
	for _, want := range []core.TicketID{human.ID, other.ID} {
		if !strings.Contains(view.Detail, string(want)) {
			t.Fatalf("gated detail = %q, want it to keep %s", view.Detail, want)
		}
	}
}

// Khoi 2026-10-14: a human-next item must show in the work board's
// Waiting / blocked lane, not only in the separate "Human next" panel, because
// the capture is blocked on a person. Ordinary ready agent work stays out of the
// lane.
func TestWaitingLaneCarriesHumanNextWork(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	project := f.addProject(t, "acme", "Acme")
	agent, err := f.actors.Add(ctx, core.ActorAgent, "bot")
	if err != nil {
		t.Fatalf("Add(actor) error = %v", err)
	}
	human, err := f.tasks.Add(ctx, app.TicketInput{ProjectID: project.ID, Kind: core.KindTask, Title: "pick the approach"})
	if err != nil {
		t.Fatalf("Add(human) error = %v", err)
	}
	agentTask, err := f.tasks.Add(ctx, app.TicketInput{
		ProjectID: project.ID, Kind: core.KindTask, Title: "agent work",
		AssigneeID: &agent.ID, Groomed: true, AcceptanceCriteria: []string{"works"},
	})
	if err != nil {
		t.Fatalf("Add(agent) error = %v", err)
	}

	server, err := New(Options{Backend: f.backend, Clock: f.clock, Project: project.ID})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	page, err := server.page(ctx)
	if err != nil {
		t.Fatalf("page() error = %v", err)
	}
	if !hasTask(page.NextHuman, human.ID) {
		t.Fatalf("NextHuman = %v, want %s", taskIDs(page.NextHuman), human.ID)
	}
	view, ok := findTask(page.Waiting, human.ID)
	if !ok {
		t.Fatalf("Waiting = %v, want the human-next %s in the lane too", taskIDs(page.Waiting), human.ID)
	}
	if view.Class != "ready-human" || view.Chip != "human next" {
		t.Fatalf("lane view = (class %q, chip %q), want (ready-human, human next)", view.Class, view.Chip)
	}
	if !hasTask(page.NextAgent, agentTask.ID) || hasTask(page.Waiting, agentTask.ID) {
		t.Fatalf("ready agent work %s must stay out of the waiting lane", agentTask.ID)
	}
}
