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
	"github.com/khoinguyen/factotum/pkg/store/jsonfile"
)

// seedJSONFile writes a legacy single-document jsonfile at path and runs seed
// against it, so tests exercise a document the real backend produced.
func seedJSONFile(t *testing.T, path string, seed func(be store.Backend)) {
	t.Helper()
	be, err := jsonfile.Open(context.Background(), store.Config{Backend: "jsonfile", Options: map[string]string{"path": path}})
	if err != nil {
		t.Fatalf("open jsonfile fixture: %v", err)
	}
	if seed != nil {
		seed(be)
	}
	if err := be.Close(); err != nil {
		t.Fatalf("close jsonfile fixture: %v", err)
	}
}

func seedLegacy(t *testing.T) func(be store.Backend) {
	t.Helper()
	return func(be store.Backend) {
		ctx := context.Background()
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatalf("seed: %v", err)
			}
		}
		must(be.Projects().Create(ctx, &core.Project{ID: "legacy-prj", Name: "Legacy", Policy: core.DefaultResolutionPolicy()}))
		must(be.Actors().Create(ctx, &core.Actor{ID: "legacy-act", Kind: core.ActorHuman, Name: "Khoi", Active: true}))
		must(be.Tasks().Create(ctx, &core.Task{
			ID: "legacy-t", ProjectID: "legacy-prj", Kind: core.KindTask, Title: "Old task",
			Status: core.StatusTodo, Description: "old body", Labels: []string{"legacy"},
		}))
		must(be.Artifacts().Create(ctx, &core.Artifact{
			ID: "legacy-art", ProjectID: "legacy-prj", Kind: core.ArtifactSpec, Title: "Old spec", Body: "spec body",
		}))
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		for i, id := range []core.EventID{"legacy-ev-1", "legacy-ev-2"} {
			must(be.Events().Append(ctx, &core.Event{
				ID: id, ProjectID: "legacy-prj", Kind: core.EventTaskCreated, Summary: "seeded",
				CreatedAt: base.Add(time.Duration(i) * time.Second),
			}))
		}
	}
}

func openJSONDir(t *testing.T, root string) *Backend {
	t.Helper()
	backend, err := Open(context.Background(), store.Config{Backend: "jsondir", Options: map[string]string{"path": root}})
	if err != nil {
		t.Fatalf("open jsondir: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	return backend.(*Backend)
}

func TestMigrateFromJSONFileRoundTrips(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "legacy.json")
	root := filepath.Join(dir, "jsondir")
	seedJSONFile(t, source, seedLegacy(t))

	if _, err := MigrateFromJSONFile(ctx, source, root); err != nil {
		t.Fatalf("MigrateFromJSONFile() error = %v", err)
	}

	backend := openJSONDir(t, root)
	project, err := backend.Projects().Get(ctx, "legacy-prj")
	if err != nil || project.Name != "Legacy" {
		t.Fatalf("project = %#v, err = %v, want Legacy", project, err)
	}
	task, err := backend.Tasks().Get(ctx, "legacy-t")
	if err != nil {
		t.Fatalf("task Get() error = %v", err)
	}
	if task.Title != "Old task" || task.Description != "old body" || len(task.Labels) != 1 || task.Labels[0] != "legacy" {
		t.Fatalf("task = %#v, want the migrated fields", task)
	}
	if _, err := backend.Actors().Get(ctx, "legacy-act"); err != nil {
		t.Fatalf("actor Get() error = %v", err)
	}
	artifact, err := backend.Artifacts().Get(ctx, "legacy-art")
	if err != nil || artifact.Body != "spec body" {
		t.Fatalf("artifact = %#v, err = %v", artifact, err)
	}
	events, err := backend.Events().List(ctx, store.EventFilter{ProjectID: "legacy-prj"})
	if err != nil {
		t.Fatalf("events List() error = %v", err)
	}
	if len(events) != 2 || events[0].ID != "legacy-ev-2" || events[1].ID != "legacy-ev-1" {
		t.Fatalf("events = %v, want newest-first [legacy-ev-2 legacy-ev-1]", eventIDs(events))
	}

	for _, rel := range []string{"projects/legacy-prj.md", "tasks/legacy-t.md", "actors/legacy-act.md", "artifacts/legacy-art.md"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("expected migrated file %s: %v", rel, err)
		}
	}
}

func TestMigrateFromJSONFileBacksUpOriginal(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "legacy.json")
	root := filepath.Join(dir, "jsondir")
	seedJSONFile(t, source, seedLegacy(t))
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	backup, err := MigrateFromJSONFile(ctx, source, root)
	if err != nil {
		t.Fatalf("MigrateFromJSONFile() error = %v", err)
	}
	if backup == "" || !strings.HasPrefix(backup, source+".bak-") {
		t.Fatalf("backup path = %q, want %s.bak-<timestamp>", backup, source)
	}
	if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source still present after migration: %v", err)
	}
	got, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("backup bytes differ from original")
	}
}

