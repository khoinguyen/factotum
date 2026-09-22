package app

import (
	"context"
	"fmt"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/judge"
)

// Thresholds for routing a related memory. The rating levels are the three
// actions, so the cut points follow from the wording, not fitted data.
const (
	memoryRelatedThreshold    = 0.5
	memorySupersedesThreshold = 1.5
	memoryMinConfidence       = 0.4
)

// MemoryRelation is the advised relationship between a freshly written memory
// and an existing one. Action is "unrelated", "related", or "supersedes".
type MemoryRelation struct {
	Action     string
	Candidate  *core.Artifact
	Rating     float64
	Confidence float64
}

// MemoryRelationService advises whether a written memory is related to or
// supersedes an existing one, so the author can reconcile them. It never
// mutates anything: the agent decides to update or drop the other entry.
type MemoryRelationService struct {
	judge judge.Judge
}

func NewMemoryRelationService(j judge.Judge) *MemoryRelationService {
	return &MemoryRelationService{judge: j}
}

// Check compares artifact against candidates retrieved in code, in one request.
func (s *MemoryRelationService) Check(ctx context.Context, artifact *core.Artifact, candidates []*core.Artifact) (MemoryRelation, error) {
	if len(candidates) == 0 {
		return MemoryRelation{Action: "unrelated"}, nil
	}
	if s.judge == nil {
		return MemoryRelation{}, judge.ErrUnavailable
	}
	entries := make([]map[string]string, 0, len(candidates))
	for _, candidate := range candidates {
		entries = append(entries, map[string]string{
			"id": string(candidate.ID), "title": candidate.Title, "brief": candidate.Brief, "body": candidate.Body,
		})
	}
	state := map[string]any{
		"memory": map[string]string{
			"id": string(artifact.ID), "title": artifact.Title, "brief": artifact.Brief, "body": artifact.Body,
		},
		"candidates": entries,
	}
	questions := make(map[string]judge.Question, len(candidates))
	for i, candidate := range candidates {
		questions["rel::"+string(candidate.ID)] = judge.Question{
			Kind: judge.KindRating,
			Instructions: fmt.Sprintf(
				"How do the new `memory` and `candidates[%d]` relate?", i),
			Criteria: []string{
				"They cover different things; keep both.",
				"They are closely related; reconcile them so the corpus does not repeat itself.",
				"The new memory supersedes the candidate.",
			},
		}
	}
	response, err := s.judge.Ask(ctx, judge.Request{State: state, Questions: questions})
	if err != nil {
		return MemoryRelation{}, err
	}

	best := MemoryRelation{Action: "unrelated"}
	for _, candidate := range candidates {
		score := response.Answers["rel::"+string(candidate.ID)]
		if best.Candidate == nil || score.Rating > best.Rating {
			best = MemoryRelation{Candidate: candidate, Rating: score.Rating, Confidence: score.Confidence}
		}
	}
	if best.Confidence < memoryMinConfidence || best.Rating < memoryRelatedThreshold {
		return MemoryRelation{Action: "unrelated"}, nil
	}
	if best.Rating >= memorySupersedesThreshold {
		best.Action = "supersedes"
	} else {
		best.Action = "related"
	}
	return best, nil
}
