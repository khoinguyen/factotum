package app

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/judge"
)

// intentMinConfidence is the floor for reading a field from a phrase. Below it the
// field is treated as unstated, so a phrase that says nothing yields an empty set
// instead of a defaulted one.
const intentMinConfidence = 0.5

const intentUnspecified = "unspecified"

// IntentService turns a natural-language sentence into the typed fields of a TaskSet.
// Free-text fields (title, description, repo) are out of scope: there is no candidate
// set to select from, so they stay explicit.
type IntentService struct {
	judge judge.Judge
	when  *WhenService
}

func NewIntentService(j judge.Judge) *IntentService {
	return &IntentService{judge: j, when: NewWhenService(j)}
}

// ParseTaskIntent reads the closed-set fields the phrase sets. labels are the candidate
// labels already used in the project. now anchors a not_before phrase.
func (s *IntentService) ParseTaskIntent(ctx context.Context, phrase string, labels []string, now time.Time) (TaskSet, error) {
	response, err := s.judge.Ask(ctx, judge.Request{State: phrase, Questions: intentQuestions(labels)})
	if err != nil {
		return TaskSet{}, err
	}
	answers := response.Answers
	var set TaskSet

	if stated(answers, "status_stated") && answers["status"].Choice != intentUnspecified {
		status := core.TaskStatus(answers["status"].Choice)
		if !status.Valid() {
			return TaskSet{}, fmt.Errorf("%w: unknown status %q", core.ErrInvalid, answers["status"].Choice)
		}
		set.Status = &status
	}
	if stated(answers, "kind_stated") && answers["kind"].Choice != intentUnspecified {
		kind := core.TaskKind(answers["kind"].Choice)
		if !kind.Valid() {
			return TaskSet{}, fmt.Errorf("%w: unknown kind %q", core.ErrInvalid, answers["kind"].Choice)
		}
		set.Kind = &kind
	}
	if stated(answers, "priority_stated") && answers["priority"].Choice != intentUnspecified {
		priority, err := strconv.Atoi(answers["priority"].Choice)
		if err != nil {
			return TaskSet{}, fmt.Errorf("%w: invalid priority %q", core.ErrInvalid, answers["priority"].Choice)
		}
		set.Priority = &priority
	}
	var added []string
	for _, label := range labels {
		if answers["add_label::"+label].Probability >= intentMinConfidence {
			added = append(added, label)
		}
	}
	if len(added) > 0 {
		set.Labels = added
	}
	if stated(answers, "not_before_stated") {
		when, err := s.when.Parse(ctx, phrase, now)
		if err != nil {
			return TaskSet{}, err
		}
		set.NotBefore = &when
	}

	if set.Empty() {
		return TaskSet{}, fmt.Errorf("%w: nothing to set", core.ErrInvalid)
	}
	return set, nil
}

func stated(answers map[string]judge.Answer, id string) bool {
	return answers[id].Probability >= intentMinConfidence
}

// intentQuestions asks one stated yes/no question per optional field, so a phrase that mentions
// nothing does not get a field set by a confident but unstated Choice. The stated
// criteria name what does not count, because words like "urgent" or "block on" are
// labels or dependencies, not priorities or statuses.
func intentQuestions(labels []string) map[string]judge.Question {
	statuses := map[string]any{intentUnspecified: "The phrase does not set a status."}
	for _, status := range []core.TaskStatus{
		core.StatusTodo, core.StatusInProgress, core.StatusBlocked,
		core.StatusReadyForReview, core.StatusDone, core.StatusCancelled,
	} {
		statuses[string(status)] = nil
	}
	kinds := map[string]any{intentUnspecified: "The phrase does not name a kind.",
		string(core.KindTask): nil, string(core.KindMilestone): nil}
	priorities := map[string]any{intentUnspecified: "The phrase does not set a priority.",
		"0": "lowest", "5": "low", "10": "normal", "20": "high", "50": "top priority"}

	questions := map[string]judge.Question{
		"status": {Kind: judge.KindChoice, Criteria: statuses,
			Instructions: "What lifecycle status does the phrase set?"},
		"status_stated": {Kind: judge.KindYesNo, Criteria: map[string]any{
			"true":  "It sets a lifecycle status: start / in progress, ready for review, done, cancelled, or reopened.",
			"false": "It sets anything else: a schedule, a priority, a label, an assignee, or a dependency. 'block it on <task>' and 'don't start until <date>' are not a status.",
		}, Instructions: "Does the phrase explicitly set a lifecycle status for the task?"},
		"kind": {Kind: judge.KindChoice, Criteria: kinds,
			Instructions: "Does the phrase make this a task or a milestone?"},
		"kind_stated": {Kind: judge.KindYesNo, Criteria: map[string]any{
			"true":  "It explicitly calls this a milestone, or explicitly a plain task.",
			"false": "It says nothing about milestone versus ordinary task.",
		}, Instructions: "Does the phrase say whether this is a milestone?"},
		"priority": {Kind: judge.KindChoice, Criteria: priorities,
			Instructions: "Which priority does the phrase indicate (higher is more important)?"},
		"priority_stated": {Kind: judge.KindYesNo, Criteria: map[string]any{
			"true":  "It names a priority: the word priority, high / low / top, or a level number.",
			"false": "It sets anything else, including a label that merely sounds important ('label it urgent').",
		}, Instructions: "Does the phrase explicitly set a priority level?"},
		"not_before_stated": {Kind: judge.KindYesNo, Criteria: map[string]any{
			"true":  "It names an earliest start date or time.",
			"false": "It names no date or time.",
		}, Instructions: "Does the phrase name an earliest start date or time?"},
	}
	for _, label := range labels {
		questions["add_label::"+label] = judge.Question{
			Kind:         judge.KindYesNo,
			Instructions: fmt.Sprintf("Does the phrase add the label %q?", label),
		}
	}
	return questions
}