func TestMigrateFromJSONFileRefusesExistingTarget(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "legacy.json")
	root := filepath.Join(dir, "jsondir")
	seedJSONFile(t, source, seedLegacy(t))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "keep.md"), []byte("mine"), 0o644); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	_, err := MigrateFromJSONFile(ctx, source, root)
	if !errors.Is(err, core.ErrAlreadyExists) {
		t.Fatalf("MigrateFromJSONFile() error = %v, want ErrAlreadyExists", err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source was touched on refusal: %v", err)
	}
}

func TestMigrateFromJSONFileIdempotent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "legacy.json")
	root := filepath.Join(dir, "jsondir")
	seedJSONFile(t, source, seedLegacy(t))

	if _, err := MigrateFromJSONFile(ctx, source, root); err != nil {
		t.Fatalf("first migrate error = %v", err)
	}
	backup, err := MigrateFromJSONFile(ctx, source, root)
	if err != nil {
		t.Fatalf("second migrate error = %v, want a no-op", err)
	}
	if backup != "" {
		t.Fatalf("second migrate backup = %q, want empty", backup)
	}
	backend := openJSONDir(t, root)
	if _, err := backend.Tasks().Get(ctx, "legacy-t"); err != nil {
		t.Fatalf("data lost after second migrate: %v", err)
	}
}

func TestMigrateFromJSONFileMissingSource(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	_, err := MigrateFromJSONFile(ctx, filepath.Join(dir, "absent.json"), filepath.Join(dir, "jsondir"))
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("MigrateFromJSONFile() error = %v, want ErrNotFound", err)
	}
}

func TestMigrateEmptyDocumentIsIdempotent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "legacy.json")
	root := filepath.Join(dir, "jsondir")
	if err := os.WriteFile(source, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write empty fixture: %v", err)
	}

	if _, err := MigrateFromJSONFile(ctx, source, root); err != nil {
		t.Fatalf("first migrate error = %v", err)
	}
	backup, err := MigrateFromJSONFile(ctx, source, root)
	if err != nil {
		t.Fatalf("second migrate error = %v, want a no-op", err)
	}
	if backup != "" {
		t.Fatalf("second migrate backup = %q, want empty", backup)
	}
}

func TestOpenMigratesEmptyDocumentTwice(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "legacy.json")
	root := filepath.Join(dir, "jsondir")
	if err := os.WriteFile(source, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write empty fixture: %v", err)
	}
	cfg := store.Config{Backend: "jsondir", Options: map[string]string{"path": root, "migrate_from": source}}

	first, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}

	second, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("second Open() error = %v, want a no-op", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestMigrateIsNoOpAfterTargetRemoved(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "legacy.json")
	root := filepath.Join(dir, "jsondir")
	seedJSONFile(t, source, seedLegacy(t))

	if _, err := MigrateFromJSONFile(ctx, source, root); err != nil {
		t.Fatalf("first migrate error = %v", err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove target: %v", err)
	}
	backup, err := MigrateFromJSONFile(ctx, source, root)
	if err != nil {
		t.Fatalf("migrate after target removed error = %v, want a no-op", err)
	}
	if backup != "" {
		t.Fatalf("migrate after target removed backup = %q, want empty", backup)
	}
}

func TestMigratedFixturePassesConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) store.Backend {
		dir := t.TempDir()
		source := filepath.Join(dir, "legacy.json")
		root := filepath.Join(dir, "jsondir")
		if err := os.WriteFile(source, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write empty fixture: %v", err)
		}
		if _, err := MigrateFromJSONFile(context.Background(), source, root); err != nil {
			t.Fatalf("MigrateFromJSONFile() error = %v", err)
		}
		backend, err := Open(context.Background(), store.Config{Backend: "jsondir", Options: map[string]string{"path": root}})
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		t.Cleanup(func() { _ = backend.Close() })
		return backend
	})
}

func TestOpenAutoMigratesFromOption(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "legacy.json")
	root := filepath.Join(dir, "jsondir")
	seedJSONFile(t, source, seedLegacy(t))

	var notices []string
	backend, err := Open(ctx, store.Config{
		Backend: "jsondir",
		Options: map[string]string{"path": root, "migrate_from": source},
		Noticef: func(format string, args ...any) { notices = append(notices, format) },
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if _, err := backend.Tasks().Get(ctx, "legacy-t"); err != nil {
		t.Fatalf("migrated task not loaded: %v", err)
	}
	if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source not backed up on open: %v", err)
	}
	if len(notices) == 0 {
		t.Fatal("Open() emitted no migration notice")
	}
}
