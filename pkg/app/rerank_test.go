package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/judge"
	"github.com/khoinguyen/factotum/pkg/judge/fake"
)

func rerankArtifact(id, title string) *core.Artifact {
	return &core.Artifact{ID: core.ArtifactID(id), Title: title, Body: "body of " + title}
}

func TestRerankOrdersByProbability(t *testing.T) {
	candidates := []*core.Artifact{rerankArtifact("a-1", "first"), rerankArtifact("a-2", "second")}
	f := fake.New(map[string]judge.Answer{
		"which":  {Choice: "a-2", Probabilities: map[string]float64{"a-1": 0.2, "a-2": 0.8}, Confidence: 0.8},
		"exists": {Probability: 0.9},
	})
	got, err := NewRerankService(f).Rerank(context.Background(), "query", candidates)
	if err != nil {
		t.Fatalf("Rerank() error = %v", err)
	}
	if len(got) != 2 || got[0].ID != "a-2" || got[1].ID != "a-1" {
		t.Fatalf("Rerank() order = %v, want [a-2 a-1]", ids(got))
	}
}

func TestRerankReportsNoMatchWhenAbsent(t *testing.T) {
	candidates := []*core.Artifact{rerankArtifact("a-1", "first")}
	f := fake.New(map[string]judge.Answer{
		"which":  {Choice: "a-1", Probabilities: map[string]float64{"a-1": 1}, Confidence: 0.9},
		"exists": {Probability: 0.1},
	})
	got, err := NewRerankService(f).Rerank(context.Background(), "unanswerable", candidates)
	if err != nil {
		t.Fatalf("Rerank() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Rerank() = %v, want an empty result", ids(got))
	}
}

func TestRerankKeepsOrderWhenUnsure(t *testing.T) {
	candidates := []*core.Artifact{rerankArtifact("a-1", "first"), rerankArtifact("a-2", "second")}
	f := fake.New(map[string]judge.Answer{
		"which":  {Choice: "a-2", Probabilities: map[string]float64{"a-1": 0.4, "a-2": 0.6}, Confidence: 0.2},
		"exists": {Probability: 0.9},
	})
	got, err := NewRerankService(f).Rerank(context.Background(), "query", candidates)
	if err != nil {
		t.Fatalf("Rerank() error = %v", err)
	}
	if got[0].ID != "a-1" {
		t.Fatalf("Rerank() order = %v, want the lexical order kept", ids(got))
	}
}

func TestRerankUnavailable(t *testing.T) {
	_, err := NewRerankService(judge.Disabled{}).Rerank(context.Background(), "q",
		[]*core.Artifact{rerankArtifact("a-1", "first")})
	if !errors.Is(err, judge.ErrUnavailable) {
		t.Fatalf("Rerank() error = %v, want judge.ErrUnavailable", err)
	}
}

func TestRerankStateIncludesBrief(t *testing.T) {
	candidates := []*core.Artifact{{ID: "a-1", Title: "a", Brief: "widget brief", Body: "unrelated text"}}
	f := fake.New(map[string]judge.Answer{
		"which":  {Choice: "a-1", Probabilities: map[string]float64{"a-1": 1}, Confidence: 0.9},
		"exists": {Probability: 0.9},
	})
	if _, err := NewRerankService(f).Rerank(context.Background(), "widget", candidates); err != nil {
		t.Fatalf("Rerank() error = %v", err)
	}
	state, ok := f.Requests()[0].State.(string)
	if !ok {
		t.Fatalf("state = %T, want string", f.Requests()[0].State)
	}
	if !strings.Contains(state, "widget brief") {
		t.Fatalf("rerank state omits the brief:\n%s", state)
	}
}

func ids(artifacts []*core.Artifact) []core.ArtifactID {
	out := make([]core.ArtifactID, 0, len(artifacts))
	for _, artifact := range artifacts {
		out = append(out, artifact.ID)
	}
	return out
}

func TestRerankCapsShortlist(t *testing.T) {
	candidates := make([]*core.Artifact, 0, 30)
	for i := 0; i < 30; i++ {
		candidates = append(candidates, rerankArtifact(fmt.Sprintf("a-%02d", i), fmt.Sprintf("doc %d", i)))
	}
	f := fake.New(map[string]judge.Answer{
		"which":  {Choice: "a-00", Probabilities: map[string]float64{"a-00": 1}, Confidence: 0.9},
		"exists": {Probability: 0.9},
	})
	got, err := NewRerankService(f).Rerank(context.Background(), "query", candidates)
	if err != nil {
		t.Fatalf("Rerank() error = %v", err)
	}
	criteria, ok := f.Requests()[0].Questions["which"].Criteria.(map[string]any)
	if !ok {
		t.Fatalf("criteria = %T, want map[string]any", f.Requests()[0].Questions["which"].Criteria)
	}
	if len(criteria) != rerankShortlistLimit {
		t.Fatalf("shortlist options = %d, want %d", len(criteria), rerankShortlistLimit)
	}
	if len(got) != rerankShortlistLimit {
		t.Fatalf("returned %d candidates, want %d", len(got), rerankShortlistLimit)
	}
}
