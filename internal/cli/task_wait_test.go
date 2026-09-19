package cli

import (
	"strings"
	"testing"
)

func TestTaskWaitSetsAndClearsWaitingOn(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("actor", "create", "--kind", "human", "Khoi")
	r.run("actor", "create", "--kind", "agent", "claude")
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "blocked on review"))

	out := r.run("task", "wait", taskID, "--on", "Khoi", "--on", "claude")
	if !strings.Contains(out, "waiting_on:") || !strings.Contains(out, "khoi") || !strings.Contains(out, "claude") {
		t.Fatalf("task wait output = %q", out)
	}
	if got := r.run("task", "get", taskID); !strings.Contains(got, "waiting_on: (human) Khoi") {
		t.Fatalf("task get should show waiting_on:\n%s", got)
	}

	r.run("task", "wait", taskID, "--clear")
	if got := r.run("task", "get", taskID); strings.Contains(got, "waiting_on:") {
		t.Fatalf("waiting_on should be cleared:\n%s", got)
	}

	if err := r.runErr("task", "wait", taskID); err == nil {
		t.Fatal("task wait with neither --on nor --clear should error")
	}
	if err := r.runErr("task", "wait", taskID, "--on", "nobody"); err == nil {
		t.Fatal("task wait with an unknown actor should error")
	}
}
