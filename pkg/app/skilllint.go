package app

import (
	"context"
	"fmt"

	"github.com/khoinguyen/factotum/pkg/judge"
)

// skillRealFloor is the probability below which a candidate token is reported as
// drift: the judge believes it is not a real ft command or flag.
const skillRealFloor = 0.5

// SkillInventory is the set of real ft command and flag names, built from the
// cobra command tree. It is the only names the semantic pass trusts.
type SkillInventory struct {
	Commands []string `json:"commands"`
	Flags    []string `json:"flags"`
}

// SkillTokenSet is one skill's candidate tokens and the inventory to judge them
// against. The candidates come from the skill body; the inventory comes from the
// command tree, so the judge never invents a name.
type SkillTokenSet struct {
	Skill     string
	Tokens    []string
	Inventory SkillInventory
}

// SkillTokenFlag is a candidate token the judge did not recognize as real.
type SkillTokenFlag struct {
	Skill       string
	Token       string
	Probability float64
}

// SkillLintService judges candidate skill tokens against a code-built inventory.
// It advises only: it never edits a skill, and the deterministic parser stays the
// gate. It returns judge.ErrUnavailable when no judge is configured.
type SkillLintService struct {
	judge judge.Judge
}

// NewSkillLintService builds the semantic lint over a judge. A nil judge makes
// Lint return judge.ErrUnavailable.
func NewSkillLintService(j judge.Judge) *SkillLintService { return &SkillLintService{judge: j} }

// Lint asks one yes/no question per candidate token in one request, then returns
// the tokens the judge scored below skillRealFloor, in candidate order.
func (s *SkillLintService) Lint(ctx context.Context, set SkillTokenSet) ([]SkillTokenFlag, error) {
	if len(set.Tokens) == 0 {
		return nil, nil
	}
	if s.judge == nil {
		return nil, judge.ErrUnavailable
	}
	questions := make(map[string]judge.Question, len(set.Tokens))
	for _, token := range set.Tokens {
		questions[token] = judge.Question{
			Kind:         judge.KindYesNo,
			Instructions: fmt.Sprintf("Is %q a real ft command or flag?", token),
			Criteria: map[string]any{
				"true":  "It appears in the inventory as an ft command or flag.",
				"false": "It is not an ft command or flag.",
			},
		}
	}
	response, err := s.judge.Ask(ctx, judge.Request{
		State: map[string]any{
			"skill":     set.Skill,
			"tokens":    set.Tokens,
			"inventory": set.Inventory,
		},
		Questions: questions,
	})
	if err != nil {
		return nil, err
	}
	var flags []SkillTokenFlag
	for _, token := range set.Tokens {
		if response.Answers[token].Probability < skillRealFloor {
			flags = append(flags, SkillTokenFlag{
				Skill:       set.Skill,
				Token:       token,
				Probability: response.Answers[token].Probability,
			})
		}
	}
	return flags, nil
}
