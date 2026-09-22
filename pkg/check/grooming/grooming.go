// Package grooming is the first task check: an advisory readiness judgment. It
// asks four dimension questions over the task's spec and, for each dimension
// below the threshold, one follow-up that names the concrete gap. The verdict is
// driven by concrete, owned findings, never by the raw scores, so an agent can
// always reach a fixed point.
package grooming

import (
	"context"
	"fmt"

	"github.com/khoinguyen/factotum/pkg/check"
	"github.com/khoinguyen/factotum/pkg/judge"
)

// Name is the check id used by --check and the cache.
const Name = "grooming"

// Version identifies the rubric. Bump it when the questions or the verdict rule
// change, so cached results are invalidated.
const Version = "1"

// Thresholds. A dimension at or above readyThreshold passes; a dimension below
// floor is a real gap even when the follow-up cannot name it, which stops a very
// low score from flipping to ready on a "none".
const (
	readyThreshold = 0.70
	floor          = 0.40
)

const holisticName = "holistic"

// dimensionNames is the fixed dimension order.
var dimensionNames = []string{
	"scope_bounded",
	"acceptance_verifiable",
	"decisions_author",
	"dependencies_named",
}

// aspect is one named sub-aspect of a dimension, with the owner who can close it
// and the edit that closes it.
type aspect struct {
	label string
	owner check.Owner
	edit  string
}

// subAspects are the fixed options of each dimension's follow-up. Every list ends
// in "none", which the model picks when it cannot name a concrete gap.
var subAspects = map[string][]aspect{
	"scope_bounded": {
		{"out-of-scope missing", check.OwnerAgent, "add an explicit Out of scope section"},
		{"boundary vague", check.OwnerAgent, "name what is in and out of scope"},
		{"non-goals missing", check.OwnerAgent, "add a Non-goals section"},
	},
	"acceptance_verifiable": {
		{"no criteria", check.OwnerAgent, "add acceptance criteria an engineer can test"},
		{"not testable", check.OwnerAgent, "rewrite the criteria as an observable check"},
		{"partial", check.OwnerAgent, "complete the acceptance criteria for the remaining behavior"},
	},
	"decisions_author": {
		// A human-owned gap carries no body edit: editing the spec cannot
		// satisfy a decision only the author can make.
		{"product decision open", check.OwnerHuman, ""},
		{"approach unspecified", check.OwnerAgent, "pick and record a default approach"},
		{"threshold unfixed", check.OwnerAgent, "pick and record a default threshold"},
	},
	"dependencies_named": {
		{"prerequisite unstated", check.OwnerAgent, "name the prerequisite work"},
		{"not declared foundational", check.OwnerAgent, "declare the foundational dependency"},
	},
}

// Check is the judge-backed grooming check.
type Check struct {
	judge judge.Judge
}

// New builds the grooming check over a judge. A nil judge makes Run return
// judge.ErrUnavailable.
func New(j judge.Judge) *Check { return &Check{judge: j} }

func (c *Check) Name() string    { return Name }
func (c *Check) Version() string { return Version }

// Run judges the spec in two batched requests: the four dimensions plus the
// advisory holistic Noul, then one follow-up Choice per below-threshold
// dimension. The holistic value never gates the verdict.
func (c *Check) Run(ctx context.Context, spec check.Spec) (check.Result, error) {
	if c.judge == nil {
		return check.Result{}, judge.ErrUnavailable
	}
	dimensions, err := c.judge.Ask(ctx, judge.Request{
		State:     state(spec),
		Questions: dimensionQuestions(),
	})
	if err != nil {
		return check.Result{}, fmt.Errorf("grooming dimensions: %w", err)
	}

	below := belowThreshold(dimensions)
	result := decide(spec, dimensions)
	if len(below) == 0 {
		return result, nil
	}
	followUps, err := c.judge.Ask(ctx, judge.Request{
		State:     state(spec),
		Questions: followUpQuestions(below),
	})
	if err != nil {
		return check.Result{}, fmt.Errorf("grooming follow-ups: %w", err)
	}
	result.Findings = findings(dimensions, followUps)
	result.Verdict = verdict(result.Findings)
	return result, nil
}

// state is the judge's view of the task: the spec fields only. The read set is
// the hash set.
func state(spec check.Spec) map[string]string {
	return map[string]string{
		"id":    spec.ID,
		"title": spec.Title,
		"kind":  spec.Kind,
		"body":  spec.Body,
	}
}

