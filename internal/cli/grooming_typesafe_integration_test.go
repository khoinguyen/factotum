package cli

import (
	"encoding/json"
	"os"
	"testing"
)

// These tests exercise the real TypeSafe judge end to end through `ft task check`:
// the grooming rubric questions, the follow-up Choice, and the verdict rule run
// against the live model. They are opt-in so `go test ./...` and CI stay hermetic.
//
// Run them with a real key:
//
//	TYPESAFE_API_KEY=... go test -count=1 -run TestRealGrooming -v ./internal/cli/
//
// The store is a throwaway JSON file under the test's temp dir, so the real project
// database is never touched.

const realGroomingGate = "TYPESAFE_API_KEY"

// groomingDimensions is the gating dimension order grooming.decide emits. A
// mis-keyed wire answer leaves the dimension at its zero value, which the
// value check below catches.
var groomingDimensions = []string{
	"scope_bounded",
	"acceptance_verifiable",
	"decisions_author",
	"dependencies_named",
}

// realStrongSpec is deliberately fully groomed: bounded scope, verifiable
// acceptance, decided approach, named dependencies.
const realStrongSpec = `## Goal
Add a --json flag to ` + "`ft foo`" + ` that prints the command's result as a single JSON object.

## Scope
In scope: the --json flag, its serialization, and tests.
Out of scope: changing the existing text output or any other command.

## Acceptance
- ` + "`ft foo --json`" + ` prints one JSON object with string keys "id" and "title".
- Without --json the output is byte-identical to today.
- ` + "`go test ./internal/cli/...`" + ` passes.

## Decisions
The flag is named --json, it writes to stdout, and the schema is frozen as above.

## Dependencies
None; the standard library JSON encoder is already used elsewhere.`

// realWeakSpec is deliberately ungroomed: no scope, no acceptance, open
// decisions, and unstated dependencies.
const realWeakSpec = `Make it better. It should probably be faster and nicer, and maybe cleaner too. We can
figure out the details later; someone should decide how it should look. Might need to
touch the API as well.`

// realCheckResult mirrors the stable `ft task check -o json` document, narrowed to
// the fields this smoke asserts.
type realCheckResult struct {
	Check           string  `json:"check"`
	CheckVersion    string  `json:"check_version"`
	Verdict         string  `json:"verdict"`
	Checked         bool    `json:"checked"`
	JudgeConfidence float64 `json:"judge_confidence"`
	Dimensions      []struct {
		Name  string  `json:"name"`
		Value float64 `json:"value"`
	} `json:"dimensions"`
	Findings []struct {
		Dimension string `json:"dimension"`
		Aspect    string `json:"aspect"`
		Owner     string `json:"owner"`
		Edit      string `json:"edit"`
	} `json:"findings"`
}

// realGroomingRunner builds a runner whose judge is built from the environment, not
// a fake, so the real TypeSafe HTTP adapter and the whole grooming rubric run. It
// skips when no key is present, keeping the default test run offline.
func realGroomingRunner(t *testing.T) *runner {
	t.Helper()
	if os.Getenv(realGroomingGate) == "" {
		t.Skipf("set %s to run the real-TypeSafe grooming smoke", realGroomingGate)
	}
	r := newRunner(t)
	r.getenv = os.Getenv
	return r
}

// runRealCheck runs `ft task check -o json` and returns the one check result.
func runRealCheck(t *testing.T, r *runner, taskID string) realCheckResult {
	t.Helper()
	out := r.run("task", "check", taskID, "-o", "json")
	var results []realCheckResult
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("task check -o json: %v\n%s", err, out)
	}
	if len(results) != 1 {
		t.Fatalf("want one check result, got %d: %s", len(results), out)
	}
	return results[0]
}

// assertDimensionsPresent fails when a gating dimension is missing or still at its
// zero value, which is what a mis-keyed or empty wire answer looks like.
func assertDimensionsPresent(t *testing.T, result realCheckResult) {
	t.Helper()
	if len(result.Dimensions) != len(groomingDimensions) {
		t.Fatalf("dimensions = %+v, want %v", result.Dimensions, groomingDimensions)
	}
	for i, want := range groomingDimensions {
		got := result.Dimensions[i]
		if got.Name != want {
			t.Errorf("dimension %d = %q, want %q", i, got.Name, want)
			continue
		}
		if got.Value <= 0 {
			t.Errorf("dimension %q = %v, want a real answer (empty or mis-keyed?)", got.Name, got.Value)
		}
	}
}

func TestRealGroomingSmoke(t *testing.T) {
	r := realGroomingRunner(t)
	project := firstField(t, r.run("project", "create", "Grooming Smoke"))

	t.Run("strong task is ready", func(t *testing.T) {
		taskID := firstField(t, r.run("task", "create", "-p", project,
			"-t", "Add --json output to ft foo", "-b", realStrongSpec))
		result := runRealCheck(t, r, taskID)
		t.Logf("strong: verdict=%s judge_confidence=%.3f dims=%v",
			result.Verdict, result.JudgeConfidence, result.Dimensions)

		assertDimensionsPresent(t, result)
		if result.Verdict != "ready" {
			t.Fatalf("strong task verdict = %q, want ready\nfindings: %+v", result.Verdict, result.Findings)
		}
		if len(result.Findings) != 0 {
			t.Fatalf("ready task must carry no findings: %+v", result.Findings)
		}
		if result.JudgeConfidence <= 0 {
			t.Fatalf("judge_confidence = %v, want the advisory holistic Noul recorded", result.JudgeConfidence)
		}
		// The holistic Noul is advisory: the four dimensions alone decide readiness,
		// so a low holistic still leaves the strong task ready.
		if result.JudgeConfidence < 0.70 {
			t.Logf("holistic Noul %.3f is below the 0.70 threshold yet the task is ready: it does not gate",
				result.JudgeConfidence)
		}
	})

	t.Run("weak task needs grooming", func(t *testing.T) {
		taskID := firstField(t, r.run("task", "create", "-p", project,
			"-t", "Make it better", "-b", realWeakSpec))
		result := runRealCheck(t, r, taskID)
		t.Logf("weak: verdict=%s judge_confidence=%.3f dims=%v findings=%+v",
			result.Verdict, result.JudgeConfidence, result.Dimensions, result.Findings)

		assertDimensionsPresent(t, result)
		if result.Verdict != "needs_grooming" && result.Verdict != "needs_human" {
			t.Fatalf("weak task verdict = %q, want needs_grooming or needs_human", result.Verdict)
		}
		if len(result.Findings) == 0 {
			t.Fatal("weak task must carry at least one finding")
		}
		named := false
		for _, finding := range result.Findings {
			if finding.Dimension == "holistic" {
				t.Errorf("holistic must never be a finding: %+v", finding)
			}
			if !contains(groomingDimensions, finding.Dimension) {
				t.Errorf("finding dimension %q is not a gating dimension", finding.Dimension)
			}
			if finding.Owner != "agent" && finding.Owner != "human" {
				t.Errorf("finding owner = %q, want agent or human", finding.Owner)
			}
			if finding.Aspect != "" {
				named = true
			}
		}
		if !named {
			t.Fatalf("weak task findings name no concrete gap: %+v", result.Findings)
		}
	})
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
