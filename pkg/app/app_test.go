package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/memory"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

type seqIDs struct{ counts map[string]int }

func (s *seqIDs) NewID(prefix string) string {
	if s.counts == nil {
		s.counts = make(map[string]int)
	}
	s.counts[prefix]++
	return fmt.Sprintf("%s-%d", prefix, s.counts[prefix])
}

type harness struct {
	backend   store.Backend
	projects  *ProjectService
	tasks     *TaskService
	actors    *ActorService
	artifacts *ArtifactService
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	backend := memory.New()
	clock := fixedClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ids := &seqIDs{}
	t.Cleanup(func() { _ = backend.Close() })
	return &harness{
		backend:   backend,
		projects:  NewProjectService(backend, clock, ids),
		tasks:     NewTaskService(backend, clock, ids),
		actors:    NewActorService(backend, clock, ids),
		artifacts: NewArtifactService(backend, clock, ids),
	}
}

func equalIDs(got, want []core.TaskID) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func (h *harness) newProject(t *testing.T) *core.Project {
	t.Helper()
	project, err := h.projects.Create(context.Background(), "Acme", "demo", nil)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return project
}

func TestProjectCreateEmitsEvent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)

	if project.Policy.TaskStatuses == nil {
		t.Fatal("expected a default resolution policy")
	}
	events, err := h.backend.Events().List(ctx, store.EventFilter{ProjectID: project.ID})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(events) != 1 || events[0].Kind != core.EventProjectCreated {
		t.Fatalf("events = %v, want one project.created", events)
	}
}

func TestTaskRequiresProject(t *testing.T) {
	h := newHarness(t)
	_, err := h.tasks.Add(context.Background(), TaskInput{ProjectID: "missing", Title: "x"})
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Add() error = %v, want ErrNotFound", err)
	}
}

func TestTaskDependenciesAndReadiness(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)

	first, err := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Title: "first"})
	if err != nil {
		t.Fatalf("Add(first) error = %v", err)
	}
	second, err := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Title: "second"})
	if err != nil {
		t.Fatalf("Add(second) error = %v", err)
	}
	if _, err := h.tasks.AddDep(ctx, second.ID, first.ID); err != nil {
		t.Fatalf("AddDep() error = %v", err)
	}

	snapshot := mustSnapshot(t, h, project.ID)
	if !equalIDs(snapshot.Graph.ReadySet(), []core.TaskID{first.ID}) {
		t.Fatalf("ReadySet() = %v, want [%s]", snapshot.Graph.ReadySet(), first.ID)
	}

	if _, err := h.tasks.SetStatus(ctx, first.ID, core.StatusReadyForReview); err != nil {
		t.Fatalf("SetStatus() error = %v", err)
	}
	snapshot = mustSnapshot(t, h, project.ID)
	if !equalIDs(snapshot.Graph.ReadySet(), []core.TaskID{second.ID}) {
		t.Fatalf("ReadySet() after review = %v, want [%s]", snapshot.Graph.ReadySet(), second.ID)
	}
}

func TestAddDepRejectsCycle(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)

	first, _ := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Title: "first"})
	second, _ := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Title: "second"})
	if _, err := h.tasks.AddDep(ctx, second.ID, first.ID); err != nil {
		t.Fatalf("AddDep() error = %v", err)
	}
	if _, err := h.tasks.AddDep(ctx, first.ID, second.ID); !errors.Is(err, core.ErrCycle) {
		t.Fatalf("AddDep() cycle error = %v, want ErrCycle", err)
	}
}

