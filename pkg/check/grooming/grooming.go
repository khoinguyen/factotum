// Package grooming is the first task check: an advisory readiness judgment. It
// asks four dimension questions over the task's spec and, for each dimension
// below the threshold, one follow-up that names the concrete gap. The verdict is
// driven by concrete, owned findings, never by the raw scores, so an agent can
// always reach a fixed point.
package grooming

import (
	"context"
	"fmt"
	"strings"

	"github.com/khoinguyen/factotum/pkg/check"
	"github.com/khoinguyen/factotum/pkg/judge"
)

// Name is the check id used by --check and the cache.
const Name = "grooming"

// Version identifies the rubric. Bump it when the questions or the verdict rule
// change, so cached results are invalidated.
const Version = "2"

// Thresholds. A dimension at or above readyThreshold passes; a dimension below
// floor is a real gap even when the follow-up cannot name it, which stops a very
// low score from flipping to ready on a "none".
const (
	readyThreshold = 0.70
	floor          = 0.40
)

const holisticName = "holistic"

// entailmentDimension is asked only when the spec carries an origin: it is the
// consistency gate that keeps a refinement from silently drifting from the
// immutable capture it came from.
const entailmentDimension = "entailment_origin"

// dimensionNames is the fixed dimension order for a task with no origin.
var dimensionNames = []string{
	"scope_bounded",
	"acceptance_verifiable",
	"decisions_author",
	"dependencies_named",
}

