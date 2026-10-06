package cli

import (
	"strings"
	"testing"
)

func TestIdeaKindLifecycle(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ideaID := firstField(t, r.run("task", "create", "-p", projectID, "-k", "idea", "-t", "a spark"))

	if got := r.run("task", "get", ideaID); !strings.Contains(got, "kind: idea") {
		t.Fatalf("task get should show kind: idea:\n%s", got)
	}

	if list := r.run("task", "list", "-p", projectID, "-k", "idea"); !strings.Contains(list, ideaID) {
		t.Fatalf("task list -k idea should include %s:\n%s", ideaID, list)
	}
	if list := r.run("task", "list", "-p", projectID, "-k", "task"); strings.Contains(list, ideaID) {
		t.Fatalf("task list -k task should exclude the idea %s:\n%s", ideaID, list)
	}

	if next := r.run("task", "next", "-p", projectID); strings.Contains(next, ideaID) {
		t.Fatalf("task next must exclude the non-executable idea:\n%s", next)
	}

	if err := r.runErr("task", "start", ideaID); err == nil {
		t.Fatal("task start on an idea should fail: ideas have no in_progress")
	}
}

func TestTaskPromoteCreatesLinkedTask(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ideaID := firstField(t, r.run("task", "create", "-p", projectID, "-k", "idea", "-t", "a spark", "-b", "context"))

	out := r.run("task", "promote", ideaID)
	taskID := firstField(t, out)
	if taskID == ideaID || !strings.Contains(out, "kind: task") {
		t.Fatalf("promote should create a task:\n%s", out)
	}

	// The idea is retained as history.
	if got := r.run("task", "get", ideaID); !strings.Contains(got, "kind: idea") {
		t.Fatalf("the idea should be kept after promotion:\n%s", got)
	}

	// The new task is linked back to the idea as its origin.
	if got := r.run("task", "get", taskID); !strings.Contains(got, "deps: "+ideaID) {
		t.Fatalf("promoted task should depend on its origin idea:\n%s", got)
	}

	// The promoted task is now executable work.
	if next := r.run("task", "next", "-p", projectID); !strings.Contains(next, taskID) {
		t.Fatalf("promoted task should be ready:\n%s", next)
	}
}

func TestTaskPromoteRejectsTask(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "work"))
	if err := r.runErr("task", "promote", taskID); err == nil {
		t.Fatal("promoting a task that is not an idea should fail")
	}
}
