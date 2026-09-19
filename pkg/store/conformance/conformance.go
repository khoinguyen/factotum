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
	t.Run("Artifact", func(t *testing.T) { testArtifact(t, factory(t)) })
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

	tasks := []*core.Task{
		{ID: "t-1", ProjectID: "prj-1", Repo: "backend", Kind: core.KindTask, Title: "one", Status: core.StatusTodo},
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

func testArtifact(t *testing.T, be store.Backend) {
	t.Helper()
	ctx := context.Background()
	repo := be.Artifacts()
	taskID := core.TaskID("t-1")

	artifacts := []*core.Artifact{
		{ID: "art-1", ProjectID: "prj-1", Kind: core.ArtifactSpec, Title: "spec"},
		{ID: "art-2", ProjectID: "prj-1", TaskID: &taskID, Kind: core.ArtifactMemory, Title: "memory", Body: "remember"},
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
