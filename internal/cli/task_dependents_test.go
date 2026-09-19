package cli

import (
	"strings"
	"testing"
)

func TestTaskGetShowsDependents(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	dep := firstField(t, r.run("task", "create", "-p", projectID, "-t", "dep"))
	blocked := firstField(t, r.run("task", "create", "-p", projectID, "-t", "blocked", "--dep", dep))

	text := r.run("task", "get", dep)
	if !strings.Contains(text, "unblocks: "+blocked) {
		t.Fatalf("task get should list dependents:\n%s", text)
	}

	jsonOut := r.run("task", "get", dep, "-o", "json")
	if !strings.Contains(jsonOut, "\"dependents\"") || !strings.Contains(jsonOut, blocked) {
		t.Fatalf("task get -o json should include dependents:\n%s", jsonOut)
	}

	if got := r.run("task", "get", blocked); strings.Contains(got, "unblocks:") {
		t.Fatalf("a task without dependents should not print unblocks:\n%s", got)
	}
}
