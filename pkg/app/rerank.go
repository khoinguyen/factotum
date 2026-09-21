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

// Rerank orders candidates by how well they answer query. Candidates must arrive in
// fast-search order; only the top rerankShortlistLimit are reranked. It returns an
// empty slice when no candidate answers the query, the input order when the judge is
// unsure, and judge.ErrUnavailable when no judge is configured.
func (s *RerankService) Rerank(ctx context.Context, query string, candidates []*core.Artifact) ([]*core.Artifact, error) {
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
		criteria[string(candidate.ID)] = nil
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
		return []*core.Artifact{}, nil
	}
	which := response.Answers["which"]
	if which.Confidence < rerankMinConfidence {
		return candidates, nil
	}
	ordered := append([]*core.Artifact(nil), candidates...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return which.Probabilities[string(ordered[i].ID)] > which.Probabilities[string(ordered[j].ID)]
	})
	return ordered, nil
}

// rerankState tags each candidate with its id so the Choice options are the ids, and
// the answer is always a real candidate.
func rerankState(query string, candidates []*core.Artifact) string {
	var builder strings.Builder
	builder.WriteString("QUERY: ")
	builder.WriteString(query)
	builder.WriteString("\n\nCANDIDATES:\n")
	for _, candidate := range candidates {
		body := strings.Join(strings.Fields(candidate.Body), " ")
		if len(body) > 280 {
			body = body[:280]
		}
		builder.WriteString(string(candidate.ID))
		builder.WriteString(" | ")
		builder.WriteString(candidate.Title)
		builder.WriteString("\n    ")
		builder.WriteString(body)
		builder.WriteString("\n")
	}
	return builder.String()
}
