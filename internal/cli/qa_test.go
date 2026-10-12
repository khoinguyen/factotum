package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// inboxEntries decodes `ft msg inbox --for <actor> -o json` for a project.
func inboxEntries(t *testing.T, r *runner, projectID, actor string) []messageEntry {
	t.Helper()
	var entries []messageEntry
	if err := json.Unmarshal([]byte(r.run("msg", "inbox", "-p", projectID, "--for", actor, "-o", "json")), &entries); err != nil {
		t.Fatalf("parse msg inbox json: %v", err)
	}
	return entries
}

// TestResolvingLastTaskRemindsTheChiefToPlanQA exercises the reminder end to
// end: finishing a capture's last task queues an ft msg to the actor that
// performed the transition, while finishing a non-final task does not.
func TestResolvingLastTaskRemindsTheChiefToPlanQA(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "a feature"))
	r.run("actor", "create", "claude", "-k", "agent")
	first := firstField(t, r.run("idea", "promote", ideaID, "--actor", "claude", "--acceptance", "it works"))
	second := firstField(t, r.run("idea", "promote", ideaID, "--actor", "claude", "--acceptance", "it works"))

	r.run("task", "done", first, "--actor", "chief")
	if entries := inboxEntries(t, r, projectID, "chief"); len(entries) != 0 {
		t.Fatalf("reminder before the capture is complete: %+v", entries)
	}

	r.run("task", "done", second, "--actor", "chief")
	entries := inboxEntries(t, r, projectID, "chief")
	if len(entries) != 1 || entries[0].To != "actor:chief" {
		t.Fatalf("inbox for chief = %+v, want exactly one reminder to actor:chief", entries)
	}
	body := r.run("msg", "get", entries[0].ID)
	if !strings.Contains(body, ideaID) || !strings.Contains(body, "ft qa") {
		t.Fatalf("reminder body:\n%s\nwant it to name %s and point at ft qa", body, ideaID)
	}
}

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

func TestQARecordFailRevokesAnEarlierPass(t *testing.T) {
	r, projectID, ideaID, _ := completedFeature(t)
	r.run("qa", "record", ideaID, "--verdict", "pass", "-b", "green")
	if out := r.run("qa", "ready", "-p", projectID); strings.Contains(out, ideaID) {
		t.Fatalf("a passed feature must not be listed as ready:\n%s", out)
	}
	r.run("qa", "record", ideaID, "--verdict", "fail", "-b", "regressed")
	if out := r.run("qa", "ready", "-p", projectID); !strings.Contains(out, ideaID) {
		t.Fatalf("a revoking fail must return the feature to the QA queue:\n%s", out)
	}
	if err := r.runErr("done", ideaID); err == nil {
		t.Fatal("accepting after a revoking fail should fail")
	}
}

func TestQAReadyRequiresDoneNotReview(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "a feature"))
	r.run("actor", "create", "claude", "-k", "agent")
	taskID := firstField(t, r.run("idea", "promote", ideaID, "--actor", "claude", "--acceptance", "it works"))
	r.run("task", "review", taskID)

	if out := r.run("qa", "ready", "-p", projectID); strings.Contains(out, ideaID) {
		t.Fatalf("a task only in review must not make the feature QA-ready:\n%s", out)
	}
	if err := r.runErr("qa", "record", ideaID, "--verdict", "pass", "-b", "premature"); err == nil {
		t.Fatal("a passing QA with a task in review should fail")
	}
}
