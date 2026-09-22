// Package conformance is the shared contract suite every store backend must
// pass. It is the definition of a backend.
package conformance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

type Factory func(t *testing.T) store.Backend

func Run(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("Project", func(t *testing.T) { testProject(t, factory(t)) })
	t.Run("Actor", func(t *testing.T) { testActor(t, factory(t)) })
	t.Run("Task", func(t *testing.T) { testTask(t, factory(t)) })
	t.Run("TaskDependents", func(t *testing.T) { testTaskDependents(t, factory(t)) })
	t.Run("TaskSearch", func(t *testing.T) { testTaskSearch(t, factory(t)) })
	t.Run("Artifact", func(t *testing.T) { testArtifact(t, factory(t)) })
	t.Run("ArtifactSearch", func(t *testing.T) { testArtifactSearch(t, factory(t)) })
	t.Run("Event", func(t *testing.T) { testEvent(t, factory(t)) })
}

func testProject(t *testing.T, be store.Backend) {
	t.Helper()
	ctx := context.Background()
	repo := be.Projects()

	project := &core.Project{ID: "prj-1", Name: "Acme", Policy: core.DefaultResolutionPolicy()}
	if err := repo.Create(ctx, project); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := repo.Create(ctx, project); !errors.Is(err, core.ErrAlreadyExists) {
		t.Fatalf("Create() duplicate error = %v, want ErrAlreadyExists", err)
	}

	got, err := repo.Get(ctx, "prj-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Name != "Acme" {
		t.Fatalf("Get().Name = %q, want Acme", got.Name)
	}

	got.Name = "Acme Renamed"
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	reloaded, err := repo.Get(ctx, "prj-1")
	if err != nil {
		t.Fatalf("Get() after update error = %v", err)
	}
	if reloaded.Name != "Acme Renamed" {
		t.Fatalf("Get() after update Name = %q, want Acme Renamed", reloaded.Name)
	}

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List() len = %d, want 1", len(list))
	}

	if err := repo.Delete(ctx, "prj-1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := repo.Get(ctx, "prj-1"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Get() after delete error = %v, want ErrNotFound", err)
	}
	if err := repo.Delete(ctx, "prj-1"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Delete() missing error = %v, want ErrNotFound", err)
	}
	missing := &core.Project{ID: "prj-x", Name: "X", Policy: core.DefaultResolutionPolicy()}
	if err := repo.Update(ctx, missing); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Update() missing error = %v, want ErrNotFound", err)
	}
}

func testActor(t *testing.T, be store.Backend) {
	t.Helper()
	ctx := context.Background()
	repo := be.Actors()

	khoi := &core.Actor{ID: "act-1", Kind: core.ActorHuman, Name: "Khoi", Active: true}
	claude := &core.Actor{ID: "act-2", Kind: core.ActorAgent, Name: "claude", Active: true}
	for _, actor := range []*core.Actor{khoi, claude} {
		if err := repo.Create(ctx, actor); err != nil {
			t.Fatalf("Create(%s) error = %v", actor.ID, err)
		}
	}
	if err := repo.Create(ctx, khoi); !errors.Is(err, core.ErrAlreadyExists) {
		t.Fatalf("Create() duplicate error = %v, want ErrAlreadyExists", err)
	}

	if _, err := repo.Get(ctx, "act-1"); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if _, err := repo.Get(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Get() missing error = %v, want ErrNotFound", err)
	}

	found, err := repo.FindByName(ctx, "claude")
	if err != nil {
		t.Fatalf("FindByName() error = %v", err)
	}
	if found.ID != "act-2" {
		t.Fatalf("FindByName().ID = %q, want act-2", found.ID)
	}
	if _, err := repo.FindByName(ctx, "nobody"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("FindByName() missing error = %v, want ErrNotFound", err)
	}

	khoi.Name = "Khoi Nguyen"
	if err := repo.Update(ctx, khoi); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	reloaded, err := repo.Get(ctx, "act-1")
	if err != nil {
		t.Fatalf("Get() after update error = %v", err)
	}
	if reloaded.Name != "Khoi Nguyen" {
		t.Fatalf("Get() after update Name = %q, want Khoi Nguyen", reloaded.Name)
	}

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List() len = %d, want 2", len(list))
	}

	if err := repo.Delete(ctx, "act-2"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := repo.Get(ctx, "act-2"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Get() after delete error = %v, want ErrNotFound", err)
	}
}

