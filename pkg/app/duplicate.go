package app

import (
	"context"
	"fmt"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/judge"
)

// Thresholds for routing a pair. The Score's levels are the three actions, so the
// cut points follow from the wording and are not fitted to data.
const (
	duplicateFlagThreshold = 0.5
	duplicateLinkThreshold = 1.5
	duplicateMinConfidence = 0.4
)

// DuplicateVerdict is the outcome of comparing a new task against existing ones.
// Action is "none", "flag" (a human should look), or "link" (probably the same task).
type DuplicateVerdict struct {
	Action      string
	Candidate   *core.Task
	Rating      float64
	Confidence  float64
	SameProblem float64
}

// DuplicateService compares a task against candidates retrieved in code. It never
// mutates anything and never merges: it only advises.
type DuplicateService struct {
	judge judge.Judge
}

func NewDuplicateService(j judge.Judge) *DuplicateService { return &DuplicateService{judge: j} }

// Check compares task against candidates (already retrieved in code) in one request.
func (s *DuplicateService) Check(ctx context.Context, task *core.Task, candidates []*core.Task) (DuplicateVerdict, error) {
	if len(candidates) == 0 {
		return DuplicateVerdict{Action: "none"}, nil
	}
	if s.judge == nil {
		return DuplicateVerdict{}, judge.ErrUnavailable
	}
	state, paths := duplicateState(task, candidates)
	questions := make(map[string]judge.Question, 2*len(candidates))
	for i, candidate := range candidates {
		id := string(candidate.ID)
		questions["link::"+id] = judge.Question{
			Kind: judge.KindRating,
			Instructions: fmt.Sprintf(
				"How do `task_a` and `candidates[%d]` relate as work items?", i),
			Criteria: []string{
				"They describe two different pieces of work; leave them unlinked.",
				"They describe closely related work that may or may not be the same task; a human should decide.",
				"They describe one and the same task.",
			},
		}
		questions["same::"+id] = judge.Question{
			Kind:         judge.KindYesNo,
			Instructions: fmt.Sprintf("Do `task_a` and `candidates[%d]` solve the same underlying problem?", i),
		}
		_ = paths
	}
	response, err := s.judge.Ask(ctx, judge.Request{State: state, Questions: questions})
	if err != nil {
		return DuplicateVerdict{}, err
	}

	best := DuplicateVerdict{Action: "none"}
	for _, candidate := range candidates {
		id := string(candidate.ID)
		score := response.Answers["link::"+id]
		if best.Candidate == nil || score.Rating > best.Rating {
			best = DuplicateVerdict{
				Candidate:   candidate,
				Rating:      score.Rating,
				Confidence:  score.Confidence,
				SameProblem: response.Answers["same::"+id].Probability,
			}
		}
	}
	if best.Confidence < duplicateMinConfidence || best.Rating < duplicateFlagThreshold {
		return DuplicateVerdict{Action: "none"}, nil
	}
	if best.Rating >= duplicateLinkThreshold {
		best.Action = "link"
	} else {
		best.Action = "flag"
	}
	return best, nil
}

// duplicateState puts the new task and every candidate into one state. The paths are
// returned for callers that want to build questions referencing a specific candidate.
func duplicateState(task *core.Task, candidates []*core.Task) (map[string]any, []string) {
	entries := make([]map[string]string, 0, len(candidates))
	paths := make([]string, 0, len(candidates))
	for i, candidate := range candidates {
		entries = append(entries, map[string]string{
			"id":    string(candidate.ID),
			"title": candidate.Title,
			"body":  candidate.Description,
		})
		paths = append(paths, fmt.Sprintf("candidates[%d]", i))
	}
	state := map[string]any{
		"task_a": map[string]string{
			"title": task.Title,
			"body":  task.Description,
		},
		"candidates": entries,
	}
	return state, paths
}