// dimensionsFor is the gating dimension order for a spec. A task refined from an
// origin adds the entailment dimension; an originless task is judged on spec
// quality alone, so it never pays for a question that cannot apply.
func dimensionsFor(spec check.Spec) []string {
	if spec.Origin == nil {
		return dimensionNames
	}
	return append(append([]string{}, dimensionNames...), entailmentDimension)
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
	// A contradiction is the groomer's to fix: the origin is immutable, so the
	// task is aligned to it (or the divergence is recorded as a human decision).
	entailmentDimension: {
		{"contradicts origin", check.OwnerAgent, "align the task with its origin, or record the divergence as a decision"},
		{"invents scope", check.OwnerAgent, "drop the invented scope or link a new origin"},
		{"drops origin intent", check.OwnerAgent, "restore the intent the origin states"},
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
	dims := dimensionsFor(spec)
	dimensions, err := c.judge.Ask(ctx, judge.Request{
		State:     state(spec),
		Questions: dimensionQuestions(dims),
	})
	if err != nil {
		return check.Result{}, fmt.Errorf("grooming dimensions: %w", err)
	}

	below := belowThreshold(dims, dimensions)
	result := decide(spec, dims, dimensions)
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
	result.Findings = findings(dims, dimensions, followUps)
	result.Verdict = verdict(result.Findings)
	return result, nil
}

// state is the judge's view of the task: the spec fields plus the origin when one
// exists. The read set is the hash set.
func state(spec check.Spec) map[string]string {
	out := map[string]string{
		"id":    spec.ID,
		"title": spec.Title,
		"kind":  spec.Kind,
		"body":  spec.Body,
	}
	if spec.Origin != nil {
		out["origin_id"] = spec.Origin.ID
		out["origin_title"] = spec.Origin.Title
		out["origin_body"] = spec.Origin.Body
	}
	return out
}

func dimensionQuestions(dims []string) map[string]judge.Question {
	questions := make(map[string]judge.Question, len(dims)+1)
	questions["scope_bounded"] = judge.Question{
		Kind:         judge.KindYesNo,
		Instructions: "Is the task's scope bounded: does it state what is out of scope and where the boundary lies?",
		Criteria: map[string]any{
			"true":  "An engineer can tell what is in scope and what is out.",
			"false": "The boundary is vague, missing, or open-ended.",
		},
	}
	questions["acceptance_verifiable"] = judge.Question{
		Kind:         judge.KindYesNo,
		Instructions: "Does the task state acceptance criteria that an engineer can verify without asking the author?",
		Criteria: map[string]any{
			"true":  "The criteria are concrete and testable.",
			"false": "They are absent, partial, or not testable.",
		},
	}
	questions["decisions_author"] = judge.Question{
		Kind:         judge.KindYesNo,
		Instructions: "Has the author made the decisions that are theirs to make, leaving only ordinary implementation choices to the implementer?",
		Criteria: map[string]any{
			"true":  "Scope, approach, and acceptance are decided by the author.",
			"false": "A product decision, approach, or threshold is still open.",
		},
	}
	questions["dependencies_named"] = judge.Question{
		Kind:         judge.KindYesNo,
		Instructions: "Are the task's prerequisites and foundational dependencies named?",
		Criteria: map[string]any{
			"true":  "Every prerequisite and foundational dependency is declared.",
			"false": "A prerequisite is unstated or a foundational dependency is undeclared.",
		},
	}
	if contains(dims, entailmentDimension) {
		questions[entailmentDimension] = judge.Question{
			Kind:         judge.KindYesNo,
			Instructions: "Is every requirement in this task entailed by its origin capture - no contradiction, and nothing invented beyond what the origin states or clearly implies?",
			Criteria: map[string]any{
				"true":  "The task is a faithful refinement of the origin: it contradicts nothing and adds only detail the origin supports.",
				"false": "It contradicts the origin, invents scope the origin does not support, or drops intent the origin states.",
			},
		}
	}
	questions[holisticName] = judge.Question{
		Kind:         judge.KindYesNo,
		Instructions: "Could an engineer implement and verify this task autonomously from the spec alone?",
		Criteria: map[string]any{
			"true":  "Yes, with only ordinary implementation choices to make.",
			"false": "No, something must be decided or clarified first.",
		},
	}
	return questions
}

// followUpQuestions asks one Choice per below-threshold dimension, each list
// ending in "none".
func followUpQuestions(below []string) map[string]judge.Question {
	questions := make(map[string]judge.Question, len(below))
	for _, name := range below {
		// TypeSafe wants a choice's criteria as a dict of option -> description.
		options := make(map[string]any, len(subAspects[name])+1)
		for _, candidate := range subAspects[name] {
			options[candidate.label] = nil
		}
		options["none"] = nil
		questions["gap::"+name] = judge.Question{
			Kind:         judge.KindChoice,
			Instructions: fmt.Sprintf("Which concrete gap, if any, keeps %s from ready?", name),
			Criteria:     options,
		}
	}
	return questions
}

func belowThreshold(dims []string, response judge.Response) []string {
	var below []string
	for _, name := range dims {
		if response.Answers[name].Probability < readyThreshold {
			below = append(below, name)
		}
	}
	return below
}

// decide builds the score-only part of the result: dimensions, confidence, the
// advisory holistic Noul, and - when there is an origin - the audit trail (the
// origin id and the deterministic refinement delta). Findings are filled in only
// when follow-ups ran.
func decide(spec check.Spec, dims []string, response judge.Response) check.Result {
	result := check.Result{
		Check:           Name,
		CheckVersion:    Version,
		Model:           response.Model,
		ContentHash:     spec.Hash(),
		Verdict:         check.Ready,
		JudgeConfidence: response.Answers[holisticName].Probability,
		Confidence:      confidence(dims, response),
		Note:            check.AdvisoryNote,
	}
	for _, name := range dims {
		result.Dimensions = append(result.Dimensions, check.Dimension{
			Name:  name,
			Value: response.Answers[name].Probability,
		})
	}
	if spec.Origin != nil {
		result.OriginID = spec.Origin.ID
		result.Delta = delta(spec.Origin, spec)
	}
	return result
}

// confidence is the least confident dimension answer, so one shaky judgment
// lowers the reported confidence. A noul answer carries no confidence of its own
// (only its probability), so it is derived from the probability.
func confidence(dims []string, response judge.Response) float64 {
	least := 1.0
	for _, name := range dims {
		answer := response.Answers[name]
		value := answer.Confidence
		if value == 0 {
			value = certainty(answer.Probability)
		}
		if value < least {
			least = value
		}
	}
	return least
}

// certainty maps a yes/no probability to a 0-1 confidence: 0 at p=0.5 (a coin
// flip) and 1 at the extremes.
func certainty(probability float64) float64 {
	if probability < 0.5 {
		return 1 - 2*probability
	}
	return 2*probability - 1
}

// findings turns the follow-up choices into owned gaps. A gap exists when the
// follow-up names a sub-aspect, or when the dimension is below the floor even
// though the model answered "none".
func findings(dims []string, dimensions, followUps judge.Response) []check.Finding {
	var out []check.Finding
	for _, name := range dims {
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
	if dimension == entailmentDimension {
		return "align the task with its origin, or record the divergence as a decision"
	}
	return fmt.Sprintf("name and close the %s gap", dimension)
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// delta is the deterministic refinement difference between the origin capture
// and the groomed task: a title change plus the non-blank lines the task added
// and dropped. It is code, not a model, so it is reproducible and auditable.
func delta(origin *check.Origin, spec check.Spec) *check.Delta {
	added, removed := diffLines(origin.Body, spec.Body)
	out := &check.Delta{Added: added, Removed: removed}
	if origin.Title != spec.Title {
		out.TitleFrom = origin.Title
		out.TitleTo = spec.Title
	}
	return out
}

// diffLines reports the lines present in to but not from (added) and in from but
// not to (removed), preserving each side's order. Duplicate lines are counted, so
// a repeated line is only a delta when its multiplicity changes. Blank lines are
// ignored as noise.
func diffLines(from, to string) (added, removed []string) {
	fromLines := nonBlankLines(from)
	toLines := nonBlankLines(to)
	fromCount := countLines(fromLines)
	toCount := countLines(toLines)
	rem := remainder(fromCount, toCount)
	add := remainder(toCount, fromCount)
	for _, line := range fromLines {
		if rem[line] > 0 {
			removed = append(removed, line)
			rem[line]--
		}
	}
	for _, line := range toLines {
		if add[line] > 0 {
			added = append(added, line)
			add[line]--
		}
	}
	return added, removed
}

func nonBlankLines(body string) []string {
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	return lines
}

func countLines(lines []string) map[string]int {
	counts := make(map[string]int, len(lines))
	for _, line := range lines {
		counts[line]++
	}
	return counts
}

// remainder is the per-line surplus of left over right.
func remainder(left, right map[string]int) map[string]int {
	out := make(map[string]int)
	for line, count := range left {
		if surplus := count - right[line]; surplus > 0 {
			out[line] = surplus
		}
	}
	return out
}
