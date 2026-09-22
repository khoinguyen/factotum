package app

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/embed"
	"github.com/khoinguyen/factotum/pkg/vector"
)

// ErrModelMismatch means the vector index holds vectors from a different embedding
// model than the one configured. Vectors from different models are not comparable,
// so the caller must reindex (or leave vector retrieval off) rather than mix them.
var ErrModelMismatch = errors.New("vector index model mismatch")

// rrfK is the reciprocal rank fusion constant. It damps the influence of the top
// ranks so a single list cannot dominate; 60 is the value from the original RRF
// paper and a common default.
const rrfK = 60

// RRF merges ranked lists by reciprocal rank. Lexical (bm25/scan) and vector
// cosine scores live on different scales, so only the ranks are combined: each
// item scores the sum of 1/(k+rank) across the lists that contain it. The union is
// returned, so vector hits the lexical search missed are added. Ties break by id.
func RRF(lists ...[]*core.Artifact) []*core.Artifact {
	scores := map[core.ArtifactID]float64{}
	artifacts := map[core.ArtifactID]*core.Artifact{}
	order := make([]core.ArtifactID, 0)
	for _, list := range lists {
		for i, artifact := range list {
			if artifact == nil {
				continue
			}
			if _, seen := artifacts[artifact.ID]; !seen {
				order = append(order, artifact.ID)
			}
			artifacts[artifact.ID] = artifact
			scores[artifact.ID] += 1.0 / float64(rrfK+i+1)
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if scores[order[i]] != scores[order[j]] {
			return scores[order[i]] > scores[order[j]]
		}
		return order[i] < order[j]
	})
	out := make([]*core.Artifact, 0, len(order))
	for _, id := range order {
		out = append(out, artifacts[id])
	}
	return out
}

// VectorRetriever adds semantic candidates to a lexical result. It is a retriever,
// not a ranker: it embeds the query, searches the side index, and returns artifacts
// in vector order for the caller to fuse (with RRF) and rerank. It owns the write
// path too: Upsert embeds and stores one memory, Reindex re-embeds all of them.
type VectorRetriever struct {
	embedder embed.Embedder
	index    vector.Index
	model    string
}

func NewVectorRetriever(embedder embed.Embedder, index vector.Index, model string) *VectorRetriever {
	return &VectorRetriever{embedder: embedder, index: index, model: model}
}

// Enabled reports whether both the embedder and the side index are configured. A
// Disabled embedder (no provider) keeps every caller on the lexical path.
func (s *VectorRetriever) Enabled() bool {
	if s == nil || s.embedder == nil || s.index == nil || s.model == "" {
		return false
	}
	_, disabled := s.embedder.(embed.Disabled)
	return !disabled
}

// Model returns the configured embedding model.
func (s *VectorRetriever) Model() string {
	if s == nil {
		return ""
	}
	return s.model
}

// Retrieve returns the artifacts in universe most similar to query, in vector
// order. A model mismatch is reported rather than silently mixed.
func (s *VectorRetriever) Retrieve(ctx context.Context, query string, universe []*core.Artifact, limit int) ([]*core.Artifact, error) {
	if !s.Enabled() {
		return nil, embed.ErrUnavailable
	}
	if err := s.checkModel(ctx, universe); err != nil {
		return nil, err
	}
	vectors, err := s.embedder.Embed(ctx, embed.InputQuery, []string{query})
	if err != nil {
		return nil, err
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return nil, fmt.Errorf("vector: embedder returned no vector for the query")
	}
	hits, err := s.index.Search(ctx, s.model, vectors[0], limit)
	if err != nil {
		return nil, err
	}
	byID := make(map[core.ArtifactID]*core.Artifact, len(universe))
	for _, artifact := range universe {
		byID[artifact.ID] = artifact
	}
	out := make([]*core.Artifact, 0, len(hits))
	for _, hit := range hits {
		if artifact, ok := byID[core.ArtifactID(hit.ID)]; ok {
			out = append(out, artifact)
		}
	}
	return out, nil
}

// Upsert embeds one artifact's document text and stores its vector, replacing any
// previous vector for the same id.
func (s *VectorRetriever) Upsert(ctx context.Context, artifact *core.Artifact) error {
	if !s.Enabled() {
		return embed.ErrUnavailable
	}
	text := embed.DocumentText(artifact.Title, artifact.Brief, artifact.Body)
	vectors, err := s.embedder.Embed(ctx, embed.InputDocument, []string{text})
	if err != nil {
		return err
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return fmt.Errorf("vector: embedder returned no vector for %s", artifact.ID)
	}
	return s.index.Put(ctx, string(artifact.ID), s.model, vectors[0])
}

// Delete removes an artifact's vector, so a deleted memory leaves no orphan that a
// later model change would misread as a mismatch. A missing vector is not an error.
func (s *VectorRetriever) Delete(ctx context.Context, id core.ArtifactID) error {
	if s == nil || s.index == nil {
		return nil
	}
	return s.index.Delete(ctx, string(id))
}

// Reindex re-embeds every artifact, replacing the index contents for the configured
// model. It is the backfill for vectors skipped while the embedder was down, and the
// repair for a model change. It returns how many were stored before the first error.
func (s *VectorRetriever) Reindex(ctx context.Context, artifacts []*core.Artifact) (int, error) {
	if !s.Enabled() {
		return 0, embed.ErrUnavailable
	}
	count := 0
	for _, artifact := range artifacts {
		if err := s.Upsert(ctx, artifact); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// checkModel compares the configured model against the models of the vectors for
// the artifacts in scope, not the whole side index: the index is shared by every
// project on the store, but a mismatch is only meaningful for the project being
// searched (and repaired by reindexing that project).
func (s *VectorRetriever) checkModel(ctx context.Context, universe []*core.Artifact) error {
	ids := make([]string, 0, len(universe))
	for _, artifact := range universe {
		if artifact != nil {
			ids = append(ids, string(artifact.ID))
		}
	}
	models, err := s.index.Models(ctx, ids)
	if err != nil {
		return err
	}
	if len(models) == 0 {
		return nil
	}
	if len(models) == 1 && models[0] == s.model {
		return nil
	}
	return fmt.Errorf("%w: vectors use %v, configured %s", ErrModelMismatch, models, s.model)
}
