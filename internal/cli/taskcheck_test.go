package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/judge"
	"github.com/khoinguyen/factotum/pkg/judge/fake"
)

// readyJudge answers every grooming dimension as passing.
func readyJudge() *fake.Judge {
	return fake.New(map[string]judge.Answer{
		"scope_bounded":         {Probability: 0.9, Confidence: 0.9},
		"acceptance_verifiable": {Probability: 0.9, Confidence: 0.9},
		"decisions_author":      {Probability: 0.9, Confidence: 0.9},
		"dependencies_named":    {Probability: 0.9, Confidence: 0.9},
		"holistic":              {Probability: 0.55, Confidence: 0.9},
	})
}

func checkRunner(t *testing.T, j judge.Judge) (*runner, string, string) {
	t.Helper()
	r := newRunner(t)
	if j != nil {
		r.judge = j
	}
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "work", "-b", "a body"))
	return r, projectID, taskID
}

func TestTaskCheckReportsReady(t *testing.T) {
	r, _, taskID := checkRunner(t, readyJudge())
	out := r.run("task", "check", taskID)
	if !strings.Contains(out, "grooming: ready") {
		t.Fatalf("check output missing ready verdict:\n%s", out)
	}
	if !strings.Contains(out, "advisory; not a gate") {
		t.Fatalf("check output missing the advisory disclaimer:\n%s", out)
	}
}

func TestTaskCheckJSONIsStable(t *testing.T) {
	r, _, taskID := checkRunner(t, readyJudge())
	out := r.run("task", "check", taskID, "-o", "json")
	var results []map[string]any
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("check -o json: %v\n%s", err, out)
	}
	if len(results) != 1 || results[0]["check"] != "grooming" || results[0]["verdict"] != "ready" {
		t.Fatalf("check json = %v", results)
	}
}

func TestTaskCheckRestrictsToNamedCheck(t *testing.T) {
	r, _, taskID := checkRunner(t, readyJudge())
	out := r.run("task", "check", taskID, "--check", "grooming")
	if !strings.Contains(out, "grooming:") {
		t.Fatalf("--check grooming missing:\n%s", out)
	}
	err := r.runErr("task", "check", taskID, "--check", "nope")
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("unknown --check error = %v, want ErrUsage", err)
	}
}

func TestTaskCheckWithoutJudgeErrors(t *testing.T) {
	r, _, taskID := checkRunner(t, nil)
	err := r.runErr("task", "check", taskID)
	if err == nil {
		t.Fatal("check with no judge should error")
	}
	if !strings.Contains(err.Error(), "judge") {
		t.Fatalf("error = %v, want a judge-unavailable message", err)
	}
}

func TestCheckCacheIsHiddenFromDocCommands(t *testing.T) {
	r, _, taskID := checkRunner(t, readyJudge())
	r.run("task", "check", taskID)
	if out := r.run("doc", "list"); strings.Contains(out, "check:") {
		t.Fatalf("doc list leaked the check cache:\n%s", out)
	}
	if out := r.run("doc", "search", "grooming"); strings.Contains(out, "check:") {
		t.Fatalf("doc search leaked the check cache:\n%s", out)
	}
}

func TestTaskGetWithoutJudgeStillShowsNotChecked(t *testing.T) {
	r, _, taskID := checkRunner(t, nil)
	out := r.run("task", "get", taskID)
	if !strings.HasPrefix(out, "(todo) ") || !strings.Contains(out, "grooming: not checked") {
		t.Fatalf("task get with no judge changed:\n%s", out)
	}
}

func TestTaskCheckIsCachedAndGetMakesNoJudgeCall(t *testing.T) {
	j := readyJudge()
	r, _, taskID := checkRunner(t, j)
	r.run("task", "check", taskID)
	calls := len(j.Requests())

	out := r.run("task", "get", taskID)
	if !strings.Contains(out, "checks:") || !strings.Contains(out, "grooming: ready") {
		t.Fatalf("task get missing cached checks:\n%s", out)
	}
	if got := len(j.Requests()); got != calls {
		t.Fatalf("task get called the judge: calls %d -> %d", calls, got)
	}
}

func TestTaskGetShowsNotChecked(t *testing.T) {
	r, _, taskID := checkRunner(t, readyJudge())
	out := r.run("task", "get", taskID)
	if !strings.Contains(out, "grooming: not checked") {
		t.Fatalf("task get missing not-checked marker:\n%s", out)
	}
}