func TestMilestoneGatesDependents(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)

	milestone, err := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Kind: core.KindMilestone, Title: "release"})
	if err != nil {
		t.Fatalf("Add(milestone) error = %v", err)
	}
	follow, err := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Title: "after release"})
	if err != nil {
		t.Fatalf("Add(follow) error = %v", err)
	}
	if _, err := h.tasks.AddDep(ctx, follow.ID, milestone.ID); err != nil {
		t.Fatalf("AddDep() error = %v", err)
	}

	if _, err := h.tasks.SetStatus(ctx, milestone.ID, core.StatusReadyForReview); err != nil {
		t.Fatalf("SetStatus(review) error = %v", err)
	}
	snapshot := mustSnapshot(t, h, project.ID)
	for _, id := range snapshot.Graph.ReadySet() {
		if id == follow.ID {
			t.Fatal("milestone ready_for_review must not unblock dependents")
		}
	}

	if _, err := h.tasks.SetStatus(ctx, milestone.ID, core.StatusDone); err != nil {
		t.Fatalf("SetStatus(done) error = %v", err)
	}
	snapshot = mustSnapshot(t, h, project.ID)
	if !equalIDs(snapshot.Graph.ReadySet(), []core.TaskID{follow.ID}) {
		t.Fatalf("ReadySet() after milestone done = %v, want [%s]", snapshot.Graph.ReadySet(), follow.ID)
	}
}

func TestAssignAndReadyByActor(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)

	agent, err := h.actors.Add(ctx, core.ActorAgent, "claude")
	if err != nil {
		t.Fatalf("Add(agent) error = %v", err)
	}
	human, err := h.actors.Add(ctx, core.ActorHuman, "Khoi")
	if err != nil {
		t.Fatalf("Add(human) error = %v", err)
	}

	agentTask, _ := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Title: "agent work"})
	if _, err := h.tasks.Assign(ctx, agentTask.ID, &agent.ID); err != nil {
		t.Fatalf("Assign(agent) error = %v", err)
	}
	humanTask, _ := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Title: "human work"})
	if _, err := h.tasks.Assign(ctx, humanTask.ID, &human.ID); err != nil {
		t.Fatalf("Assign(human) error = %v", err)
	}
	unownedTask, _ := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Title: "unowned"})

	snapshot := mustSnapshot(t, h, project.ID)
	if !equalIDs(snapshot.Ready.Agent, []core.TaskID{agentTask.ID}) {
		t.Fatalf("Ready.Agent = %v, want [%s]", snapshot.Ready.Agent, agentTask.ID)
	}
	if !equalIDs(snapshot.Ready.Human, []core.TaskID{humanTask.ID, unownedTask.ID}) {
		t.Fatalf("Ready.Human = %v, want [%s %s]", snapshot.Ready.Human, humanTask.ID, unownedTask.ID)
	}
}

func TestLoadAllSnapshotMergesProjects(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	alpha, err := h.projects.Create(ctx, "Alpha", "", nil)
	if err != nil {
		t.Fatalf("Create(alpha) error = %v", err)
	}
	beta, err := h.projects.Create(ctx, "Beta", "", nil)
	if err != nil {
		t.Fatalf("Create(beta) error = %v", err)
	}
	first, _ := h.tasks.Add(ctx, TaskInput{ProjectID: alpha.ID, Title: "first"})
	second, _ := h.tasks.Add(ctx, TaskInput{ProjectID: beta.ID, Title: "second"})

	snapshot, err := LoadAllSnapshot(ctx, h.backend, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("LoadAllSnapshot() error = %v", err)
	}
	if snapshot.Project != nil {
		t.Fatalf("Project = %v, want nil", snapshot.Project)
	}
	if len(snapshot.Tasks) != 2 {
		t.Fatalf("Tasks = %d, want 2", len(snapshot.Tasks))
	}
	ready := map[core.TaskID]bool{}
	for _, id := range snapshot.Ready.Human {
		ready[id] = true
	}
	if !ready[first.ID] || !ready[second.ID] {
		t.Fatalf("Ready.Human = %v, want both %s and %s", snapshot.Ready.Human, first.ID, second.ID)
	}
}

func TestActorIDIsSlugOfName(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	actor, err := h.actors.Add(ctx, core.ActorAgent, "Claude Code")
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if actor.ID != "claude-code" {
		t.Fatalf("actor ID = %q, want claude-code", actor.ID)
	}
	resolved, err := h.actors.Resolve(ctx, "claude-code")
	if err != nil {
		t.Fatalf("Resolve(claude-code) error = %v", err)
	}
	if resolved.ID != actor.ID {
		t.Fatalf("Resolve().ID = %q, want %q", resolved.ID, actor.ID)
	}
}

