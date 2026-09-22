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

func rerankTask(id, title string) *core.Task {
	return &core.Task{ID: core.TaskID(id), Title: title, Description: "body of " + title}
}

func TestRerankTasksOrdersByProbability(t *testing.T) {
	candidates := []*core.Task{rerankTask("t-1", "first"), rerankTask("t-2", "second")}
	f := fake.New(map[string]judge.Answer{
		"which":  {Choice: "t-2", Probabilities: map[string]float64{"t-1": 0.2, "t-2": 0.8}, Confidence: 0.8},
		"exists": {Probability: 0.9},
	})
	got, err := NewRerankService(f).RerankTasks(context.Background(), "query", candidates)
	if err != nil {
		t.Fatalf("RerankTasks() error = %v", err)
	}
	if len(got) != 2 || got[0].ID != "t-2" || got[1].ID != "t-1" {
		t.Fatalf("RerankTasks() order = %v, want [t-2 t-1]", taskIDs(got))
	}
}

func TestRerankTasksReportsNoMatchWhenAbsent(t *testing.T) {
	candidates := []*core.Task{rerankTask("t-1", "first")}
	f := fake.New(map[string]judge.Answer{
		"which":  {Choice: "t-1", Probabilities: map[string]float64{"t-1": 1}, Confidence: 0.9},
		"exists": {Probability: 0.1},
	})
	got, err := NewRerankService(f).RerankTasks(context.Background(), "unanswerable", candidates)
	if err != nil {
		t.Fatalf("RerankTasks() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("RerankTasks() = %v, want an empty result", taskIDs(got))
	}
}

func TestRerankTasksKeepsOrderWhenUnsure(t *testing.T) {
	candidates := []*core.Task{rerankTask("t-1", "first"), rerankTask("t-2", "second")}
	f := fake.New(map[string]judge.Answer{
		"which":  {Choice: "t-2", Probabilities: map[string]float64{"t-1": 0.4, "t-2": 0.6}, Confidence: 0.2},
		"exists": {Probability: 0.9},
	})
	got, err := NewRerankService(f).RerankTasks(context.Background(), "query", candidates)
	if err != nil {
		t.Fatalf("RerankTasks() error = %v", err)
	}
	if got[0].ID != "t-1" {
		t.Fatalf("RerankTasks() order = %v, want the lexical order kept", taskIDs(got))
	}
}

func TestRerankTasksUnavailable(t *testing.T) {
	_, err := NewRerankService(judge.Disabled{}).RerankTasks(context.Background(), "q",
		[]*core.Task{rerankTask("t-1", "first")})
	if !errors.Is(err, judge.ErrUnavailable) {
		t.Fatalf("RerankTasks() error = %v, want judge.ErrUnavailable", err)
	}
}

func TestRerankTaskStateIncludesDescription(t *testing.T) {
	candidates := []*core.Task{{ID: "t-1", Title: "a", Description: "widget description"}}
	f := fake.New(map[string]judge.Answer{
		"which":  {Choice: "t-1", Probabilities: map[string]float64{"t-1": 1}, Confidence: 0.9},
		"exists": {Probability: 0.9},
	})
	if _, err := NewRerankService(f).RerankTasks(context.Background(), "widget", candidates); err != nil {
		t.Fatalf("RerankTasks() error = %v", err)
	}
	state, ok := f.Requests()[0].State.(string)
	if !ok {
		t.Fatalf("state = %T, want string", f.Requests()[0].State)
	}
	if !strings.Contains(state, "widget description") {
		t.Fatalf("rerank task state omits the description:\n%s", state)
	}
}

func taskIDs(tasks []*core.Task) []core.TaskID {
	out := make([]core.TaskID, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, task.ID)
	}
	return out
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
