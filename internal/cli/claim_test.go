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

	second := firstField(t, r.run("task", "claim", "--for", "claude", "-p", projectID))
	if second == firstID {
		t.Fatalf("second claim must pick a different task, both were %s", second)
	}
	if err := r.runErr("task", "claim", "--for", "claude", "-p", projectID); err == nil {
		t.Fatal("claiming with no ready task left should error")
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