func TestActorIDFallsBackWhenNameHasNoSlug(t *testing.T) {
	h := newHarness(t)
	actor, err := h.actors.Add(context.Background(), core.ActorHuman, "!!!")
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if actor.ID == "" || actor.ID == "!!!" {
		t.Fatalf("actor ID = %q, want a generated fallback", actor.ID)
	}
}

func TestActorSlugCollisionRejected(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.actors.Add(ctx, core.ActorAgent, "Claude Code"); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := h.actors.Add(ctx, core.ActorAgent, "claude-code"); !errors.Is(err, core.ErrAlreadyExists) {
		t.Fatalf("Add() collision error = %v, want ErrAlreadyExists", err)
	}
}

func TestActorNamesAreUnique(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.actors.Add(ctx, core.ActorHuman, "Khoi"); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := h.actors.Add(ctx, core.ActorHuman, "Khoi"); !errors.Is(err, core.ErrAlreadyExists) {
		t.Fatalf("Add() duplicate error = %v, want ErrAlreadyExists", err)
	}
	resolved, err := h.actors.Resolve(ctx, "Khoi")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Kind != core.ActorHuman {
		t.Fatalf("Resolve().Kind = %q, want human", resolved.Kind)
	}
}

func TestArtifactsAndMemorySearch(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)

	if _, err := h.artifacts.Add(ctx, ArtifactInput{ProjectID: project.ID, Kind: core.ArtifactSpec, Title: "Product spec", Body: "ship it"}); err != nil {
		t.Fatalf("Add(spec) error = %v", err)
	}
	if _, err := h.artifacts.Add(ctx, ArtifactInput{ProjectID: project.ID, Kind: core.ArtifactMemory, Title: "Deploy notes", Body: "terraform apply in devops repo"}); err != nil {
		t.Fatalf("Add(memory) error = %v", err)
	}

	found, err := h.artifacts.Search(ctx, store.ArtifactFilter{ProjectID: project.ID}, "terraform")
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(found) != 1 || found[0].Title != "Deploy notes" {
		t.Fatalf("Search() = %v, want Deploy notes", found)
	}
	none, err := h.artifacts.Search(ctx, store.ArtifactFilter{ProjectID: project.ID}, "kubernetes")
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("Search() = %v, want none", none)
	}
}

func TestArtifactSearchRanksTitleBeforeBody(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)

	titles := []string{"run scripts", "Terraform notes"}
	expected := []string{"Terraform notes", "run scripts"}
	for _, title := range titles {
		body := "nothing"
		if title == "run scripts" {
			body = "terraform apply"
		}
		if _, err := h.artifacts.Add(ctx, ArtifactInput{ProjectID: project.ID, Kind: core.ArtifactMemory, Title: title, Body: body}); err != nil {
			t.Fatalf("Add(%q) error = %v", title, err)
		}
	}

	found, err := h.artifacts.Search(ctx, store.ArtifactFilter{ProjectID: project.ID}, "terraform")
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	got := make([]string, 0, len(found))
	for _, artifact := range found {
		got = append(got, artifact.Title)
	}
	if len(got) != len(expected) {
		t.Fatalf("Search() = %v, want %v", got, expected)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Fatalf("Search() order = %v, want %v", got, expected)
		}
	}
}

func TestArtifactSearchFiltersByKind(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)

	if _, err := h.artifacts.Add(ctx, ArtifactInput{ProjectID: project.ID, Kind: core.ArtifactDoc, Title: "terraform doc", Body: "keep"}); err != nil {
		t.Fatalf("Add(doc) error = %v", err)
	}
	if _, err := h.artifacts.Add(ctx, ArtifactInput{ProjectID: project.ID, Kind: core.ArtifactMemory, Title: "terraform memory", Body: "keep"}); err != nil {
		t.Fatalf("Add(memory) error = %v", err)
	}

	kind := core.ArtifactMemory
	found, err := h.artifacts.Search(ctx, store.ArtifactFilter{ProjectID: project.ID, Kind: &kind}, "terraform")
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(found) != 1 || found[0].Title != "terraform memory" {
		t.Fatalf("Search() = %v, want only the memory artifact", found)
	}
}

