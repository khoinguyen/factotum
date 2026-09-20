package cli

import (
	"strings"
	"testing"
)

func TestTaskNextForWithNoMatchesReturnsNone(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("actor", "create", "--kind", "agent", "claude")
	r.run("actor", "create", "--kind", "agent", "other")
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "a"))
	r.run("task", "assign", taskID, "--actor", "other")

	out := r.run("task", "next", "--for", "claude", "-p", projectID)
	if strings.Contains(out, taskID) {
		t.Fatalf("--for with no matching tasks should return none, got:\n%s", out)
	}
}

func TestTaskNextLabelWithNoMatchesReturnsNone(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "a"))

	out := r.run("task", "next", "--label", "missing", "-p", projectID)
	if strings.Contains(out, taskID) {
		t.Fatalf("--label with no matching tasks should return none, got:\n%s", out)
	}
}
