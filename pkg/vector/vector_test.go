package vector_test

import (
	"context"
	"math"
	"testing"

	"github.com/khoinguyen/factotum/pkg/vector"
)

func TestCosine(t *testing.T) {
	tests := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"identical", []float32{1, 2, 3}, []float32{1, 2, 3}, 1},
		{"opposite", []float32{1, 0}, []float32{-1, 0}, -1},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0},
		{"zero vector", []float32{0, 0}, []float32{1, 1}, 0},
		{"length mismatch", []float32{1}, []float32{1, 2}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := vector.Cosine(tt.a, tt.b); math.Abs(got-tt.want) > 1e-6 {
				t.Fatalf("Cosine(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestMemorySearchOrdersBySimilarity(t *testing.T) {
	ctx := context.Background()
	index := vector.NewMemory()
	if err := index.Put(ctx, "far", "m", []float32{0.1, 1}); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if err := index.Put(ctx, "near", "m", []float32{1, 0.1}); err != nil {
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

func TestMemorySearchFiltersByModel(t *testing.T) {
	ctx := context.Background()
	index := vector.NewMemory()
	_ = index.Put(ctx, "old", "model-a", []float32{1, 0})
	_ = index.Put(ctx, "new", "model-b", []float32{1, 0})

	hits, err := index.Search(ctx, "model-b", []float32{1, 0}, 10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "new" {
		t.Fatalf("Search() = %+v, want only the requested model", hits)
	}
}

func TestMemoryModels(t *testing.T) {
	ctx := context.Background()
	index := vector.NewMemory()
	_ = index.Put(ctx, "a", "model-a", []float32{1, 0})
	_ = index.Put(ctx, "b", "model-b", []float32{1, 0})
	_ = index.Put(ctx, "c", "model-a", []float32{0, 1})

	models, err := index.Models(ctx)
	if err != nil {
		t.Fatalf("Models() error = %v", err)
	}
	if len(models) != 2 || models[0] != "model-a" || models[1] != "model-b" {
		t.Fatalf("Models() = %v, want sorted model-a, model-b", models)
	}
}

func TestMemoryPutReplacesAndDeleteRemoves(t *testing.T) {
	ctx := context.Background()
	index := vector.NewMemory()
	_ = index.Put(ctx, "x", "m", []float32{1, 0})
	_ = index.Put(ctx, "x", "m", []float32{0, 1})

	hits, _ := index.Search(ctx, "m", []float32{0, 1}, 10)
	if len(hits) != 1 {
		t.Fatalf("Put() should replace, got %d hits", len(hits))
	}

	if err := index.Delete(ctx, "x"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	hits, _ = index.Search(ctx, "m", []float32{0, 1}, 10)
	if len(hits) != 0 {
		t.Fatalf("Delete() left %d hits", len(hits))
	}
}

func TestMemorySearchHonorsLimit(t *testing.T) {
	ctx := context.Background()
	index := vector.NewMemory()
	for _, id := range []string{"a", "b", "c"} {
		_ = index.Put(ctx, id, "m", []float32{1, 0})
	}
	hits, err := index.Search(ctx, "m", []float32{1, 0}, 2)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("Search() len = %d, want limit 2", len(hits))
	}
}

func TestPutRejectsEmptyVector(t *testing.T) {
	if err := vector.NewMemory().Put(context.Background(), "x", "m", nil); err == nil {
		t.Fatal("Put() with an empty vector should error")
	}
}
