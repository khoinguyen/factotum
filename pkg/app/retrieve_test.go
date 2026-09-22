package app

import (
	"context"
	"errors"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/embed"
	"github.com/khoinguyen/factotum/pkg/vector"
)

type stubEmbedder struct {
	vectors  [][]float32
	err      error
	lastIn   embed.Input
	lastText []string
}

func (s *stubEmbedder) Embed(_ context.Context, input embed.Input, texts []string) ([][]float32, error) {
	s.lastIn = input
	s.lastText = texts
	if s.err != nil {
		return nil, s.err
	}
	return s.vectors, nil
}

func memoryArtifact(id, title, brief string) *core.Artifact {
	return &core.Artifact{ID: core.ArtifactID(id), Kind: core.ArtifactMemory, Title: title, Brief: brief}
}

func TestRRFMergesAndRanksByReciprocalRank(t *testing.T) {
	a := memoryArtifact("a", "A", "")
	b := memoryArtifact("b", "B", "")
	c := memoryArtifact("c", "C", "")

	// b tops lexical, a tops vector; both get two contributions, a slightly higher.
	merged := RRF([]*core.Artifact{b, a}, []*core.Artifact{a, c})
	got := artifactIDs(merged)
	if len(got) != 3 {
		t.Fatalf("RRF() = %v, want the union of both lists", got)
	}
	if got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("RRF() order = %v, want a, b, c", got)
	}
}

func TestRRFIsDeterministicOnTies(t *testing.T) {
	a := memoryArtifact("a", "A", "")
	b := memoryArtifact("b", "B", "")
	first := artifactIDs(RRF([]*core.Artifact{a, b}, []*core.Artifact{b, a}))
	second := artifactIDs(RRF([]*core.Artifact{a, b}, []*core.Artifact{b, a}))
	if len(first) != 2 || first[0] != "a" || first[1] != "b" {
		t.Fatalf("RRF() tie order = %v, want id ascending", first)
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("RRF() not deterministic: %v vs %v", first, second)
		}
	}
}

func TestVectorRetrieverDisabledWithoutEmbedderOrIndex(t *testing.T) {
	index := vector.NewMemory()
	if NewVectorRetriever(nil, index, "m").Enabled() {
		t.Fatal("nil embedder should be disabled")
	}
	if NewVectorRetriever(&stubEmbedder{}, nil, "m").Enabled() {
		t.Fatal("nil index should be disabled")
	}
	if NewVectorRetriever(&stubEmbedder{}, index, "").Enabled() {
		t.Fatal("empty model should be disabled")
	}
	if NewVectorRetriever(embed.Disabled{}, index, "m").Enabled() {
		t.Fatal("Disabled embedder should be disabled")
	}
	if !NewVectorRetriever(&stubEmbedder{}, index, "m").Enabled() {
		t.Fatal("configured retriever should be enabled")
	}
}

func TestVectorRetrieverRetrieveOrdersByVector(t *testing.T) {
	ctx := context.Background()
	index := vector.NewMemory()
	_ = index.Put(ctx, "near", "m", []float32{1, 0})
	_ = index.Put(ctx, "far", "m", []float32{0.1, 1})
	near := memoryArtifact("near", "Near", "")
	far := memoryArtifact("far", "Far", "")
	universe := []*core.Artifact{far, near}

	embedder := &stubEmbedder{vectors: [][]float32{{1, 0}}}
	got, err := NewVectorRetriever(embedder, index, "m").Retrieve(ctx, "query", universe, 10)
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(got) != 2 || got[0].ID != "near" || got[1].ID != "far" {
		t.Fatalf("Retrieve() = %v, want near then far", ids(got))
	}
	if embedder.lastIn != embed.InputQuery {
		t.Fatalf("Retrieve() embed input = %q, want query", embedder.lastIn)
	}
}

func TestVectorRetrieverRejectsModelMismatch(t *testing.T) {
	ctx := context.Background()
	index := vector.NewMemory()
	_ = index.Put(ctx, "x", "old-model", []float32{1, 0})

	retriever := NewVectorRetriever(&stubEmbedder{vectors: [][]float32{{1, 0}}}, index, "new-model")
	_, err := retriever.Retrieve(ctx, "query", []*core.Artifact{memoryArtifact("x", "X", "")}, 10)
	if !errors.Is(err, ErrModelMismatch) {
		t.Fatalf("Retrieve() error = %v, want ErrModelMismatch", err)
	}
}

func TestVectorRetrieverUpsertEmbedsDocumentText(t *testing.T) {
	ctx := context.Background()
	index := vector.NewMemory()
	embedder := &stubEmbedder{vectors: [][]float32{{1, 0}}}
	retriever := NewVectorRetriever(embedder, index, "m")

	if err := retriever.Upsert(ctx, memoryArtifact("a", "Title", "the brief")); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if embedder.lastIn != embed.InputDocument {
		t.Fatalf("Upsert() embed input = %q, want document", embedder.lastIn)
	}
	if len(embedder.lastText) != 1 || embedder.lastText[0] != "Title\nthe brief" {
		t.Fatalf("Upsert() text = %q", embedder.lastText)
	}
	hits, _ := index.Search(ctx, "m", []float32{1, 0}, 10)
	if len(hits) != 1 || hits[0].ID != "a" {
		t.Fatalf("Upsert() did not store the vector: %+v", hits)
	}
}

func TestVectorRetrieverReindexEmbedsAll(t *testing.T) {
	ctx := context.Background()
	index := vector.NewMemory()
	embedder := &stubEmbedder{vectors: [][]float32{{1, 0}}}
	retriever := NewVectorRetriever(embedder, index, "m")

	count, err := retriever.Reindex(ctx, []*core.Artifact{memoryArtifact("a", "A", ""), memoryArtifact("b", "B", "")})
	if err != nil {
		t.Fatalf("Reindex() error = %v", err)
	}
	if count != 2 {
		t.Fatalf("Reindex() count = %d, want 2", count)
	}
	hits, _ := index.Search(ctx, "m", []float32{1, 0}, 10)
	if len(hits) != 2 {
		t.Fatalf("Reindex() stored %d vectors, want 2", len(hits))
	}
}

func artifactIDs(artifacts []*core.Artifact) []string {
	out := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		out = append(out, string(artifact.ID))
	}
	return out
}