func TestAddNote(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	task, _ := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Title: "decide"})

	updated, err := h.tasks.AddNote(ctx, task.ID, NoteInput{
		Body:  "need Khoi's call",
		Links: []core.Link{{Kind: core.LinkPR, URL: "https://example.com/pr/1"}},
	})
	if err != nil {
		t.Fatalf("AddNote() error = %v", err)
	}
	if len(updated.Notes) != 1 || updated.Notes[0].Body != "need Khoi's call" {
		t.Fatalf("Notes = %v, want one note", updated.Notes)
	}
}

func TestSlug(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Kloobot", "kloobot"},
		{"Acme Product!", "acme-product"},
		{"  Multiple   Spaces  ", "multiple-spaces"},
		{"UPPER_case-42", "upper-case-42"},
		{"!!!", ""},
	}
	for _, tt := range tests {
		if got := Slug(tt.in); got != tt.want {
			t.Errorf("Slug(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestProjectCreateUsesSlugID(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project, err := h.projects.Create(ctx, "Acme Product!", "", nil)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if project.ID != "acme-product" {
		t.Fatalf("ID = %q, want acme-product", project.ID)
	}
	if _, err := h.projects.Create(ctx, "Acme Product!", "", nil); !errors.Is(err, core.ErrAlreadyExists) {
		t.Fatalf("Create() duplicate error = %v, want ErrAlreadyExists", err)
	}
}

func TestTaskKindUpdate(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	task, err := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Title: "feature flag rollout"})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	milestone := core.KindMilestone
	updated, err := h.tasks.Update(ctx, task.ID, TaskUpdate{Kind: &milestone})
	if err != nil {
		t.Fatalf("Update(kind) error = %v", err)
	}
	if !updated.IsMilestone() {
		t.Fatalf("Kind = %q, want milestone", updated.Kind)
	}

	bad := core.TaskKind("epic")
	if _, err := h.tasks.Update(ctx, task.ID, TaskUpdate{Kind: &bad}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Update(bad kind) error = %v, want ErrInvalid", err)
	}
}

func TestTaskExplicitID(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)

	id := core.TaskID("APS-99999")
	task, err := h.tasks.Add(ctx, TaskInput{ID: &id, ProjectID: project.ID, Title: "imported"})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if task.ID != id {
		t.Fatalf("ID = %q, want %q", task.ID, id)
	}
	if _, err := h.tasks.Add(ctx, TaskInput{ID: &id, ProjectID: project.ID, Title: "dup"}); !errors.Is(err, core.ErrAlreadyExists) {
		t.Fatalf("Add() duplicate error = %v, want ErrAlreadyExists", err)
	}
}

func TestProjectRepoLifecycle(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)

	updated, err := h.projects.AddRepo(ctx, project.ID, core.Repository{Name: "backend", Description: "API service"})
	if err != nil {
		t.Fatalf("AddRepo() error = %v", err)
	}
	if len(updated.Repos) != 1 {
		t.Fatalf("Repos len = %d, want 1", len(updated.Repos))
	}
	if _, err := h.projects.AddRepo(ctx, project.ID, core.Repository{Name: "backend"}); !errors.Is(err, core.ErrAlreadyExists) {
		t.Fatalf("AddRepo() duplicate error = %v, want ErrAlreadyExists", err)
	}

	brief := "API and workers"
	if _, err := h.projects.UpdateRepo(ctx, project.ID, "backend", RepositoryUpdate{Description: &brief}); err != nil {
		t.Fatalf("UpdateRepo() error = %v", err)
	}
	reloaded, err := h.projects.Get(ctx, project.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if reloaded.Repos[0].Description != brief {
		t.Fatalf("repo brief = %q, want %q", reloaded.Repos[0].Description, brief)
	}

	if _, err := h.projects.RemoveRepo(ctx, project.ID, "backend"); err != nil {
		t.Fatalf("RemoveRepo() error = %v", err)
	}
	reloaded, _ = h.projects.Get(ctx, project.ID)
	if len(reloaded.Repos) != 0 {
		t.Fatalf("Repos len = %d, want 0", len(reloaded.Repos))
	}
	if _, err := h.projects.RemoveRepo(ctx, project.ID, "backend"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("RemoveRepo() missing error = %v, want ErrNotFound", err)
	}
}

