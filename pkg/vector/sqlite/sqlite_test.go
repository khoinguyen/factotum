package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/khoinguyen/factotum/pkg/vector"
	vsqlite "github.com/khoinguyen/factotum/pkg/vector/sqlite"
)

func openIndex(t *testing.T, path string) *vsqlite.Index {
	t.Helper()
	index, err := vsqlite.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = index.Close() })
	return index
}

func TestSQLiteRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vectors.db")
	index := openIndex(t, path)

	if err := index.Put(ctx, "near", "m", []float32{1, 0.1}); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if err := index.Put(ctx, "far", "m", []float32{0.1, 1}); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	hits, err := index.Search(ctx, "m", []float32{1, 0}, 10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) != 2 || hits[0].ID != "near" || hits[1].ID != "far" {
		t.Fatalf("Search() = %+v, want near then far", hits)
	}
}

func TestSQLitePersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vectors.db")

	index := openIndex(t, path)
	if err := index.Put(ctx, "kept", "m", []float32{1, 0}); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if err := index.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened := openIndex(t, path)
	hits, err := reopened.Search(ctx, "m", []float32{1, 0}, 10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "kept" {
		t.Fatalf("reopened Search() = %+v, want kept", hits)
	}
}

func TestSQLiteModelFilterAndReplace(t *testing.T) {
	ctx := context.Background()
	index := openIndex(t, filepath.Join(t.TempDir(), "vectors.db"))

	_ = index.Put(ctx, "x", "model-a", []float32{1, 0})
	_ = index.Put(ctx, "x", "model-b", []float32{0, 1})

	hits, _ := index.Search(ctx, "model-b", []float32{0, 1}, 10)
	if len(hits) != 1 || hits[0].ID != "x" {
		t.Fatalf("Put() should replace by id, got %+v", hits)
	}
	if stale, _ := index.Search(ctx, "model-a", []float32{1, 0}, 10); len(stale) != 0 {
		t.Fatalf("replaced vector still searchable under its old model: %+v", stale)
	}
	models, err := index.Models(ctx)
	if err != nil {
		t.Fatalf("Models() error = %v", err)
	}
	if len(models) != 1 || models[0] != "model-b" {
		t.Fatalf("Models() = %v, want [model-b]", models)
	}

	if err := index.Delete(ctx, "x"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	hits, _ = index.Search(ctx, "model-b", []float32{0, 1}, 10)
	if len(hits) != 0 {
		t.Fatalf("Delete() left %d hits", len(hits))
	}
}

func TestSQLiteRejectsEmptyVector(t *testing.T) {
	index := openIndex(t, filepath.Join(t.TempDir(), "vectors.db"))
	if err := index.Put(context.Background(), "x", "m", nil); err == nil {
		t.Fatal("Put() with an empty vector should error")
	}
}

var _ vector.Index = (*vsqlite.Index)(nil)
