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

// TaskSearchHit is one ranked task search result.
type TaskSearchHit struct {
	Task  *core.Task
	Score float64
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
// match a title, brief, or body token by prefix; a title match weighs more than
// a brief match, which weighs more than a body match. It reports whether the
// artifact matched all terms.
func LexicalScore(artifact *core.Artifact, terms []string) (float64, bool) {
	if len(terms) == 0 {
		return 0, true
	}
	titleTokens := LexicalTerms(artifact.Title)
	briefTokens := LexicalTerms(artifact.Brief)
	bodyTokens := LexicalTerms(artifact.Body)
	score := 0.0
	for _, term := range terms {
		switch {
		case hasTokenPrefix(titleTokens, term):
			score += 3
		case hasTokenPrefix(briefTokens, term):
			score += 2
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

// LexicalTaskScore scores a task against pre-split terms. Every term must match
// a title, description, or note token by prefix; a title match weighs more than
// a description match, which weighs more than a note match. System notes are not
// scored. It reports whether the task matched all terms.
func LexicalTaskScore(task *core.Task, terms []string) (float64, bool) {
	if len(terms) == 0 {
		return 0, true
	}
	titleTokens := LexicalTerms(task.Title)
	bodyTokens := LexicalTerms(task.Description)
	var noteTokens []string
	for _, note := range task.Notes {
		if note.System {
			continue
		}
		noteTokens = append(noteTokens, LexicalTerms(note.Body)...)
	}
	score := 0.0
	for _, term := range terms {
		switch {
		case hasTokenPrefix(titleTokens, term):
			score += 3
		case hasTokenPrefix(bodyTokens, term):
			score += 2
		case hasTokenPrefix(noteTokens, term):
			score++
		default:
			return 0, false
		}
	}
	return score, true
}

// SortSearchHits orders hits by score (descending), then title and id
// (ascending), so every backend produces the same deterministic order.
func SortSearchHits(hits []SearchHit) {
	sortHits(hits,
		func(hit SearchHit) float64 { return hit.Score },
		func(hit SearchHit) string { return hit.Artifact.Title },
		func(hit SearchHit) string { return string(hit.Artifact.ID) })
}

// SortTaskSearchHits orders task hits by score (descending), then title and id
// (ascending), so every backend produces the same deterministic order.
func SortTaskSearchHits(hits []TaskSearchHit) {
	sortHits(hits,
		func(hit TaskSearchHit) float64 { return hit.Score },
		func(hit TaskSearchHit) string { return hit.Task.Title },
		func(hit TaskSearchHit) string { return string(hit.Task.ID) })
}

// sortHits applies the shared search order: score descending, then title and id
// ascending. It is generic so artifact and task hits share one comparator.
func sortHits[T any](hits []T, score func(T) float64, title func(T) string, id func(T) string) {
	sort.SliceStable(hits, func(i, j int) bool {
		if score(hits[i]) != score(hits[j]) {
			return score(hits[i]) > score(hits[j])
		}
		left := strings.ToLower(title(hits[i]))
		right := strings.ToLower(title(hits[j]))
		if left != right {
			return left < right
		}
		return id(hits[i]) < id(hits[j])
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