func dimensionQuestions() map[string]judge.Question {
	questions := make(map[string]judge.Question, len(dimensionNames)+1)
	questions["scope_bounded"] = judge.Question{
		Kind:         judge.KindYesNo,
		Instructions: "Is the task's scope bounded: does it state what is out of scope and where the boundary lies?",
		Criteria: []string{
			"true: an engineer can tell what is in scope and what is out",
			"false: the boundary is vague, missing, or open-ended",
		},
	}
	questions["acceptance_verifiable"] = judge.Question{
		Kind:         judge.KindYesNo,
		Instructions: "Does the task state acceptance criteria that an engineer can verify without asking the author?",
		Criteria: []string{
			"true: the criteria are concrete and testable",
			"false: they are absent, partial, or not testable",
		},
	}
	questions["decisions_author"] = judge.Question{
		Kind:         judge.KindYesNo,
		Instructions: "Has the author made the decisions that are theirs to make, leaving only ordinary implementation choices to the implementer?",
		Criteria: []string{
			"true: scope, approach, and acceptance are decided by the author",
			"false: a product decision, approach, or threshold is still open",
		},
	}
	questions["dependencies_named"] = judge.Question{
		Kind:         judge.KindYesNo,
		Instructions: "Are the task's prerequisites and foundational dependencies named?",
		Criteria: []string{
			"true: every prerequisite and foundational dependency is declared",
			"false: a prerequisite is unstated or a foundational dependency is undeclared",
		},
	}
	questions[holisticName] = judge.Question{
		Kind:         judge.KindYesNo,
		Instructions: "Could an engineer implement and verify this task autonomously from the spec alone?",
		Criteria: []string{
			"true: yes, with only ordinary implementation choices to make",
			"false: no, something must be decided or clarified first",
		},
	}
	return questions
}

// followUpQuestions asks one Choice per below-threshold dimension, each list
// ending in "none".
func followUpQuestions(below []string) map[string]judge.Question {
	questions := make(map[string]judge.Question, len(below))
	for _, name := range below {
		options := make([]string, 0, len(subAspects[name])+1)
		for _, candidate := range subAspects[name] {
			options = append(options, candidate.label)
		}
		options = append(options, "none")
		questions["gap::"+name] = judge.Question{
			Kind:         judge.KindChoice,
			Instructions: fmt.Sprintf("Which concrete gap, if any, keeps %s from ready?", name),
			Criteria:     options,
		}
	}
	return questions
}

func belowThreshold(response judge.Response) []string {
	var below []string
	for _, name := range dimensionNames {
		if response.Answers[name].Probability < readyThreshold {
			below = append(below, name)
		}
	}
	return below
}

// decide builds the score-only part of the result: dimensions, confidence, and
// the advisory holistic Noul. Findings are filled in only when follow-ups ran.
func decide(spec check.Spec, response judge.Response) check.Result {
	result := check.Result{
		Check:           Name,
		CheckVersion:    Version,
		Model:           response.Model,
		ContentHash:     spec.Hash(),
		Verdict:         check.Ready,
		JudgeConfidence: response.Answers[holisticName].Probability,
		Confidence:      confidence(response),
		Note:            check.AdvisoryNote,
	}
	for _, name := range dimensionNames {
		result.Dimensions = append(result.Dimensions, check.Dimension{
			Name:  name,
			Value: response.Answers[name].Probability,
		})
	}
	return result
}

// confidence is the least confident dimension answer, so one shaky judgment
// lowers the reported confidence.
func confidence(response judge.Response) float64 {
	least := 1.0
	for _, name := range dimensionNames {
		value := response.Answers[name].Confidence
		if value < least {
			least = value
		}
	}
	return least
}

// findings turns the follow-up choices into owned gaps. A gap exists when the
// follow-up names a sub-aspect, or when the dimension is below the floor even
// though the model answered "none".
func findings(dimensions, followUps judge.Response) []check.Finding {
	var out []check.Finding
	for _, name := range dimensionNames {
		value := dimensions.Answers[name].Probability
		if value >= readyThreshold {
			continue
		}
		choice := followUps.Answers["gap::"+name].Choice
		if named, ok := lookupAspect(name, choice); ok {
			out = append(out, check.Finding{
				Dimension: name,
				Aspect:    named.label,
				Owner:     named.owner,
				Edit:      named.edit,
			})
			continue
		}
		if value < floor {
			out = append(out, check.Finding{
				Dimension: name,
				Owner:     defaultOwner(name),
				Edit:      unspecifiedEdit(name),
			})
		}
	}
	return out
}

func verdict(findings []check.Finding) check.Verdict {
	if len(findings) == 0 {
		return check.Ready
	}
	for _, finding := range findings {
		if finding.Owner == check.OwnerHuman {
			return check.NeedsHuman
		}
	}
	return check.NeedsGrooming
}

func lookupAspect(dimension, label string) (aspect, bool) {
	for _, candidate := range subAspects[dimension] {
		if candidate.label == label {
			return candidate, true
		}
	}
	return aspect{}, false
}

// defaultOwner is the owner an unspecified gap takes: the decisions dimension is
// the author's, the rest are the implementer's.
func defaultOwner(dimension string) check.Owner {
	if dimension == "decisions_author" {
		return check.OwnerHuman
	}
	return check.OwnerAgent
}

func unspecifiedEdit(dimension string) string {
	if defaultOwner(dimension) == check.OwnerHuman {
		return ""
	}
	return fmt.Sprintf("name and close the %s gap", dimension)
}
