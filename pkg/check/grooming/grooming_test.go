package grooming

import (
	"context"
	"errors"
	"testing"

	"github.com/khoinguyen/factotum/pkg/check"
	"github.com/khoinguyen/factotum/pkg/judge"
	"github.com/khoinguyen/factotum/pkg/judge/fake"
)

func spec() check.Spec {
	return check.Spec{ID: "t-1", Title: "work", Kind: "task", Body: "a fully specified task"}
}

// dims builds the dimension answers for a fake judge. Omitted dimensions default
// to a passing value.
func dims(overrides map[string]float64) map[string]judge.Answer {
	answers := map[string]judge.Answer{}
	for _, name := range dimensionNames {
		value := 0.9
		if v, ok := overrides[name]; ok {
			value = v
		}
		answers[name] = judge.Answer{Probability: value, Confidence: 0.9}
	}
	if v, ok := overrides[holisticName]; ok {
		answers[holisticName] = judge.Answer{Probability: v, Confidence: 0.9}
	} else {
		answers[holisticName] = judge.Answer{Probability: 0.9, Confidence: 0.9}
	}
	return answers
}

func run(t *testing.T, answers map[string]judge.Answer) check.Result {
	t.Helper()
	c := New(fake.New(answers))
	result, err := c.Run(context.Background(), spec())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	return result
}

func TestAllDimensionsPassIsReadyEvenWhenHolisticIsLow(t *testing.T) {
	result := run(t, dims(map[string]float64{holisticName: 0.55}))
	if result.Verdict != check.Ready {
		t.Fatalf("verdict = %q, want ready", result.Verdict)
	}
	if result.JudgeConfidence != 0.55 {
		t.Fatalf("judge_confidence = %v, want 0.55 (advisory)", result.JudgeConfidence)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("ready result must carry no findings: %+v", result.Findings)
	}
}

func TestBorderlineDimensionWithNoneFollowUpFlipsToReady(t *testing.T) {
	answers := dims(map[string]float64{"scope_bounded": 0.62})
	answers["gap::scope_bounded"] = judge.Answer{Choice: "none", Confidence: 0.9}
	result := run(t, answers)
	if result.Verdict != check.Ready {
		t.Fatalf("verdict = %q, want ready on a none flip", result.Verdict)
	}
}

func TestDimensionBelowFloorNeverFlipsToReady(t *testing.T) {
	answers := dims(map[string]float64{"scope_bounded": 0.20})
	answers["gap::scope_bounded"] = judge.Answer{Choice: "none", Confidence: 0.9}
	result := run(t, answers)
	if result.Verdict != check.NeedsGrooming {
		t.Fatalf("verdict = %q, want needs_grooming", result.Verdict)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("want one unspecified finding, got %+v", result.Findings)
	}
	finding := result.Findings[0]
	if finding.Dimension != "scope_bounded" || finding.Aspect != "" || finding.Owner != check.OwnerAgent {
		t.Fatalf("unspecified gap = %+v, want agent-owned scope_bounded with no aspect", finding)
	}
}

func TestNamedAgentFindingYieldsNeedsGrooming(t *testing.T) {
	answers := dims(map[string]float64{"acceptance_verifiable": 0.30})
	answers["gap::acceptance_verifiable"] = judge.Answer{Choice: "no criteria", Confidence: 0.9}
	result := run(t, answers)
	if result.Verdict != check.NeedsGrooming {
		t.Fatalf("verdict = %q, want needs_grooming", result.Verdict)
	}
	if len(result.Findings) != 1 || result.Findings[0].Owner != check.OwnerAgent || result.Findings[0].Edit == "" {
		t.Fatalf("findings = %+v, want one agent-owned edit", result.Findings)
	}
	if result.Findings[0].Aspect != "no criteria" {
		t.Fatalf("aspect = %q, want the named sub-aspect", result.Findings[0].Aspect)
	}
}

func TestNamedHumanFindingYieldsNeedsHumanWithoutEdit(t *testing.T) {
	answers := dims(map[string]float64{"decisions_author": 0.30})
	answers["gap::decisions_author"] = judge.Answer{Choice: "product decision open", Confidence: 0.9}
	result := run(t, answers)
	if result.Verdict != check.NeedsHuman {
		t.Fatalf("verdict = %q, want needs_human", result.Verdict)
	}
	if len(result.Findings) != 1 || result.Findings[0].Owner != check.OwnerHuman {
		t.Fatalf("findings = %+v, want one human-owned finding", result.Findings)
	}
	if result.Findings[0].Edit != "" {
		t.Fatalf("human finding must propose no body edit, got %q", result.Findings[0].Edit)
	}
}

