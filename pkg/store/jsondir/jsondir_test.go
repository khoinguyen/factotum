package jsondir

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/conformance"
)

func newBackend(t *testing.T) *Backend {
	t.Helper()
	dir := t.TempDir()
	backend, err := Open(context.Background(), store.Config{Backend: "jsondir", Options: map[string]string{"path": dir}})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	return backend.(*Backend)
}

func TestConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) store.Backend {
		dir := t.TempDir()
		backend, err := Open(context.Background(), store.Config{Backend: "jsondir", Options: map[string]string{"path": dir}})
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		t.Cleanup(func() { _ = backend.Close() })
		return backend
	})
}

func TestPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	cfg := store.Config{Backend: "jsondir", Options: map[string]string{"path": dir}}

	first, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	task := &core.Ticket{ID: "t-1", ProjectID: "prj-1", Kind: core.KindTask, Title: "one", Status: core.StatusTodo, Description: "body text"}
	if err := first.Tickets().Create(ctx, task); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	second, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	got, err := second.Tickets().Get(ctx, "t-1")
	if err != nil {
		t.Fatalf("Get() after reopen error = %v", err)
	}
	if got.Title != "one" || got.Description != "body text" {
		t.Fatalf("Get() after reopen = %#v, want the stored task", got)
	}
}

func TestLayoutIsDirectoryOfMarkdownFiles(t *testing.T) {
	backend := newBackend(t)
	ctx := context.Background()
	if err := backend.Projects().Create(ctx, &core.Project{ID: "prj-1", Name: "Acme", Policy: core.DefaultResolutionPolicy()}); err != nil {
		t.Fatalf("Create(project) error = %v", err)
	}
	if err := backend.Tickets().Create(ctx, &core.Ticket{ID: "t-1", ProjectID: "prj-1", Kind: core.KindTask, Title: "one", Status: core.StatusTodo}); err != nil {
		t.Fatalf("Create(task) error = %v", err)
	}
	if err := backend.Actors().Create(ctx, &core.Actor{ID: "act-1", Kind: core.ActorHuman, Name: "Khoi", Active: true}); err != nil {
		t.Fatalf("Create(actor) error = %v", err)
	}
	if err := backend.Artifacts().Create(ctx, &core.Artifact{ID: "art-1", ProjectID: "prj-1", Kind: core.ArtifactSpec, Title: "spec"}); err != nil {
		t.Fatalf("Create(artifact) error = %v", err)
	}
	if err := backend.Events().Append(ctx, &core.Event{ID: "ev-1", ProjectID: "prj-1", Kind: core.EventProjectCreated, Summary: "created"}); err != nil {
		t.Fatalf("Append(event) error = %v", err)
	}

	for _, rel := range []string{"projects/prj-1.md", "tasks/t-1.md", "actors/act-1.md", "artifacts/art-1.md"} {
		if _, err := os.Stat(filepath.Join(backend.root, rel)); err != nil {
			t.Fatalf("expected file %s: %v", rel, err)
		}
	}
	shards, err := filepath.Glob(filepath.Join(backend.root, "events", "*.jsonl"))
	if err != nil || len(shards) == 0 {
		t.Fatalf("expected an event shard file, glob=%v err=%v", shards, err)
	}
}

func TestWriteTouchesOnlyAffectedFile(t *testing.T) {
	backend := newBackend(t)
	ctx := context.Background()
	for _, id := range []core.TicketID{"t-1", "t-2"} {
		if err := backend.Tickets().Create(ctx, &core.Ticket{ID: id, ProjectID: "prj-1", Kind: core.KindTask, Title: string(id), Status: core.StatusTodo}); err != nil {
			t.Fatalf("Create(%s) error = %v", id, err)
		}
	}
	pathA := filepath.Join(backend.root, "tasks", "t-1.md")
	before, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	taskB, err := backend.Tickets().Get(ctx, "t-2")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	taskB.Title = "two renamed"
	if err := backend.Tickets().Update(ctx, taskB); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	after, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("updating t-2 changed t-1:\n before=%s\n after=%s", before, after)
	}
}

