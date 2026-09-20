package store

import (
	"sort"
	"strings"
	"unicode"

	"github.com/khoinguyen/factotum/pkg/core"
)

// SearchHit is one ranked artifact search result.
type SearchHit struct {
	Artifact *core.Artifact
	Score    float64
}

// LexicalTerms splits a query into lowercase alphanumeric terms. Query terms
// are separated by any non-alphanumeric run, so "infra-as-code" yields
// ["infra","as","code"]. An empty result means "match everything".
func LexicalTerms(query string) []string {
	var terms []string
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			terms = append(terms, strings.ToLower(word.String()))
			word.Reset()
		}
	}
	for _, r := range query {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			word.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return terms
}

// LexicalScore scores an artifact against pre-split terms. Every term must
// match a title or body token by prefix; a title match weighs more than a body
// match. It reports whether the artifact matched all terms.
func LexicalScore(artifact *core.Artifact, terms []string) (float64, bool) {
	if len(terms) == 0 {
		return 0, true
	}
	titleTokens := LexicalTerms(artifact.Title)
	bodyTokens := LexicalTerms(artifact.Body)
	score := 0.0
	for _, term := range terms {
		switch {
		case hasTokenPrefix(titleTokens, term):
			score += 3
		case hasTokenPrefix(bodyTokens, term):
			score++
		default:
			return 0, false
		}
	}
	return score, true
}

func hasTokenPrefix(tokens []string, prefix string) bool {
	for _, token := range tokens {
		if strings.HasPrefix(token, prefix) {
			return true
		}
	}
	return false
}

// SortSearchHits orders hits by score (descending), then title and id
// (ascending), so every backend produces the same deterministic order.
func SortSearchHits(hits []SearchHit) {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		left := strings.ToLower(hits[i].Artifact.Title)
		right := strings.ToLower(hits[j].Artifact.Title)
		if left != right {
			return left < right
		}
		return hits[i].Artifact.ID < hits[j].Artifact.ID
	})
}

// MatchesArtifactFilter reports whether an artifact is in a filter's scope.
func MatchesArtifactFilter(artifact *core.Artifact, filter ArtifactFilter) bool {
	if filter.ProjectID != "" && artifact.ProjectID != filter.ProjectID {
		return false
	}
	if filter.TaskID != nil && (artifact.TaskID == nil || *artifact.TaskID != *filter.TaskID) {
		return false
	}
	if filter.Kind != nil && artifact.Kind != *filter.Kind {
		return false
	}
	return true
}