func TestTaskRepoAssociation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project, err := h.projects.Create(ctx, "Acme", "", []core.Repository{{Name: "data"}, {Name: "devops"}})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	dataTask, err := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Repo: "data", Title: "data task"})
	if err != nil {
		t.Fatalf("Add(repo data) error = %v", err)
	}
	if _, err := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Repo: "nope", Title: "bad"}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Add(bad repo) error = %v, want ErrInvalid", err)
	}

	repo := "data"
	list, err := h.tasks.List(ctx, store.TaskFilter{ProjectID: project.ID, Repo: &repo})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 1 || list[0].ID != dataTask.ID {
		t.Fatalf("List(repo data) = %v, want [%s]", list, dataTask.ID)
	}

	newRepo := "devops"
	updated, err := h.tasks.Update(ctx, dataTask.ID, TaskUpdate{Repo: &newRepo})
	if err != nil {
		t.Fatalf("Update(repo) error = %v", err)
	}
	if updated.Repo != "devops" {
		t.Fatalf("Repo = %q, want devops", updated.Repo)
	}
}

func TestTaskSetAppliesTypedFields(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	task, err := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Title: "original"})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	status := core.StatusInProgress
	priority := 7
	title := "renamed"
	description := "long body"
	labels := []string{"ui", "api"}
	updated, err := h.tasks.Set(ctx, task.ID, TaskSet{
		Status:      &status,
		Priority:    &priority,
		Title:       &title,
		Description: &description,
		Labels:      labels,
	})
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if updated.Status != status {
		t.Errorf("Status = %q, want %q", updated.Status, status)
	}
	if updated.Priority != priority {
		t.Errorf("Priority = %d, want %d", updated.Priority, priority)
	}
	if updated.Title != title {
		t.Errorf("Title = %q, want %q", updated.Title, title)
	}
	if updated.Description != description {
		t.Errorf("Description = %q, want %q", updated.Description, description)
	}
	if len(updated.Labels) != 2 || updated.Labels[0] != "ui" || updated.Labels[1] != "api" {
		t.Errorf("Labels = %v, want [ui api]", updated.Labels)
	}
}

func TestTaskSetRejectsInvalidStatus(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	task, _ := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Title: "x"})

	bad := core.TaskStatus("bogus")
	if _, err := h.tasks.Set(ctx, task.ID, TaskSet{Status: &bad}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Set(bad status) error = %v, want ErrInvalid", err)
	}
}

func TestTaskSetValidatesRepo(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project, err := h.projects.Create(ctx, "Acme", "", []core.Repository{{Name: "data"}})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	task, _ := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Title: "x"})

	bad := "nope"
	if _, err := h.tasks.Set(ctx, task.ID, TaskSet{Repo: &bad}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Set(bad repo) error = %v, want ErrInvalid", err)
	}
}

func TestSetStatusEmitsStatusEvent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	task, _ := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Title: "x"})

	if _, err := h.tasks.SetStatus(ctx, task.ID, core.StatusDone); err != nil {
		t.Fatalf("SetStatus() error = %v", err)
	}
	events, err := h.backend.Events().List(ctx, store.EventFilter{TaskID: &task.ID})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	var found *core.Event
	for i := range events {
		if events[i].Kind == core.EventTaskStatusChanged {
			found = events[i]
		}
	}
	if found == nil {
		t.Fatalf("no status event in %v", events)
	}
	if found.Data["to"] != "done" {
		t.Fatalf("status event data = %v, want to=done", found.Data)
	}
}

func TestTaskSetClearsLabels(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	task, _ := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Title: "x"})

	empty := []string{}
	if _, err := h.tasks.Set(ctx, task.ID, TaskSet{Labels: empty}); err != nil {
		t.Fatalf("Set(clear labels) error = %v", err)
	}
	reloaded, _ := h.tasks.Get(ctx, task.ID)
	if len(reloaded.Labels) != 0 {
		t.Fatalf("Labels = %v, want empty", reloaded.Labels)
	}
}