func TestUnknownFrontmatterSurvivesUpdate(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	cfg := store.Config{Backend: "jsondir", Options: map[string]string{"path": dir}}

	backend, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := backend.Tickets().Create(ctx, &core.Ticket{ID: "t-1", ProjectID: "prj-1", Kind: core.KindTask, Title: "one", Status: core.StatusTodo}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	path := filepath.Join(dir, "tasks", "t-1.md")
	hand, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	injected := strings.Replace(string(hand), "project: prj-1\n", "project: prj-1\norigin: human\ncustom:\n  nested: 1\n", 1)
	if err := os.WriteFile(path, []byte(injected), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	reopened, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	task, err := reopened.Tickets().Get(ctx, "t-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	task.Title = "renamed"
	if err := reopened.Tickets().Update(ctx, task); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	text := string(out)
	if !strings.Contains(text, "origin: human") || !strings.Contains(text, "custom:\n  nested: 1") {
		t.Fatalf("unknown frontmatter lost on update:\n%s", text)
	}
	if !strings.Contains(text, "title: renamed") {
		t.Fatalf("update not written:\n%s", text)
	}
}

func TestOpenFailsClearlyOnSchemaBreak(t *testing.T) {
	dir := t.TempDir()
	tasks := filepath.Join(dir, "tasks")
	if err := os.MkdirAll(tasks, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	bad := "---\nid: t-1\nkind: task\ntitle: one\nstatus: bogus\nproject: prj-1\n---\n"
	if err := os.WriteFile(filepath.Join(tasks, "t-1.md"), []byte(bad), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	_, err := Open(context.Background(), store.Config{Backend: "jsondir", Options: map[string]string{"path": dir}})
	if err == nil {
		t.Fatal("Open() error = nil, want a schema error")
	}
	if !strings.Contains(err.Error(), "t-1.md") {
		t.Fatalf("Open() error = %v, want it to name the offending file", err)
	}
}

func TestDuplicateIDsFailClearly(t *testing.T) {
	dir := t.TempDir()
	tasks := filepath.Join(dir, "tasks")
	if err := os.MkdirAll(tasks, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	doc := "---\nid: t-1\nkind: task\ntitle: one\nstatus: todo\nproject: prj-1\n---\n"
	for _, name := range []string{"a.md", "b.md"} {
		if err := os.WriteFile(filepath.Join(tasks, name), []byte(doc), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}
	if _, err := Open(context.Background(), store.Config{Backend: "jsondir", Options: map[string]string{"path": dir}}); err == nil {
		t.Fatal("Open() error = nil, want a duplicate-id error")
	}
}

func TestUpdateExpectedConflict(t *testing.T) {
	backend := newBackend(t)
	ctx := context.Background()
	task := &core.Ticket{ID: "t-1", ProjectID: "prj-1", Kind: core.KindTask, Title: "one", Status: core.StatusTodo, UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := backend.Tickets().Create(ctx, task); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	stale := task.UpdatedAt
	task.Title = "two"
	task.UpdatedAt = stale.Add(time.Minute)
	if err := backend.Tickets().UpdateExpected(ctx, task, stale); err != nil {
		t.Fatalf("UpdateExpected(current) error = %v", err)
	}
	task.Title = "three"
	if err := backend.Tickets().UpdateExpected(ctx, task, stale); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("UpdateExpected(stale) error = %v, want ErrConflict", err)
	}
}

func TestEventsKeepInsertionOrderAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	cfg := store.Config{Backend: "jsondir", Options: map[string]string{"path": dir}}

	backend, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, id := range []core.EventID{"ev-1", "ev-2", "ev-3"} {
		if err := backend.Events().Append(ctx, &core.Event{ID: id, ProjectID: "prj-1", Kind: core.EventTaskCreated, CreatedAt: base.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatalf("Append(%s) error = %v", id, err)
		}
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	events, err := reopened.Events().List(ctx, store.EventFilter{ProjectID: "prj-1"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(events) != 3 || events[0].ID != "ev-3" || events[1].ID != "ev-2" || events[2].ID != "ev-1" {
		t.Fatalf("List() = %v, want newest-first [ev-3 ev-2 ev-1]", eventIDs(events))
	}
}

func eventIDs(events []*core.Event) []core.EventID {
	out := make([]core.EventID, 0, len(events))
	for _, event := range events {
		out = append(out, event.ID)
	}
	return out
}
