// Package vector is the port for the vector side index and a pure-Go cosine
// implementation. Vectors live outside the storage backends and outside pkg/core:
// they are a side index keyed by item id and embedding model. At this scale the
// scan happens in Go; no ANN library is needed.
package vector

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
)

// Hit is one ranked vector search result.
type Hit struct {
	ID    string
	Score float64
}

// Index stores one vector per item and searches by cosine similarity. Vectors from
// different embedding models are not comparable, so every operation is scoped by
// model and Search only returns vectors embedded with the requested model.
type Index interface {
	Put(ctx context.Context, id, model string, vec []float32) error
	Delete(ctx context.Context, id string) error
	Search(ctx context.Context, model string, query []float32, limit int) ([]Hit, error)
	// Models returns the distinct embedding models present, sorted.
	Models(ctx context.Context) ([]string, error)
}

// Cosine returns the cosine similarity of a and b. Mismatched lengths or a zero
// vector yield 0, so a degenerate vector never looks similar to anything.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

type memoryItem struct {
	model string
	vec   []float32
}

// Memory is an in-process Index. It is the side index for backends without a
// persistent side index, and the fake used in tests.
type Memory struct {
	mu    sync.Mutex
	items map[string]memoryItem
}

func NewMemory() *Memory {
	return &Memory{items: map[string]memoryItem{}}
}

func (m *Memory) Put(_ context.Context, id, model string, vec []float32) error {
	if id == "" {
		return fmt.Errorf("vector: empty id")
	}
	if len(vec) == 0 {
		return fmt.Errorf("vector: empty vector for %s", id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[id] = memoryItem{model: model, vec: append([]float32(nil), vec...)}
	return nil
}

func (m *Memory) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

func (m *Memory) Search(_ context.Context, model string, query []float32, limit int) ([]Hit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	hits := make([]Hit, 0, len(m.items))
	for id, item := range m.items {
		if item.model != model {
			continue
		}
		hits = append(hits, Hit{ID: id, Score: Cosine(query, item.vec)})
	}
	return Rank(hits, limit), nil
}

func (m *Memory) Models(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	for _, item := range m.items {
		seen[item.model] = true
	}
	return sortedKeys(seen), nil
}

// Rank sorts hits by score descending, then id ascending, and applies limit when
// positive. Zero and negative scores are dropped: an unrelated vector should not
// add a candidate.
func Rank(hits []Hit, limit int) []Hit {
	filtered := hits[:0]
	for _, hit := range hits {
		if hit.Score > 0 {
			filtered = append(filtered, hit)
		}
	}
	hits = filtered
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ID < hits[j].ID
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

func sortedKeys(seen map[string]bool) []string {
	out := make([]string, 0, len(seen))
	for key := range seen {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
