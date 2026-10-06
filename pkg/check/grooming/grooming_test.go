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

// specWithOrigin is a task refined from an immutable capture. The origin is a
// separate spec the check reads but never mutates.
func specWithOrigin() check.Spec {
	s := spec()
	s.Origin = &check.Origin{ID: "t-idea", Title: "a spark", Body: "ship a --json flag"}
	return s
}

// dims builds the dimension answers for a fake judge. Omitted dimensions default
// to a passing value. The entailment answer is included even when the spec has no
// origin: a fake judge ignores answers to questions that were never asked.
func dims(overrides map[string]float64) map[string]judge.Answer {
	answers := map[string]judge.Answer{}
	for _, name := range append(append([]string{}, dimensionNames...), entailmentDimension) {
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

// originSpec is the immutable capture the entailment cases refine.
func originSpec() check.Origin {
	return check.Origin{ID: "t-idea", Title: "a spark", Body: "ship a --json flag"}
}

// TestGroomingEntailmentTable covers the three acceptance cases - consistent,
// contradicting, and under-specified - offline against the fake judge.
func TestGroomingEntailmentTable(t *testing.T) {
	origin := originSpec()
	cases := []struct {
		name       string
		spec       check.Spec
		answers    map[string]judge.Answer
		verdict    check.Verdict
		findingDim string
		aspect     string
		emptyDelta bool
	}{
		{
			name:       "consistent refinement is ready",
			spec:       check.Spec{ID: "t-1", Title: origin.Title, Kind: "task", Body: origin.Body, Origin: &origin},
			answers:    dims(nil),
			verdict:    check.Ready,
			emptyDelta: true,
		},
		{
			name: "contradicting origin fails with a specific finding",
			spec: check.Spec{ID: "t-1", Title: origin.Title, Kind: "task", Body: "remove the --json flag", Origin: &origin},
			answers: func() map[string]judge.Answer {
				a := dims(map[string]float64{entailmentDimension: 0.20})
				a["gap::"+entailmentDimension] = judge.Answer{Choice: "contradicts origin", Confidence: 0.9}
				return a
			}(),
			verdict:    check.NeedsGrooming,
			findingDim: entailmentDimension,
			aspect:     "contradicts origin",
		},
		{
			name: "under-specified task is gated by its own spec quality",
			spec: check.Spec{ID: "t-1", Title: origin.Title, Kind: "task", Body: "make it better", Origin: &origin},
			answers: func() map[string]judge.Answer {
				a := dims(map[string]float64{"scope_bounded": 0.30})
				a["gap::scope_bounded"] = judge.Answer{Choice: "non-goals missing", Confidence: 0.9}
				return a
			}(),
			verdict:    check.NeedsGrooming,
			findingDim: "scope_bounded",
			aspect:     "non-goals missing",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := New(fake.New(tc.answers)).Run(context.Background(), tc.spec)
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if result.Verdict != tc.verdict {
				t.Fatalf("verdict = %q, want %q (findings %+v)", result.Verdict, tc.verdict, result.Findings)
			}
			if result.OriginID != origin.ID {
				t.Fatalf("origin_id = %q, want %q", result.OriginID, origin.ID)
			}
			if result.Delta == nil {
				t.Fatal("a task with an origin must carry a delta")
			}
			if result.Delta.Empty() != tc.emptyDelta {
				t.Fatalf("delta = %+v, empty = %v, want empty = %v", result.Delta, result.Delta.Empty(), tc.emptyDelta)
			}
			if tc.findingDim == "" {
				if len(result.Findings) != 0 {
					t.Fatalf("ready result must carry no findings: %+v", result.Findings)
				}
				return
			}
			if len(result.Findings) != 1 {
				t.Fatalf("findings = %+v, want exactly the %s finding", result.Findings, tc.findingDim)
			}
			finding := result.Findings[0]
			if finding.Dimension != tc.findingDim || finding.Aspect != tc.aspect || finding.Owner != check.OwnerAgent || finding.Edit == "" {
				t.Fatalf("finding = %+v, want an agent-owned %s/%s with an edit", finding, tc.findingDim, tc.aspect)
			}
		})
	}
}

// TestGroomingShapingDimensionsTable covers the two task-shaping signals the
// rubric adds beyond spec quality: a task that bundles independent units and
// should split (decomposable), and one that is too large to finish as a single
// unit (right_sized). Each is a concrete, agent-owned finding, so the verdict is
// needs_grooming.
func TestGroomingShapingDimensionsTable(t *testing.T) {
	cases := []struct {
		name   string
		dim    string
		aspect string
	}{
		{"task that should split", "decomposable", "bundles independent units"},
		{"task that is too large", "right_sized", "oversized"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answers := dims(map[string]float64{tc.dim: 0.30})
			answers["gap::"+tc.dim] = judge.Answer{Choice: tc.aspect, Confidence: 0.9}
			result := run(t, answers)
			if result.Verdict != check.NeedsGrooming {
				t.Fatalf("verdict = %q, want needs_grooming (findings %+v)", result.Verdict, result.Findings)
			}
			if len(result.Findings) != 1 {
				t.Fatalf("findings = %+v, want exactly the %s finding", result.Findings, tc.dim)
			}
			finding := result.Findings[0]
			if finding.Dimension != tc.dim || finding.Aspect != tc.aspect || finding.Owner != check.OwnerAgent || finding.Edit == "" {
				t.Fatalf("finding = %+v, want an agent-owned %s/%s with an edit", finding, tc.dim, tc.aspect)
			}
		})
	}
}

