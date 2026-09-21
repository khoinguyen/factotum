package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/judge"
	"github.com/khoinguyen/factotum/pkg/judge/fake"
)

func refTask(id, title string) *core.Task {
	return &core.Task{ID: core.TaskID(id), Title: title, Description: "body of " + title}
}

func TestReferenceFindSelectsRealTask(t *testing.T) {
	candidates := []*core.Task{refTask("t-a", "Auth refactor"), refTask("t-b", "Ranking")}
	f := fake.New(map[string]judge.Answer{
		"which":  {Choice: "t-a", Confidence: 0.9},
		"exists": {Probability: 0.95},
	})
	ref, err := NewReferenceService(f).Find(context.Background(), "blocked on the auth refactor", candidates)
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if ref.Task == nil || ref.Task.ID != "t-a" {
		t.Fatalf("Reference = %+v, want t-a", ref)
	}
}

func TestReferenceFindReportsNoneWhenAbsent(t *testing.T) {
	candidates := []*core.Task{refTask("t-a", "Auth refactor")}
	f := fake.New(map[string]judge.Answer{
		"which":  {Choice: "t-a", Confidence: 0.9},
		"exists": {Probability: 0.1},
	})
	ref, err := NewReferenceService(f).Find(context.Background(), "unrelated note", candidates)
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if ref.Task != nil {
		t.Fatalf("Reference = %+v, want none", ref)
	}
}

func TestReferenceFindStaysSilentWhenUnsure(t *testing.T) {
	candidates := []*core.Task{refTask("t-a", "Auth refactor")}
	f := fake.New(map[string]judge.Answer{
		"which":  {Choice: "t-a", Confidence: 0.2},
		"exists": {Probability: 0.9},
	})
	ref, err := NewReferenceService(f).Find(context.Background(), "maybe auth", candidates)
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if ref.Task != nil {
		t.Fatalf("Reference = %+v, want none when unsure", ref)
	}
}

func TestReferenceFindUnavailable(t *testing.T) {
	_, err := NewReferenceService(judge.Disabled{}).Find(context.Background(), "x",
		[]*core.Task{refTask("t-a", "Auth refactor")})
	if !errors.Is(err, judge.ErrUnavailable) {
		t.Fatalf("Find() error = %v, want judge.ErrUnavailable", err)
	}
}

func TestReferenceStateTagsCandidates(t *testing.T) {
	state := referenceState("blocked on auth", []*core.Task{refTask("t-a", "Auth refactor")})
	if !strings.Contains(state, "t-a") || !strings.Contains(state, "blocked on auth") {
		t.Fatalf("state = %q, want the text and candidate id", state)
	}
}
