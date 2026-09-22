package app

import (
	"context"
	"errors"
	"testing"

	"github.com/khoinguyen/factotum/pkg/judge"
	"github.com/khoinguyen/factotum/pkg/judge/fake"
)

func TestSkillLintFlagsTokensBelowHalf(t *testing.T) {
	j := fake.New(map[string]judge.Answer{
		"task":       {Probability: 0.98},
		"frobnicate": {Probability: 0.10},
		"--bogus":    {Probability: 0.30},
	})
	flags, err := NewSkillLintService(j).Lint(context.Background(), SkillTokenSet{
		Skill:     "ft",
		Tokens:    []string{"task", "frobnicate", "--bogus"},
		Inventory: SkillInventory{Commands: []string{"ft task"}, Flags: []string{"--force"}},
	})
	if err != nil {
		t.Fatalf("Lint() error = %v", err)
	}
	if len(flags) != 2 {
		t.Fatalf("flags = %+v, want two flagged tokens", flags)
	}
	if flags[0].Token != "frobnicate" || flags[0].Probability != 0.10 || flags[0].Skill != "ft" {
		t.Fatalf("flags[0] = %+v", flags[0])
	}
	if flags[1].Token != "--bogus" {
		t.Fatalf("flags[1] = %+v", flags[1])
	}
}

func TestSkillLintKeepsTokensAtOrAboveHalf(t *testing.T) {
	j := fake.New(map[string]judge.Answer{
		"task":    {Probability: 0.50},
		"next":    {Probability: 0.99},
		"--force": {Probability: 0.50},
	})
	flags, err := NewSkillLintService(j).Lint(context.Background(), SkillTokenSet{
		Skill:  "ft",
		Tokens: []string{"task", "next", "--force"},
	})
	if err != nil {
		t.Fatalf("Lint() error = %v", err)
	}
	if len(flags) != 0 {
		t.Fatalf("flags = %+v, want none at the 0.5 floor", flags)
	}
}

func TestSkillLintAsksOneQuestionPerTokenWithInventory(t *testing.T) {
	j := fake.New(map[string]judge.Answer{"task": {Probability: 0.9}})
	inventory := SkillInventory{Commands: []string{"ft task", "ft task next"}, Flags: []string{"--force"}}
	if _, err := NewSkillLintService(j).Lint(context.Background(), SkillTokenSet{
		Skill:     "ft",
		Tokens:    []string{"task", "--force"},
		Inventory: inventory,
	}); err != nil {
		t.Fatalf("Lint() error = %v", err)
	}
	requests := j.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want one per skill", len(requests))
	}
	questions := requests[0].Questions
	if len(questions) != 2 {
		t.Fatalf("questions = %+v, want one per token", questions)
	}
	for _, token := range []string{"task", "--force"} {
		question, ok := questions[token]
		if !ok || question.Kind != judge.KindYesNo {
			t.Fatalf("question %q = %+v, want a yes/no question", token, question)
		}
	}
	state, ok := requests[0].State.(map[string]any)
	if !ok {
		t.Fatalf("state = %#v, want a map carrying the inventory", requests[0].State)
	}
	got, ok := state["inventory"].(SkillInventory)
	if !ok || len(got.Commands) != 2 || len(got.Flags) != 1 {
		t.Fatalf("state inventory = %#v, want the code-built inventory", state["inventory"])
	}
}

func TestSkillLintWithoutJudgeIsUnavailable(t *testing.T) {
	for name, j := range map[string]judge.Judge{"nil": nil, "disabled": judge.Disabled{}} {
		t.Run(name, func(t *testing.T) {
			_, err := NewSkillLintService(j).Lint(context.Background(), SkillTokenSet{Tokens: []string{"task"}})
			if !errors.Is(err, judge.ErrUnavailable) {
				t.Fatalf("Lint() error = %v, want ErrUnavailable", err)
			}
		})
	}
}

func TestSkillLintNoTokensDoesNotAsk(t *testing.T) {
	j := fake.New(nil)
	flags, err := NewSkillLintService(j).Lint(context.Background(), SkillTokenSet{Skill: "ft"})
	if err != nil {
		t.Fatalf("Lint() error = %v", err)
	}
	if len(flags) != 0 {
		t.Fatalf("flags = %+v, want none", flags)
	}
	if len(j.Requests()) != 0 {
		t.Fatalf("requests = %d, want none for an empty token set", len(j.Requests()))
	}
}