func TestTaskGetFieldsSelectsChecks(t *testing.T) {
	r, _, taskID := checkRunner(t, readyJudge())
	r.run("task", "check", taskID)
	out := r.run("task", "get", taskID, "--fields", "checks")
	if !strings.Contains(out, "checks:") {
		t.Fatalf("--fields checks did not select the block:\n%s", out)
	}
	if strings.Contains(out, "(todo)") {
		t.Fatalf("--fields checks leaked the human block:\n%s", out)
	}
}

func TestTaskDecideRequiresHumanActor(t *testing.T) {
	r, _, taskID := checkRunner(t, readyJudge())
	r.run("actor", "create", "--kind", "agent", "claude")
	r.run("--actor", "claude", "task", "check", taskID)
	err := r.runErr("--actor", "claude", "task", "decide", taskID, "--ready", "--reason", "ok")
	if err == nil || !strings.Contains(err.Error(), "human") {
		t.Fatalf("agent decide error = %v, want a human-actor error", err)
	}
}

func TestTaskDecideAndGetReportHumanDecision(t *testing.T) {
	r, _, taskID := checkRunner(t, readyJudge())
	r.run("actor", "create", "--kind", "human", "Khoi")
	r.run("--actor", "Khoi", "task", "decide", taskID, "--ready", "--reason", "shipped before")
	out := r.run("task", "get", taskID)
	if !strings.Contains(out, "ready (decided by Khoi") || !strings.Contains(out, "shipped before") {
		t.Fatalf("task get missing the human decision:\n%s", out)
	}
}

func TestTaskDecideClearRestoresJudgeVerdict(t *testing.T) {
	r, _, taskID := checkRunner(t, readyJudge())
	r.run("actor", "create", "--kind", "human", "Khoi")
	r.run("--actor", "Khoi", "task", "decide", taskID, "--ready", "--reason", "ok")
	r.run("--actor", "Khoi", "task", "decide", taskID, "--clear")
	out := r.run("task", "get", taskID)
	if strings.Contains(out, "decided by") {
		t.Fatalf("clear did not remove the decision:\n%s", out)
	}
	if !strings.Contains(out, "grooming: not checked") {
		t.Fatalf("clear should leave the judge verdict uncached:\n%s", out)
	}
}

func TestTaskDecideOverrideGoesStaleWhenBodyChanges(t *testing.T) {
	r, _, taskID := checkRunner(t, readyJudge())
	r.run("actor", "create", "--kind", "human", "Khoi")
	r.run("--actor", "Khoi", "task", "decide", taskID, "--ready", "--reason", "ok")
	r.run("task", "update", taskID, "--body", "a changed body")
	out := r.run("task", "get", taskID)
	if !strings.Contains(out, "stale human decision") {
		t.Fatalf("task get did not report a stale decision:\n%s", out)
	}
}

func TestTaskCheckReportsFindingsAndNotes(t *testing.T) {
	r, _, taskID := checkRunner(t, nil)
	r.judge = fake.New(map[string]judge.Answer{
		"scope_bounded":         {Probability: 0.3, Confidence: 0.9},
		"acceptance_verifiable": {Probability: 0.9, Confidence: 0.9},
		"decisions_author":      {Probability: 0.9, Confidence: 0.9},
		"dependencies_named":    {Probability: 0.9, Confidence: 0.9},
		"holistic":              {Probability: 0.5, Confidence: 0.9},
		"gap::scope_bounded":    {Choice: "non-goals missing", Confidence: 0.9},
	})
	out := r.run("task", "check", taskID)
	for _, want := range []string{"grooming: needs_grooming", "non-goals missing", "agent", "notes: 0 not considered", "advisory; not a gate"} {
		if !strings.Contains(out, want) {
			t.Fatalf("check report missing %q:\n%s", want, out)
		}
	}
}

func TestNoteCreateHintsTheBodyIsTheSpec(t *testing.T) {
	r, _, taskID := checkRunner(t, readyJudge())
	// A judge that reports needs grooming: one below-threshold dimension with a
	// named agent-owned gap.
	r.judge = fake.New(map[string]judge.Answer{
		"scope_bounded":         {Probability: 0.3, Confidence: 0.9},
		"acceptance_verifiable": {Probability: 0.9, Confidence: 0.9},
		"decisions_author":      {Probability: 0.9, Confidence: 0.9},
		"dependencies_named":    {Probability: 0.9, Confidence: 0.9},
		"holistic":              {Probability: 0.5, Confidence: 0.9},
		"gap::scope_bounded":    {Choice: "non-goals missing", Confidence: 0.9},
	})
	r.run("task", "check", taskID)
	_, stderr := r.runSplit("task", "note", "create", taskID, "-b", "a clarifying note")
	if !strings.Contains(stderr, "body is the spec") {
		t.Fatalf("note create missing the spec-vs-notes hint:\n%s", stderr)
	}
}
