package perf

import (
	"context"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/memory"
)

func TestSpecForTiersArtifactsAndEvents(t *testing.T) {
	tests := []struct {
		tasks int
		want  Spec
	}{
		{1000, Spec{Tasks: 1000, Artifacts: 1000, Events: 1000}},
		{5000, Spec{Tasks: 5000, Artifacts: 1000, Events: 1000}},
		{50000, Spec{Tasks: 50000, Artifacts: 10000, Events: 10000}},
	}
	for _, tt := range tests {
		if got := SpecFor(tt.tasks); got != tt.want {
			t.Fatalf("SpecFor(%d) = %+v, want %+v", tt.tasks, got, tt.want)
		}
	}
}

func TestSeedCapCapsJSONFile(t *testing.T) {
	if got := SeedCap("jsonfile"); got != JSONFileSeedCap {
		t.Fatalf("SeedCap(jsonfile) = %d, want %d", got, JSONFileSeedCap)
	}
	if got := SeedCap("sqlite"); got != 0 {
		t.Fatalf("SeedCap(sqlite) = %d, want 0 (unlimited)", got)
	}
	if got := SeedCap("memory"); got != 0 {
		t.Fatalf("SeedCap(memory) = %d, want 0 (unlimited)", got)
	}
}

func TestSeedPopulatesDataset(t *testing.T) {
	ctx := context.Background()
	be := memory.New()
	defer func() { _ = be.Close() }()

	projectID, err := Seed(ctx, be, Spec{Tasks: 100, Artifacts: 20, Events: 30})
	if err != nil {
		t.Fatalf("Seed() error = %v", err)
	}
	if projectID != DefaultProjectID {
		t.Fatalf("project = %q, want %q", projectID, DefaultProjectID)
	}

	tasks, err := be.Tasks().List(ctx, store.TaskFilter{ProjectID: projectID})
	if err != nil {
		t.Fatalf("Tasks().List() error = %v", err)
	}
	if len(tasks) != 100 {
		t.Fatalf("tasks = %d, want 100", len(tasks))
	}
	artifacts, err := be.Artifacts().List(ctx, store.ArtifactFilter{ProjectID: projectID})
	if err != nil {
		t.Fatalf("Artifacts().List() error = %v", err)
	}
	if len(artifacts) != 20 {
		t.Fatalf("artifacts = %d, want 20", len(artifacts))
	}
	events, err := be.Events().List(ctx, store.EventFilter{ProjectID: projectID})
	if err != nil {
		t.Fatalf("Events().List() error = %v", err)
	}
	if len(events) != 30 {
		t.Fatalf("events = %d, want 30", len(events))
	}
}

func TestSeedLinksEveryTenthTaskToItsPredecessor(t *testing.T) {
	ctx := context.Background()
	be := memory.New()
	defer func() { _ = be.Close() }()
	projectID, err := Seed(ctx, be, Spec{Tasks: 25})
	if err != nil {
		t.Fatalf("Seed() error = %v", err)
	}

	dep, err := be.Tasks().Get(ctx, core.TaskID("t-000010"))
	if err != nil {
		t.Fatalf("Get(t-000010) error = %v", err)
	}
	if len(dep.Deps) != 1 || dep.Deps[0] != core.TaskID("t-000009") {
		t.Fatalf("t-000010 deps = %v, want [t-000009]", dep.Deps)
	}

	first, err := be.Tasks().Get(ctx, core.TaskID("t-000000"))
	if err != nil {
		t.Fatalf("Get(t-000000) error = %v", err)
	}
	if len(first.Deps) != 0 {
		t.Fatalf("t-000000 deps = %v, want none", first.Deps)
	}
	if first.ProjectID != projectID {
		t.Fatalf("task project = %q, want %q", first.ProjectID, projectID)
	}
}

func TestSeedSearchFindsSyntheticBody(t *testing.T) {
	ctx := context.Background()
	be := memory.New()
	defer func() { _ = be.Close() }()
	projectID, err := Seed(ctx, be, Spec{Tasks: 50})
	if err != nil {
		t.Fatalf("Seed() error = %v", err)
	}
	hits, err := be.Tasks().Search(ctx, store.TaskFilter{ProjectID: projectID}, "synthetic")
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) != 50 {
		t.Fatalf("search hits = %d, want 50", len(hits))
	}
}
