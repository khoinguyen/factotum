package app

import (
	"context"
	"sort"
	"strings"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/judge"
)

const (
	// rerankMinConfidence is the floor for trusting the rerank order; below it the
	// caller's lexical order is kept.
	rerankMinConfidence = 0.5
	// rerankExistsFloor is the floor for "some candidate answers the query". Below it
	// the result is empty, so a search can report no match instead of the nearest line.
	rerankExistsFloor = 0.5
	// rerankShortlistLimit bounds the two-stage flow: fast search ranks the corpus, the
	// top N become the shortlist, and only that shortlist is reranked. It keeps the
	// Choice well under its option limit and the request cost bounded.
	rerankShortlistLimit = 25
)

// RerankService reorders a lexical shortlist by meaning. Retrieval stays in code; this
// only reorders what the caller already found, and never adds a candidate.
type RerankService struct {
	judge judge.Judge
}

func NewRerankService(j judge.Judge) *RerankService { return &RerankService{judge: j} }

// rerankCandidate is the field set the reranker needs from any hit. Artifacts and
// tasks both map onto it, so the two share one judge request and ordering.
type rerankCandidate struct {
	id    string
	title string
	brief string
	body  string
}

// Rerank orders candidates by how well they answer query. Candidates must arrive in
// fast-search order; only the top rerankShortlistLimit are reranked. It returns an
// empty slice when no candidate answers the query, the input order when the judge is
// unsure, and judge.ErrUnavailable when no judge is configured.
func (s *RerankService) Rerank(ctx context.Context, query string, candidates []*core.Artifact) ([]*core.Artifact, error) {
	inputs := make([]rerankCandidate, len(candidates))
	byID := make(map[string]*core.Artifact, len(candidates))
	for i, candidate := range candidates {
		inputs[i] = rerankCandidate{
			id:    string(candidate.ID),
			title: candidate.Title,
			brief: candidate.Brief,
			body:  candidate.Body,
		}
		byID[string(candidate.ID)] = candidate
	}
	ordered, err := s.rerank(ctx, query, inputs)
	if err != nil {
		return nil, err
	}
	out := make([]*core.Artifact, 0, len(ordered))
	for _, candidate := range ordered {
		out = append(out, byID[candidate.id])
	}
	return out, nil
}

// RerankTasks orders task candidates by how well they answer query, with the same
// two-stage contract as Rerank: it only reorders the shortlist the store returned.
func (s *RerankService) RerankTasks(ctx context.Context, query string, candidates []*core.Task) ([]*core.Task, error) {
	inputs := make([]rerankCandidate, len(candidates))
	byID := make(map[string]*core.Task, len(candidates))
	for i, candidate := range candidates {
		inputs[i] = rerankCandidate{
			id:    string(candidate.ID),
			title: candidate.Title,
			body:  candidate.Description,
		}
		byID[string(candidate.ID)] = candidate
	}
	ordered, err := s.rerank(ctx, query, inputs)
	if err != nil {
		return nil, err
	}
	out := make([]*core.Task, 0, len(ordered))
	for _, candidate := range ordered {
		out = append(out, byID[candidate.id])
	}
	return out, nil
}

// rerank runs the shared two-stage rerank over candidates and returns them in the
// judge's order. It returns an empty slice when no candidate answers the query, the
// input order when the judge is unsure, and judge.ErrUnavailable with no judge.
func (s *RerankService) rerank(ctx context.Context, query string, candidates []rerankCandidate) ([]rerankCandidate, error) {
	if len(candidates) == 0 {
		return candidates, nil
	}
	if s.judge == nil {
		return nil, judge.ErrUnavailable
	}
	if len(candidates) > rerankShortlistLimit {
		candidates = candidates[:rerankShortlistLimit]
	}
	criteria := make(map[string]any, len(candidates))
	for _, candidate := range candidates {
		criteria[candidate.id] = nil
	}
	response, err := s.judge.Ask(ctx, judge.Request{
		State: rerankState(query, candidates),
		Questions: map[string]judge.Question{
			"which": {Kind: judge.KindChoice, Criteria: criteria,
				Instructions: "Which candidate best answers the query?"},
			"exists": {Kind: judge.KindYesNo,
				Instructions: "Does any candidate answer the query?"},
		},
	})
	if err != nil {
		return nil, err
	}
	if response.Answers["exists"].Probability < rerankExistsFloor {
		return []rerankCandidate{}, nil
	}
	which := response.Answers["which"]
	if which.Confidence < rerankMinConfidence {
		return candidates, nil
	}
	ordered := append([]rerankCandidate(nil), candidates...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return which.Probabilities[ordered[i].id] > which.Probabilities[ordered[j].id]
	})
	return ordered, nil
}

// rerankState tags each candidate with its id so the Choice options are the ids, and
// the answer is always a real candidate.
func rerankState(query string, candidates []rerankCandidate) string {
	var builder strings.Builder
	builder.WriteString("QUERY: ")
	builder.WriteString(query)
	builder.WriteString("\n\nCANDIDATES:\n")
	for _, candidate := range candidates {
		builder.WriteString(candidate.id)
		builder.WriteString(" | ")
		builder.WriteString(candidate.title)
		if brief := collapse(candidate.brief); brief != "" {
			builder.WriteString("\n    ")
			builder.WriteString(brief)
		}
		builder.WriteString("\n    ")
		builder.WriteString(collapse(candidate.body))
		builder.WriteString("\n")
	}
	return builder.String()
}

// collapse flattens whitespace and bounds a field so the state stays small.
func collapse(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 280 {
		text = text[:280]
	}
	return text
}