func TestMixedFindingsReportBothOwners(t *testing.T) {
	answers := dims(map[string]float64{
		"scope_bounded":         0.30,
		"decisions_author":      0.30,
		"acceptance_verifiable": 0.9,
	})
	answers["gap::scope_bounded"] = judge.Answer{Choice: "non-goals missing", Confidence: 0.9}
	answers["gap::decisions_author"] = judge.Answer{Choice: "product decision open", Confidence: 0.9}
	result := run(t, answers)
	if result.Verdict != check.NeedsHuman {
		t.Fatalf("verdict = %q, want needs_human", result.Verdict)
	}
	if len(result.Findings) != 2 {
		t.Fatalf("findings = %+v, want both", result.Findings)
	}
	owners := map[check.Owner]int{}
	for _, finding := range result.Findings {
		owners[finding.Owner]++
	}
	if owners[check.OwnerAgent] != 1 || owners[check.OwnerHuman] != 1 {
		t.Fatalf("owners = %v, want one agent and one human", owners)
	}
}

func TestNoneFlipNeverOverridesANamedHumanFinding(t *testing.T) {
	// decisions sits in the borderline band, but the follow-up names a human
	// decision; the verdict must stay needs_human, not flip to ready.
	answers := dims(map[string]float64{"decisions_author": 0.55})
	answers["gap::decisions_author"] = judge.Answer{Choice: "product decision open", Confidence: 0.9}
	result := run(t, answers)
	if result.Verdict != check.NeedsHuman {
		t.Fatalf("verdict = %q, want needs_human", result.Verdict)
	}
}

func TestFollowUpAskedOnlyForBelowThresholdDimensions(t *testing.T) {
	answers := dims(map[string]float64{"scope_bounded": 0.30})
	answers["gap::scope_bounded"] = judge.Answer{Choice: "none", Confidence: 0.9}
	j := fake.New(answers)
	if _, err := New(j).Run(context.Background(), spec()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	requests := j.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2 (dimensions then follow-ups)", len(requests))
	}
	followUp := requests[1].Questions
	if len(followUp) != 1 {
		t.Fatalf("follow-up questions = %v, want only scope_bounded", followUp)
	}
	if _, ok := followUp["gap::scope_bounded"]; !ok {
		t.Fatalf("follow-up questions = %v, want gap::scope_bounded", followUp)
	}
}

// TestCriteriaUseTheTypeSafeWireShape guards the real API: a noul or choice
// criteria must be an object, not an array (the API returns 422 for an array).
// The fake judge ignores criteria, so this asserts the captured request.
func TestCriteriaUseTheTypeSafeWireShape(t *testing.T) {
	answers := dims(map[string]float64{"scope_bounded": 0.3})
	answers["gap::scope_bounded"] = judge.Answer{Choice: "non-goals missing", Confidence: 0.9}
	j := fake.New(answers)
	if _, err := New(j).Run(context.Background(), spec()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	requests := j.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	for id, question := range requests[0].Questions {
		criteria, ok := question.Criteria.(map[string]any)
		if !ok {
			t.Fatalf("noul %s criteria type = %T, want map[string]any", id, question.Criteria)
		}
		if _, ok := criteria["true"]; !ok {
			t.Errorf("noul %s criteria missing the true key: %v", id, criteria)
		}
		if _, ok := criteria["false"]; !ok {
			t.Errorf("noul %s criteria missing the false key: %v", id, criteria)
		}
	}
	criteria, ok := requests[1].Questions["gap::scope_bounded"].Criteria.(map[string]any)
	if !ok {
		t.Fatalf("choice criteria type = %T, want map[string]any", requests[1].Questions["gap::scope_bounded"].Criteria)
	}
	for _, option := range []string{"non-goals missing", "none"} {
		if _, ok := criteria[option]; !ok {
			t.Errorf("choice criteria missing %q: %v", option, criteria)
		}
	}
}

func TestConfidenceDerivedFromNoulWhenAbsent(t *testing.T) {
	// TypeSafe returns no confidence for a noul answer, so it must be derived
	// from the probability rather than reported as 0.
	answers := map[string]judge.Answer{}
	for _, name := range dimensionNames {
		answers[name] = judge.Answer{Probability: 0.9}
	}
	answers[holisticName] = judge.Answer{Probability: 0.9}
	result := run(t, answers)
	if result.Confidence < 0.79 || result.Confidence > 0.81 {
		t.Fatalf("confidence = %v, want ~0.80 derived from p=0.9", result.Confidence)
	}
}

func TestConfidenceUsesProvidedWhenPresent(t *testing.T) {
	answers := dims(map[string]float64{})
	answers["scope_bounded"] = judge.Answer{Probability: 0.9, Confidence: 0.5}
	result := run(t, answers)
	if result.Confidence != 0.5 {
		t.Fatalf("confidence = %v, want the provided 0.5", result.Confidence)
	}
}

func TestJudgeUnavailablePropagates(t *testing.T) {
	j := fake.New(nil)
	j.FailWith(judge.ErrUnavailable)
	_, err := New(j).Run(context.Background(), spec())
	if !errors.Is(err, judge.ErrUnavailable) {
		t.Fatalf("Run() error = %v, want ErrUnavailable", err)
	}
}

func TestNameAndVersion(t *testing.T) {
	c := New(nil)
	if c.Name() != "grooming" || c.Version() == "" {
		t.Fatalf("Name/Version = %q/%q", c.Name(), c.Version())
	}
}
