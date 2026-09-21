package app

import (
	"context"
	"strings"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/judge"
)

const (
	// referenceExistsFloor and referenceMinConfidence gate the suggestion: below either
	// the service stays silent rather than pointing at the wrong task.
	referenceExistsFloor   = 0.5
	referenceMinConfidence = 0.5
)

// Reference is a task a piece of prose appears to mention.
type Reference struct {
	Task       *core.Task
	Confidence float64
}

// ReferenceService finds the task a note or body refers to in prose, so the edge can
// be added. It only suggests: the model selects among real candidates and never
// invents an id.
type ReferenceService struct {
	judge judge.Judge
}

func NewReferenceService(j judge.Judge) *ReferenceService { return &ReferenceService{judge: j} }

// Find returns the candidate the text refers to, or a zero Reference when none does.
func (s *ReferenceService) Find(ctx context.Context, text string, candidates []*core.Task) (Reference, error) {
	if len(candidates) == 0 {
		return Reference{}, nil
	}
	if s.judge == nil {
		return Reference{}, judge.ErrUnavailable
	}
	criteria := make(map[string]any, len(candidates))
	for _, candidate := range candidates {
		criteria[string(candidate.ID)] = nil
	}
	response, err := s.judge.Ask(ctx, judge.Request{
		State: referenceState(text, candidates),
		Questions: map[string]judge.Question{
			"which": {Kind: judge.KindChoice, Criteria: criteria,
				Instructions: "Which candidate task, if any, does the text refer to?"},
			"exists": {Kind: judge.KindYesNo,
				Instructions: "Does the text refer to any of the candidate tasks?"},
		},
	})
	if err != nil {
		return Reference{}, err
	}
	if response.Answers["exists"].Probability < referenceExistsFloor {
		return Reference{}, nil
	}
	which := response.Answers["which"]
	if which.Confidence < referenceMinConfidence {
		return Reference{}, nil
	}
	for _, candidate := range candidates {
		if string(candidate.ID) == which.Choice {
			return Reference{Task: candidate, Confidence: which.Confidence}, nil
		}
	}
	return Reference{}, nil
}

// referenceState tags each candidate with its id so the Choice options are the ids.
func referenceState(text string, candidates []*core.Task) string {
	var builder strings.Builder
	builder.WriteString("TEXT: ")
	builder.WriteString(text)
	builder.WriteString("\n\nCANDIDATE TASKS:\n")
	for _, candidate := range candidates {
		builder.WriteString(string(candidate.ID))
		builder.WriteString(" | ")
		builder.WriteString(candidate.Title)
		builder.WriteString("\n")
	}
	return builder.String()
}