// TestShapingFindingKeepsTheAuditableDelta proves the shaping signals ride the
// same audit trail as the entailment gate: a below-threshold shaping dimension on
// a promoted task still records the deterministic delta against its origin.
func TestShapingFindingKeepsTheAuditableDelta(t *testing.T) {
	origin := originSpec()
	spec := check.Spec{
		ID:     "t-1",
		Title:  origin.Title,
		Kind:   "task",
		Body:   origin.Body + "\nand do several other things",
		Origin: &origin,
	}
	answers := dims(map[string]float64{"right_sized": 0.30})
	answers["gap::right_sized"] = judge.Answer{Choice: "oversized", Confidence: 0.9}
	result, err := New(fake.New(answers)).Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Delta == nil || result.Delta.Empty() {
		t.Fatalf("delta = %+v, want the auditable refinement delta", result.Delta)
	}
	if len(result.Findings) != 1 || result.Findings[0].Dimension != "right_sized" {
		t.Fatalf("findings = %+v, want the right_sized finding", result.Findings)
	}
}

// TestShapingDimensionIsAdvisoryNotGatedByScore records the shaping signals'
// intent: a low score with no named gap does not, by itself, gate the verdict,
// because a size or split judgment is subjective and an agent cannot reach a
// fixed point against a raw score. A spec-quality dimension behaves the opposite
// way: a very low score stays a gap even when the follow-up names nothing.
func TestShapingDimensionIsAdvisoryNotGatedByScore(t *testing.T) {
	cases := []struct {
		name    string
		dim     string
		verdict check.Verdict
	}{
		{"shaping signal below floor with none stays ready", "right_sized", check.Ready},
		{"spec dimension below floor with none still gates", "scope_bounded", check.NeedsGrooming},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answers := dims(map[string]float64{tc.dim: 0.20})
			answers["gap::"+tc.dim] = judge.Answer{Choice: "none", Confidence: 0.9}
			result := run(t, answers)
			if result.Verdict != tc.verdict {
				t.Fatalf("verdict = %q, want %q (findings %+v)", result.Verdict, tc.verdict, result.Findings)
			}
		})
	}
}

func TestEntailmentDimensionOnlyAskedWithOrigin(t *testing.T) {
	withOrigin := fake.New(dims(nil))
	if _, err := New(withOrigin).Run(context.Background(), specWithOrigin()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, ok := withOrigin.Requests()[0].Questions[entailmentDimension]; !ok {
		t.Fatalf("origin task must ask %s: %v", entailmentDimension, withOrigin.Requests()[0].Questions)
	}

	withoutOrigin := fake.New(dims(nil))
	if _, err := New(withoutOrigin).Run(context.Background(), spec()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, ok := withoutOrigin.Requests()[0].Questions[entailmentDimension]; ok {
		t.Fatalf("originless task must not ask %s", entailmentDimension)
	}
}

func TestNoOriginCarriesNoDeltaOrOriginID(t *testing.T) {
	result := run(t, dims(nil))
	if result.OriginID != "" || result.Delta != nil {
		t.Fatalf("originless result = origin_id %q delta %+v, want both empty", result.OriginID, result.Delta)
	}
}

func TestDeltaFromOrigin(t *testing.T) {
	cases := []struct {
		name    string
		origin  check.Origin
		task    check.Spec
		added   []string
		removed []string
	}{
		{
			name:   "identical is empty",
			origin: check.Origin{ID: "t-i", Title: "t", Body: "one\ntwo"},
			task:   check.Spec{ID: "t-1", Title: "t", Kind: "task", Body: "one\ntwo"},
		},
		{
			name:   "refinement adds lines",
			origin: check.Origin{ID: "t-i", Title: "t", Body: "one"},
			task:   check.Spec{ID: "t-1", Title: "t", Kind: "task", Body: "one\ntwo"},
			added:  []string{"two"},
		},
		{
			name:    "rewrite removes and adds",
			origin:  check.Origin{ID: "t-i", Title: "t", Body: "one\ntwo"},
			task:    check.Spec{ID: "t-1", Title: "t", Kind: "task", Body: "one\nthree"},
			added:   []string{"three"},
			removed: []string{"two"},
		},
		{
			name:   "title change is recorded",
			origin: check.Origin{ID: "t-i", Title: "old", Body: "one"},
			task:   check.Spec{ID: "t-1", Title: "new", Kind: "task", Body: "one"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.task.Origin = &tc.origin
			got := delta(&tc.origin, tc.task)
			if !equalStrings(got.Added, tc.added) || !equalStrings(got.Removed, tc.removed) {
				t.Fatalf("delta = %+v, want added %v removed %v", got, tc.added, tc.removed)
			}
			if tc.origin.Title != tc.task.Title {
				if got.TitleFrom != tc.origin.Title || got.TitleTo != tc.task.Title {
					t.Fatalf("title delta = %q -> %q, want %q -> %q", got.TitleFrom, got.TitleTo, tc.origin.Title, tc.task.Title)
				}
			} else if got.TitleFrom != "" || got.TitleTo != "" {
				t.Fatalf("unchanged title should carry no title delta: %+v", got)
			}
		})
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
