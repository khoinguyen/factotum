package cli

import (
	"strings"
	"testing"
)

func TestTaskClaimClaimsDistinctReadyTasks(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("actor", "create", "--kind", "agent", "claude")
	a := firstField(t, r.run("task", "create", "-p", projectID, "-t", "a"))
	b := firstField(t, r.run("task", "create", "-p", projectID, "-t", "b"))

	first := r.run("task", "claim", "--for", "claude", "-p", projectID)
	firstID := firstField(t, first)
	if firstID != a && firstID != b {
		t.Fatalf("claim returned unexpected task %q:\n%s", firstID, first)
	}
	if !strings.Contains(first, "assignee: claude") {
		t.Fatalf("claim should self-assign:\n%s", first)
	}
	if !strings.Contains(first, "status: todo") {
		t.Fatalf("claim without --start should leave the task in todo:\n%s", first)
	}

	second := firstField(t, r.run("task", "claim", "--for", "claude", "-p", projectID))
	if second == firstID {
		t.Fatalf("second claim must pick a different task, both were %s", second)
	}
	if err := r.runErr("task", "claim", "--for", "claude", "-p", projectID); err == nil {
		t.Fatal("claiming with no ready task left should error")
	}
}

func TestTaskClaimStartBeginsWork(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("actor", "create", "--kind", "agent", "claude")
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "a"))

	out := r.run("task", "claim", "--for", "claude", "--start", "-p", projectID)
	if !strings.Contains(out, "status: in_progress") {
		t.Fatalf("claim --start should report in_progress:\n%s", out)
	}
	if !strings.Contains(out, "assignee: claude") {
		t.Fatalf("claim --start should self-assign:\n%s", out)
	}

	get := r.run("task", "get", taskID, "-o", "json")
	if !strings.Contains(get, `"status": "in_progress"`) {
		t.Fatalf("structured output should show in_progress:\n%s", get)
	}
}

func TestTaskClaimRequiresActor(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("task", "create", "-p", projectID, "-t", "a")

	if err := r.runErr("task", "claim", "-p", projectID); err == nil {
		t.Fatal("claim without --for and without default_actor should error")
	}
}