func TestTaskSetRejectsStaleExpectation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	task, err := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Title: "x"})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	stale := task.UpdatedAt

	concurrent := *task
	concurrent.Title = "other"
	concurrent.UpdatedAt = stale.Add(time.Minute)
	if err := h.backend.Tasks().Update(ctx, &concurrent); err != nil {
		t.Fatalf("concurrent Update() error = %v", err)
	}

	title := "mine"
	if _, err := h.tasks.Set(ctx, task.ID, TaskSet{Title: &title, Expect: &stale}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("Set(stale expect) error = %v, want ErrConflict", err)
	}

	fresh := concurrent.UpdatedAt
	if _, err := h.tasks.Set(ctx, task.ID, TaskSet{Title: &title, Expect: &fresh}); err != nil {
		t.Fatalf("Set(fresh expect) error = %v", err)
	}
}

func TestTaskClaimIsExclusive(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	task, err := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Title: "work"})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	claude, _ := h.actors.Add(ctx, core.ActorAgent, "claude")
	other, _ := h.actors.Add(ctx, core.ActorAgent, "other")

	claimed, err := h.tasks.Claim(ctx, task.ID, claude.ID)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claimed.AssigneeID == nil || *claimed.AssigneeID != claude.ID {
		t.Fatalf("Claim().AssigneeID = %v, want %s", claimed.AssigneeID, claude.ID)
	}
	if _, err := h.tasks.Claim(ctx, task.ID, other.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("second Claim() error = %v, want ErrConflict", err)
	}
	// The holder reclaiming is a no-op, not a conflict.
	if _, err := h.tasks.Claim(ctx, task.ID, claude.ID); err != nil {
		t.Fatalf("re-Claim() error = %v", err)
	}
}

func mustSnapshot(t *testing.T, h *harness, projectID core.ProjectID) *Snapshot {
	t.Helper()
	snapshot, err := LoadSnapshot(context.Background(), h.backend, projectID, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("LoadSnapshot() error = %v", err)
	}
	return snapshot
}

func TestTaskSetNotBefore(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	task, err := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Title: "soak"})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	deadline := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	updated, err := h.tasks.Set(ctx, task.ID, TaskSet{NotBefore: &deadline})
	if err != nil {
		t.Fatalf("Set(not_before) error = %v", err)
	}
	if updated.NotBefore == nil || !updated.NotBefore.Equal(deadline) {
		t.Fatalf("NotBefore = %v, want %v", updated.NotBefore, deadline)
	}

	cleared, err := h.tasks.Set(ctx, task.ID, TaskSet{ClearNotBefore: true})
	if err != nil {
		t.Fatalf("Set(clear) error = %v", err)
	}
	if cleared.NotBefore != nil {
		t.Fatalf("NotBefore = %v, want nil after clear", cleared.NotBefore)
	}
}

func TestSnapshotExcludesNotBeforeUntilDeadline(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	task, err := h.tasks.Add(ctx, TaskInput{ProjectID: project.ID, Title: "soak"})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	deadline := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if _, err := h.tasks.Set(ctx, task.ID, TaskSet{NotBefore: &deadline}); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	before, err := LoadSnapshot(ctx, h.backend, project.ID, deadline.Add(-time.Hour))
	if err != nil {
		t.Fatalf("LoadSnapshot() error = %v", err)
	}
	if got := unionIDs(before.Ready.Agent, before.Ready.Human); len(got) != 0 {
		t.Fatalf("ready before deadline = %v, want none", got)
	}

	after, err := LoadSnapshot(ctx, h.backend, project.ID, deadline.Add(time.Hour))
	if err != nil {
		t.Fatalf("LoadSnapshot() error = %v", err)
	}
	got := append(append([]core.TaskID{}, after.Ready.Agent...), after.Ready.Human...)
	if len(got) != 1 || got[0] != task.ID {
		t.Fatalf("ready after deadline = %v, want [%s]", got, task.ID)
	}
}

func unionIDs(groups ...[]core.TaskID) []core.TaskID {
	seen := make(map[core.TaskID]struct{})
	var out []core.TaskID
	for _, group := range groups {
		for _, id := range group {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}