func testTask(t *testing.T, be store.Backend) {
	t.Helper()
	ctx := context.Background()
	repo := be.Tasks()

	notBefore := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	tasks := []*core.Task{
		{ID: "t-1", ProjectID: "prj-1", Repo: "backend", Kind: core.KindTask, Title: "one", Status: core.StatusTodo, NotBefore: &notBefore, Snooze: &core.Snooze{Indefinite: true}},
		{ID: "t-2", ProjectID: "prj-1", Repo: "backend", Kind: core.KindTask, Title: "two", Status: core.StatusDone},
		{ID: "t-3", ProjectID: "prj-2", Repo: "web", Kind: core.KindTask, Title: "three", Status: core.StatusTodo},
		{ID: "m-1", ProjectID: "prj-1", Kind: core.KindMilestone, Title: "release", Status: core.StatusTodo},
	}
	for _, task := range tasks {
		if err := repo.Create(ctx, task); err != nil {
			t.Fatalf("Create(%s) error = %v", task.ID, err)
		}
	}
	if err := repo.Create(ctx, tasks[0]); !errors.Is(err, core.ErrAlreadyExists) {
		t.Fatalf("Create() duplicate error = %v, want ErrAlreadyExists", err)
	}

	got, err := repo.Get(ctx, "t-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Title != "one" {
		t.Fatalf("Get().Title = %q, want one", got.Title)
	}
	if got.NotBefore == nil || !got.NotBefore.Equal(notBefore) {
		t.Fatalf("Get().NotBefore = %v, want %v", got.NotBefore, notBefore)
	}
	if got.Snooze == nil || !got.Snooze.Indefinite {
		t.Fatalf("Get().Snooze = %v, want indefinite", got.Snooze)
	}
	if _, err := repo.Get(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Get() missing error = %v, want ErrNotFound", err)
	}

	byProject, err := repo.List(ctx, store.TaskFilter{ProjectID: "prj-1"})
	if err != nil {
		t.Fatalf("List(project) error = %v", err)
	}
	if len(byProject) != 3 {
		t.Fatalf("List(project) len = %d, want 3", len(byProject))
	}

	byStatus, err := repo.List(ctx, store.TaskFilter{ProjectID: "prj-1", Statuses: []core.TaskStatus{core.StatusTodo}})
	if err != nil {
		t.Fatalf("List(status) error = %v", err)
	}
	if len(byStatus) != 2 {
		t.Fatalf("List(status) len = %d, want 2", len(byStatus))
	}

	kind := core.KindMilestone
	byKind, err := repo.List(ctx, store.TaskFilter{ProjectID: "prj-1", Kind: &kind})
	if err != nil {
		t.Fatalf("List(kind) error = %v", err)
	}
	if len(byKind) != 1 || byKind[0].ID != "m-1" {
		t.Fatalf("List(kind) = %v, want [m-1]", byKind)
	}

	repoName := "backend"
	byRepo, err := repo.List(ctx, store.TaskFilter{ProjectID: "prj-1", Repo: &repoName})
	if err != nil {
		t.Fatalf("List(repo) error = %v", err)
	}
	if len(byRepo) != 2 || byRepo[0].ID != "t-1" || byRepo[1].ID != "t-2" {
		t.Fatalf("List(repo) = %v, want [t-1 t-2]", byRepo)
	}

	got.Status = core.StatusInProgress
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	reloaded, err := repo.Get(ctx, "t-1")
	if err != nil {
		t.Fatalf("Get() after update error = %v", err)
	}
	if reloaded.Status != core.StatusInProgress {
		t.Fatalf("Get() after update Status = %q, want in_progress", reloaded.Status)
	}

	// Conditional update (compare-and-swap on UpdatedAt): the current token
	// succeeds, a stale one is rejected.
	token := reloaded.UpdatedAt
	reloaded.Title = "one cas"
	reloaded.UpdatedAt = token.Add(time.Minute)
	if err := repo.UpdateExpected(ctx, reloaded, token); err != nil {
		t.Fatalf("UpdateExpected(current) error = %v", err)
	}
	if err := repo.UpdateExpected(ctx, reloaded, token); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("UpdateExpected(stale) error = %v, want ErrConflict", err)
	}
	if _, err := repo.Get(ctx, "t-1"); err != nil {
		t.Fatalf("Get() after conditional update error = %v", err)
	}

	// Label filter: every provided label must be present (AND).
	labeled, err := repo.Get(ctx, "t-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	labeled.Labels = []string{"groomed", "urgent"}
	if err := repo.Update(ctx, labeled); err != nil {
		t.Fatalf("Update(labels t-1) error = %v", err)
	}
	second, err := repo.Get(ctx, "t-2")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	second.Labels = []string{"groomed"}
	if err := repo.Update(ctx, second); err != nil {
		t.Fatalf("Update(labels t-2) error = %v", err)
	}

	byLabel, err := repo.List(ctx, store.TaskFilter{ProjectID: "prj-1", Labels: []string{"groomed"}})
	if err != nil {
		t.Fatalf("List(label) error = %v", err)
	}
	if len(byLabel) != 2 {
		t.Fatalf("List(label groomed) len = %d, want 2", len(byLabel))
	}
	byAllLabels, err := repo.List(ctx, store.TaskFilter{ProjectID: "prj-1", Labels: []string{"groomed", "urgent"}})
	if err != nil {
		t.Fatalf("List(labels) error = %v", err)
	}
	if len(byAllLabels) != 1 || byAllLabels[0].ID != "t-1" {
		t.Fatalf("List(labels groomed+urgent) = %v, want [t-1]", byAllLabels)
	}
	byMissing, err := repo.List(ctx, store.TaskFilter{Labels: []string{"missing"}})
	if err != nil {
		t.Fatalf("List(missing label) error = %v", err)
	}
	if len(byMissing) != 0 {
		t.Fatalf("List(missing label) len = %d, want 0", len(byMissing))
	}

	if err := repo.Delete(ctx, "t-3"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := repo.Get(ctx, "t-3"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Get() after delete error = %v, want ErrNotFound", err)
	}
}

// testTaskDependents pins the DependsOn reverse-edge filter: it returns the
// tasks whose Deps name the given id, scoped by the rest of the filter.
func testTaskDependents(t *testing.T, be store.Backend) {
	t.Helper()
	ctx := context.Background()
	repo := be.Tasks()

	tasks := []*core.Task{
		{ID: "a", ProjectID: "prj-1", Kind: core.KindTask, Title: "a", Status: core.StatusTodo},
		{ID: "b", ProjectID: "prj-1", Kind: core.KindTask, Title: "b", Description: "tune retries", Status: core.StatusTodo, Deps: []core.TaskID{"a"}},
		{ID: "c", ProjectID: "prj-1", Kind: core.KindTask, Title: "c", Description: "tune retries", Status: core.StatusTodo, Deps: []core.TaskID{"a", "b"}},
		{ID: "d", ProjectID: "prj-2", Kind: core.KindTask, Title: "d", Status: core.StatusTodo, Deps: []core.TaskID{"a"}},
		// e matches the search probe but is not a dependent of a, so the
		// reverse-edge filter must exclude it.
		{ID: "e", ProjectID: "prj-1", Kind: core.KindTask, Title: "e", Description: "tune retries", Status: core.StatusTodo},
	}
	for _, task := range tasks {
		if err := repo.Create(ctx, task); err != nil {
			t.Fatalf("Create(%s) error = %v", task.ID, err)
		}
	}

	scoped := func(id core.TaskID) store.TaskFilter {
		return store.TaskFilter{ProjectID: "prj-1", DependsOn: &id}
	}
	assertTaskIDs(t, repo, ctx, scoped("a"), []core.TaskID{"b", "c"})
	assertTaskIDs(t, repo, ctx, scoped("b"), []core.TaskID{"c"})
	assertTaskIDs(t, repo, ctx, scoped("c"), nil)
	// Without a project scope the filter spans projects.
	assertTaskIDs(t, repo, ctx, store.TaskFilter{DependsOn: depPtr("a")}, []core.TaskID{"b", "c", "d"})
	// Search composes with the reverse-edge filter: e matches the probe but does
	// not depend on a, so the scoped search must drop it. Without the filter the
	// same query would return b, c, and e.
	assertTaskSearch(t, repo, ctx, store.TaskFilter{ProjectID: "prj-1"}, "retries", []core.TaskID{"b", "c", "e"})
	assertTaskSearch(t, repo, ctx, scoped("a"), "retries", []core.TaskID{"b", "c"})

	// Updating a task's deps reindexes its outgoing edges.
	cleared := &core.Task{ID: "c", ProjectID: "prj-1", Kind: core.KindTask, Title: "c", Status: core.StatusTodo}
	if err := repo.Update(ctx, cleared); err != nil {
		t.Fatalf("Update(c) error = %v", err)
	}
	assertTaskIDs(t, repo, ctx, scoped("a"), []core.TaskID{"b"})

	// Deleting a task removes its outgoing edges but leaves the records of
	// tasks that still name it as a dependency.
	if err := repo.Delete(ctx, "b"); err != nil {
		t.Fatalf("Delete(b) error = %v", err)
	}
	assertTaskIDs(t, repo, ctx, scoped("a"), nil)
	assertTaskIDs(t, repo, ctx, scoped("b"), nil)
}

func depPtr(id core.TaskID) *core.TaskID { return &id }

func assertTaskIDs(t *testing.T, repo store.TaskRepo, ctx context.Context, filter store.TaskFilter, want []core.TaskID) {
	t.Helper()
	tasks, err := repo.List(ctx, filter)
	if err != nil {
		t.Fatalf("List(%+v) error = %v", filter, err)
	}
	got := make([]core.TaskID, 0, len(tasks))
	for _, task := range tasks {
		got = append(got, task.ID)
	}
	if len(got) != len(want) {
		t.Fatalf("List(%+v) ids = %v, want %v", filter, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("List(%+v) ids = %v, want %v", filter, got, want)
		}
	}
}

func testTaskSearch(t *testing.T, be store.Backend) {
	t.Helper()
	ctx := context.Background()
	repo := be.Tasks()

	tasks := []*core.Task{
		{ID: "t-title", ProjectID: "prj-1", Repo: "backend", Kind: core.KindTask, Title: "Terraform notes", Description: "apply in devops", Status: core.StatusTodo},
		{ID: "t-body", ProjectID: "prj-1", Kind: core.KindTask, Title: "run scripts", Description: "terraform then kubectl", Status: core.StatusInProgress, Labels: []string{"groomed"}},
		{ID: "t-note", ProjectID: "prj-1", Kind: core.KindMilestone, Title: "unrelated", Description: "nothing here", Status: core.StatusTodo, Notes: []core.Note{{ID: "note-1", Body: "terraform in the notes"}}},
		{ID: "t-other", ProjectID: "prj-2", Kind: core.KindTask, Title: "Kubernetes notes", Description: "cluster upgrade", Status: core.StatusTodo},
		{ID: "t-sysnote", ProjectID: "prj-4", Kind: core.KindTask, Title: "quiet", Description: "nothing here", Status: core.StatusTodo, Notes: []core.Note{
			{ID: "note-sys", Body: "sysprobe only in the generated note", System: true},
			{ID: "note-human", Body: "humanprobe in a real note"},
		}},
	}
	for _, task := range tasks {
		if err := repo.Create(ctx, task); err != nil {
			t.Fatalf("Create(%s) error = %v", task.ID, err)
		}
	}

	// Repetition must not promote a lower-ranked column: a single title hit still
	// outranks many description hits, and a description hit outranks many note hits.
	repeats := []*core.Task{
		{ID: "t-rtitle", ProjectID: "prj-3", Kind: core.KindTask, Title: "delta", Description: "x", Status: core.StatusTodo},
		{ID: "t-rbody", ProjectID: "prj-3", Kind: core.KindTask, Title: "gamma", Description: "delta delta delta delta", Status: core.StatusTodo},
		{ID: "t-rnote", ProjectID: "prj-3", Kind: core.KindTask, Title: "epsilon", Description: "x", Status: core.StatusTodo, Notes: []core.Note{{ID: "note-r", Body: "delta delta delta delta delta"}}},
	}
	for _, task := range repeats {
		if err := repo.Create(ctx, task); err != nil {
			t.Fatalf("Create(%s) error = %v", task.ID, err)
		}
	}
	assertTaskSearch(t, repo, ctx, store.TaskFilter{ProjectID: "prj-3"}, "delta", []core.TaskID{"t-rtitle", "t-rbody", "t-rnote"})

	prj := store.TaskFilter{ProjectID: "prj-1"}
	milestone := core.KindMilestone
	todo := core.StatusTodo

	// Title outranks description, which outranks notes.
	assertTaskSearch(t, repo, ctx, prj, "terraform", []core.TaskID{"t-title", "t-body", "t-note"})
	assertTaskSearch(t, repo, ctx, prj, "terra", []core.TaskID{"t-title", "t-body", "t-note"})     // prefix
	assertTaskSearch(t, repo, ctx, prj, "TERRAFORM", []core.TaskID{"t-title", "t-body", "t-note"}) // case-insensitive
	assertTaskSearch(t, repo, ctx, prj, "terraform apply", []core.TaskID{"t-title"})               // all terms
	assertTaskSearch(t, repo, ctx, prj, "terraform kubectl", []core.TaskID{"t-body"})              // description vs notes
	assertTaskSearch(t, repo, ctx, prj, "notes", []core.TaskID{"t-title", "t-note"})               // title vs notes
	assertTaskSearch(t, repo, ctx, prj, "kubernetes", nil)                                         // other project
	assertTaskSearch(t, repo, ctx, store.TaskFilter{ProjectID: "prj-1", Statuses: []core.TaskStatus{todo}}, "terraform", []core.TaskID{"t-title", "t-note"})
	assertTaskSearch(t, repo, ctx, store.TaskFilter{ProjectID: "prj-1", Kind: &milestone}, "terraform", []core.TaskID{"t-note"})
	backendRepo := "backend"
	assertTaskSearch(t, repo, ctx, store.TaskFilter{ProjectID: "prj-1", Repo: &backendRepo}, "terraform", []core.TaskID{"t-title"})
	assertTaskSearch(t, repo, ctx, store.TaskFilter{ProjectID: "prj-1", Labels: []string{"groomed"}}, "terraform", []core.TaskID{"t-body"})

	// System notes are not indexed: a term that only appears in a generated note
	// finds nothing, while a real note on the same task still matches. The note
	// itself is retained for display.
	sysPrj := store.TaskFilter{ProjectID: "prj-4"}
	assertTaskSearch(t, repo, ctx, sysPrj, "sysprobe", nil)
	assertTaskSearch(t, repo, ctx, sysPrj, "humanprobe", []core.TaskID{"t-sysnote"})
	stored, err := repo.Get(ctx, "t-sysnote")
	if err != nil {
		t.Fatalf("Get(t-sysnote) error = %v", err)
	}
	if len(stored.Notes) != 2 {
		t.Fatalf("t-sysnote notes = %d, want 2", len(stored.Notes))
	}
	var hasSystem, hasHuman bool
	for _, note := range stored.Notes {
		if note.System {
			hasSystem = true
		} else {
			hasHuman = true
		}
	}
	if !hasSystem || !hasHuman {
		t.Fatalf("t-sysnote system/human markers = %v/%v, want both", hasSystem, hasHuman)
	}

	// Unmarking reindexes the note, so an update makes it searchable again.
	unmarked := &core.Task{ID: "t-sysnote", ProjectID: "prj-4", Kind: core.KindTask, Title: "quiet", Description: "nothing here", Status: core.StatusTodo, Notes: []core.Note{
		{ID: "note-sys", Body: "sysprobe only in the generated note"},
		{ID: "note-human", Body: "humanprobe in a real note"},
	}}
	if err := repo.Update(ctx, unmarked); err != nil {
		t.Fatalf("Update(t-sysnote) error = %v", err)
	}
	assertTaskSearch(t, repo, ctx, sysPrj, "sysprobe", []core.TaskID{"t-sysnote"})

	// Empty query returns everything in scope, ordered by title then id.
	assertTaskSearch(t, repo, ctx, prj, "", []core.TaskID{"t-body", "t-title", "t-note"})

	// Repeating a query is deterministic.
	assertTaskSearch(t, repo, ctx, prj, "terraform", []core.TaskID{"t-title", "t-body", "t-note"})

	// Updates reindex the title, description, and notes.
	updated := &core.Task{ID: "t-title", ProjectID: "prj-1", Repo: "backend", Kind: core.KindTask, Title: "Rust notes", Description: "cargo build", Status: core.StatusTodo, Notes: []core.Note{{ID: "note-x", Body: "reindexprobe note"}}}
	if err := repo.Update(ctx, updated); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	assertTaskSearch(t, repo, ctx, prj, "terraform", []core.TaskID{"t-body", "t-note"})
	assertTaskSearch(t, repo, ctx, prj, "cargo", []core.TaskID{"t-title"})
	assertTaskSearch(t, repo, ctx, prj, "reindexprobe", []core.TaskID{"t-title"})

	// Deletes unindex.
	if err := repo.Delete(ctx, "t-body"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	assertTaskSearch(t, repo, ctx, prj, "terraform", []core.TaskID{"t-note"})
}

func assertTaskSearch(t *testing.T, repo store.TaskRepo, ctx context.Context, filter store.TaskFilter, query string, want []core.TaskID) {
	t.Helper()
	hits, err := repo.Search(ctx, filter, query)
	if err != nil {
		t.Fatalf("Search(%q) error = %v", query, err)
	}
	if len(hits) != len(want) {
		t.Fatalf("Search(%q) = %v, want %v", query, taskHitIDs(hits), want)
	}
	for i, id := range want {
		if hits[i].Task.ID != id {
			t.Fatalf("Search(%q) = %v, want %v", query, taskHitIDs(hits), want)
		}
	}
}

func taskHitIDs(hits []store.TaskSearchHit) []core.TaskID {
	out := make([]core.TaskID, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.Task.ID)
	}
	return out
}

func testArtifactSearch(t *testing.T, be store.Backend) {
	t.Helper()
	ctx := context.Background()
	repo := be.Artifacts()
	taskID := core.TaskID("t-9")

	artifacts := []*core.Artifact{
		{ID: "art-t", ProjectID: "prj-1", Kind: core.ArtifactMemory, Title: "Terraform notes", Body: "apply in devops"},
		{ID: "art-b", ProjectID: "prj-1", TaskID: &taskID, Kind: core.ArtifactMemory, Title: "run scripts", Body: "terraform then kubectl"},
		{ID: "art-x", ProjectID: "prj-2", Kind: core.ArtifactDoc, Title: "Kubernetes notes", Body: "cluster upgrade"},
		{ID: "art-n", ProjectID: "prj-1", Kind: core.ArtifactMemory, Title: "unrelated", Brief: "quickstart guide", Body: "nothing here"},
	}
	for _, artifact := range artifacts {
		if err := repo.Create(ctx, artifact); err != nil {
			t.Fatalf("Create(%s) error = %v", artifact.ID, err)
		}
	}

	prj := store.ArtifactFilter{ProjectID: "prj-1"}
	memory := core.ArtifactMemory

	assertSearch(t, repo, ctx, prj, "terraform", []core.ArtifactID{"art-t", "art-b"})
	assertSearch(t, repo, ctx, prj, "terra", []core.ArtifactID{"art-t", "art-b"})     // prefix
	assertSearch(t, repo, ctx, prj, "TERRAFORM", []core.ArtifactID{"art-t", "art-b"}) // case-insensitive
	assertSearch(t, repo, ctx, prj, "terraform apply", []core.ArtifactID{"art-t"})    // all terms
	assertSearch(t, repo, ctx, prj, "terraform kubectl", []core.ArtifactID{"art-b"})  // title vs body
	assertSearch(t, repo, ctx, prj, "quickstart", []core.ArtifactID{"art-n"})         // brief-only match
	assertSearch(t, repo, ctx, prj, "kubernetes", nil)                                // other project
	assertSearch(t, repo, ctx, store.ArtifactFilter{ProjectID: "prj-1", Kind: &memory}, "terraform", []core.ArtifactID{"art-t", "art-b"})
	assertSearch(t, repo, ctx, store.ArtifactFilter{ProjectID: "prj-1", Kind: &memory}, "kubernetes", nil)
	assertSearch(t, repo, ctx, store.ArtifactFilter{TaskID: &taskID}, "terraform", []core.ArtifactID{"art-b"})

	// Empty query returns everything in scope, ordered by title then id.
	assertSearch(t, repo, ctx, prj, "", []core.ArtifactID{"art-b", "art-t", "art-n"})

	// Repeating a query is deterministic.
	assertSearch(t, repo, ctx, prj, "terraform", []core.ArtifactID{"art-t", "art-b"})

	// Updates reindex.
	updated := &core.Artifact{ID: "art-t", ProjectID: "prj-1", Kind: core.ArtifactMemory, Title: "Rust notes", Brief: "reindexprobe brief", Body: "cargo build"}
	if err := repo.Update(ctx, updated); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	assertSearch(t, repo, ctx, prj, "terraform", []core.ArtifactID{"art-b"})
	assertSearch(t, repo, ctx, prj, "cargo", []core.ArtifactID{"art-t"})
	assertSearch(t, repo, ctx, prj, "reindexprobe", []core.ArtifactID{"art-t"}) // Update re-indexes the brief

	// Deletes unindex.
	if err := repo.Delete(ctx, "art-b"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	assertSearch(t, repo, ctx, prj, "terraform", nil)

	// Term frequency must not let a repeated body or brief hit outrank a single
	// title hit: ranking is the shared LexicalScore, not raw bm25.
	tfPrj := store.ArtifactFilter{ProjectID: "prj-tf"}
	tfArtifacts := []*core.Artifact{
		{ID: "art-tf-title", ProjectID: "prj-tf", Kind: core.ArtifactMemory, Title: "beta"},
		{ID: "art-tf-brief", ProjectID: "prj-tf", Kind: core.ArtifactMemory, Title: "unrelated tf brief", Brief: "beta beta beta beta beta beta beta beta beta beta"},
		{ID: "art-tf-body", ProjectID: "prj-tf", Kind: core.ArtifactMemory, Title: "unrelated tf body", Body: "beta beta beta beta beta beta beta beta beta beta beta beta beta beta beta beta beta beta beta beta"},
	}
	for _, artifact := range tfArtifacts {
		if err := repo.Create(ctx, artifact); err != nil {
			t.Fatalf("Create(%s) error = %v", artifact.ID, err)
		}
	}
	assertSearch(t, repo, ctx, tfPrj, "beta", []core.ArtifactID{"art-tf-title", "art-tf-brief", "art-tf-body"})
}

func assertSearch(t *testing.T, repo store.ArtifactRepo, ctx context.Context, filter store.ArtifactFilter, query string, want []core.ArtifactID) {
	t.Helper()
	hits, err := repo.Search(ctx, filter, query)
	if err != nil {
		t.Fatalf("Search(%q) error = %v", query, err)
	}
	if len(hits) != len(want) {
		t.Fatalf("Search(%q) = %v, want %v", query, hitIDs(hits), want)
	}
	for i, id := range want {
		if hits[i].Artifact.ID != id {
			t.Fatalf("Search(%q) = %v, want %v", query, hitIDs(hits), want)
		}
	}
}

func hitIDs(hits []store.SearchHit) []core.ArtifactID {
	out := make([]core.ArtifactID, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.Artifact.ID)
	}
	return out
}

func testArtifact(t *testing.T, be store.Backend) {
	t.Helper()
	ctx := context.Background()
	repo := be.Artifacts()
	taskID := core.TaskID("t-1")

	artifacts := []*core.Artifact{
		{ID: "art-1", ProjectID: "prj-1", Kind: core.ArtifactSpec, Title: "spec"},
		{ID: "art-2", ProjectID: "prj-1", TaskID: &taskID, Kind: core.ArtifactMemory, Title: "memory", Brief: "remember briefly", Body: "remember"},
		{ID: "art-3", ProjectID: "prj-2", Kind: core.ArtifactDoc, Title: "doc"},
	}
	for _, artifact := range artifacts {
		if err := repo.Create(ctx, artifact); err != nil {
			t.Fatalf("Create(%s) error = %v", artifact.ID, err)
		}
	}
	if err := repo.Create(ctx, artifacts[0]); !errors.Is(err, core.ErrAlreadyExists) {
		t.Fatalf("Create() duplicate error = %v, want ErrAlreadyExists", err)
	}

	if _, err := repo.Get(ctx, "art-1"); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if _, err := repo.Get(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Get() missing error = %v, want ErrNotFound", err)
	}

	byProject, err := repo.List(ctx, store.ArtifactFilter{ProjectID: "prj-1"})
	if err != nil {
		t.Fatalf("List(project) error = %v", err)
	}
	if len(byProject) != 2 {
		t.Fatalf("List(project) len = %d, want 2", len(byProject))
	}
	for _, artifact := range byProject {
		if artifact.ID == "art-2" && artifact.Brief != "remember briefly" {
			t.Fatalf("List(project) art-2 Brief = %q, want the stored brief", artifact.Brief)
		}
	}

	byTask, err := repo.List(ctx, store.ArtifactFilter{TaskID: &taskID})
	if err != nil {
		t.Fatalf("List(task) error = %v", err)
	}
	if len(byTask) != 1 || byTask[0].ID != "art-2" {
		t.Fatalf("List(task) = %v, want [art-2]", byTask)
	}

	kind := core.ArtifactSpec
	byKind, err := repo.List(ctx, store.ArtifactFilter{Kind: &kind})
	if err != nil {
		t.Fatalf("List(kind) error = %v", err)
	}
	if len(byKind) != 1 || byKind[0].ID != "art-1" {
		t.Fatalf("List(kind) = %v, want [art-1]", byKind)
	}

	briefed, err := repo.Get(ctx, "art-2")
	if err != nil {
		t.Fatalf("Get(art-2) error = %v", err)
	}
	if briefed.Brief != "remember briefly" {
		t.Fatalf("Get(art-2) Brief = %q, want the stored brief", briefed.Brief)
	}

	got, err := repo.Get(ctx, "art-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	got.Title = "spec v2"
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	reloaded, err := repo.Get(ctx, "art-1")
	if err != nil {
		t.Fatalf("Get() after update error = %v", err)
	}
	if reloaded.Title != "spec v2" {
		t.Fatalf("Get() after update Title = %q, want spec v2", reloaded.Title)
	}

	if err := repo.Delete(ctx, "art-3"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := repo.Get(ctx, "art-3"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Get() after delete error = %v, want ErrNotFound", err)
	}
}

func testEvent(t *testing.T, be store.Backend) {
	t.Helper()
	ctx := context.Background()
	repo := be.Events()
	taskID := core.TaskID("t-1")

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	events := []*core.Event{
		{ID: "ev-1", ProjectID: "prj-1", Kind: core.EventProjectCreated, Summary: "created", CreatedAt: base},
		{ID: "ev-2", ProjectID: "prj-1", TaskID: &taskID, Kind: core.EventTaskCreated, Summary: "task created", CreatedAt: base.Add(time.Second)},
		{ID: "ev-3", ProjectID: "prj-1", TaskID: &taskID, Kind: core.EventTaskStatusChanged, Summary: "status", CreatedAt: base.Add(2 * time.Second)},
	}
	for _, event := range events {
		if err := repo.Append(ctx, event); err != nil {
			t.Fatalf("Append(%s) error = %v", event.ID, err)
		}
	}

	all, err := repo.List(ctx, store.EventFilter{ProjectID: "prj-1"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("List() len = %d, want 3", len(all))
	}
	if all[0].ID != "ev-3" || all[1].ID != "ev-2" || all[2].ID != "ev-1" {
		t.Fatalf("List() order = [%s %s %s], want newest first [ev-3 ev-2 ev-1]", all[0].ID, all[1].ID, all[2].ID)
	}

	byTask, err := repo.List(ctx, store.EventFilter{TaskID: &taskID})
	if err != nil {
		t.Fatalf("List(task) error = %v", err)
	}
	if len(byTask) != 2 {
		t.Fatalf("List(task) len = %d, want 2", len(byTask))
	}

	byKind, err := repo.List(ctx, store.EventFilter{Kinds: []core.EventKind{core.EventTaskStatusChanged}})
	if err != nil {
		t.Fatalf("List(kind) error = %v", err)
	}
	if len(byKind) != 1 || byKind[0].ID != "ev-3" {
		t.Fatalf("List(kind) = %v, want [ev-3]", byKind)
	}

	limited, err := repo.List(ctx, store.EventFilter{ProjectID: "prj-1", Limit: 2})
	if err != nil {
		t.Fatalf("List(limit) error = %v", err)
	}
	if len(limited) != 2 || limited[0].ID != "ev-3" {
		t.Fatalf("List(limit) = %v, want [ev-3 ev-2]", limited)
	}

	since := base.Add(time.Second)
	sinceEvents, err := repo.List(ctx, store.EventFilter{Since: &since})
	if err != nil {
		t.Fatalf("List(since) error = %v", err)
	}
	if len(sinceEvents) != 2 {
		t.Fatalf("List(since) len = %d, want 2", len(sinceEvents))
	}
}
