package cli

import (
	"strings"
	"testing"
)

// completedFeature creates a project with one idea whose single task is done,
// leaving the feature ready for QA.
func completedFeature(t *testing.T) (*runner, string, string, string) {
	t.Helper()
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "a feature"))
	r.run("actor", "create", "claude", "-k", "agent")
	taskID := firstField(t, r.run("idea", "promote", ideaID, "--actor", "claude", "--acceptance", "it works"))
	r.run("task", "done", taskID)
	return r, projectID, ideaID, taskID
}

func TestQAReadyListsFeatureWithEveryTaskDone(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "a feature"))
	r.run("actor", "create", "claude", "-k", "agent")
	taskID := firstField(t, r.run("idea", "promote", ideaID, "--actor", "claude", "--acceptance", "it works"))

	if out := r.run("qa", "ready", "-p", projectID); strings.Contains(out, ideaID) {
		t.Fatalf("qa ready listed a feature with an open task:\n%s", out)
	}
	r.run("task", "done", taskID)
	out := r.run("qa", "ready", "-p", projectID)
	if !strings.Contains(out, ideaID) {
		t.Fatalf("qa ready should list the completed feature %s:\n%s", ideaID, out)
	}
}

func TestQARecordPassUnlocksAcceptingTheCapture(t *testing.T) {
	r, projectID, ideaID, _ := completedFeature(t)

	if err := r.runErr("task", "done", ideaID); err == nil {
		t.Fatal("accepting a capture before QA should fail")
	}

	out := r.run("qa", "record", ideaID, "--verdict", "pass", "-b", "verified end to end")
	if !strings.Contains(out, "capture: "+ideaID) || !strings.Contains(out, "verdict: pass") {
		t.Fatalf("qa record pass output:\n%s", out)
	}
	if got := r.run("done", ideaID); !strings.Contains(got, "status: done") {
		t.Fatalf("accepting after a pass should succeed:\n%s", got)
	}
	if out := r.run("qa", "ready", "-p", projectID); strings.Contains(out, ideaID) {
		t.Fatalf("a passed feature should not stay in qa ready:\n%s", out)
	}
}

func TestQARecordPassRefusesOpenTask(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "a feature"))
	r.run("actor", "create", "claude", "-k", "agent")
	r.run("idea", "promote", ideaID, "--actor", "claude", "--acceptance", "it works")

	if err := r.runErr("qa", "record", ideaID, "--verdict", "pass", "-b", "premature"); err == nil {
		t.Fatal("a passing QA with an open task should fail")
	}
}

func TestQARecordRequiresAVerdict(t *testing.T) {
	r, _, ideaID, _ := completedFeature(t)
	if err := r.runErr("qa", "record", ideaID); err == nil {
		t.Fatal("qa record without a verdict should fail")
	}
}
