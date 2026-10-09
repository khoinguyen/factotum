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
	r.run("actor", "create", "claude", "-k", "agent")

	out := r.run("task", "promote", ideaID, "--actor", "claude", "--acceptance", "it works")
	taskID := firstField(t, out)
	if taskID == ideaID || !strings.Contains(out, "kind: task") {
		t.Fatalf("promote should create a task:\n%s", out)
	}
	got := r.run("task", "get", taskID)
	if !strings.Contains(got, "groomed: true") || !strings.Contains(got, "acceptance: it works") {
		t.Fatalf("promote should create a groomed task with the acceptance criterion:\n%s", got)
	}
	if !strings.Contains(got, "assignee: (agent) claude") {
		t.Fatalf("promote should assign the task to the agent:\n%s", got)
	}

	// The idea is retained as history. It gates nothing, so it must not print a
	// graph "unblocks" line for the promoted task.
	if got := r.run("task", "get", ideaID); !strings.Contains(got, "kind: idea") {
		t.Fatalf("the idea should be kept after promotion:\n%s", got)
	} else if strings.Contains(got, "unblocks:") {
		t.Fatalf("an idea gates no work and should not print unblocks:\n%s", got)
	}

	// The new task is linked back to the idea as its origin, not as a blocking
	// dependency: an idea gates nothing.
	if got := r.run("task", "get", taskID); !strings.Contains(got, "origin: "+ideaID) {
		t.Fatalf("promoted task should record the idea as its origin:\n%s", got)
	} else if deps := fieldLine(got, "deps"); strings.Contains(deps, ideaID) {
		t.Fatalf("an idea must not read as a blocking dependency:\n%s", got)
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

func TestTaskKindMutationInvolvingIdeaRejected(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ideaID := firstField(t, r.run("task", "create", "-p", projectID, "-k", "idea", "-t", "spark"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "work"))

	if err := r.runErr("task", "update", taskID, "-k", "idea"); err == nil {
		t.Fatal("demoting a task to an idea via update should fail")
	}
	if err := r.runErr("task", "update", ideaID, "-k", "task"); err == nil {
		t.Fatal("turning an idea into a task via update should fail; use task promote")
	}

	help := r.run("task", "update", "--help")
	if strings.Contains(help, "task, milestone, or idea") {
		t.Fatalf("update help must not advertise idea as an accepted value:\n%s", help)
	}
	// Backticks would make pflag take the quoted word as the value placeholder,
	// clobbering "string"; the help must stay plain text.
	if !strings.Contains(help, "-k, --kind string") || !strings.Contains(help, "task kind: task or milestone; an idea or bug is turned into a task with ft task promote") {
		t.Fatalf("update help missing the kind flag description:\n%s", help)
	}
}
