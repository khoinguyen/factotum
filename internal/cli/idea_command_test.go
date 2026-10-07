package cli

import (
	"strings"
	"testing"
)

func TestIdeaCommandCreateAndGet(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))

	out := r.run("idea", "create", "-p", projectID, "-t", "a spark", "-b", "context")
	ideaID := firstField(t, out)
	if !strings.Contains(out, "kind: idea") {
		t.Fatalf("idea create should report kind: idea:\n%s", out)
	}

	got := r.run("idea", "get", ideaID)
	for _, want := range []string{"(todo) " + ideaID + ": a spark", "kind: idea", "project: " + projectID, "context"} {
		if !strings.Contains(got, want) {
			t.Fatalf("idea get missing %q:\n%s", want, got)
		}
	}

	if next := r.run("task", "next", "-p", projectID); strings.Contains(next, ideaID) {
		t.Fatalf("an idea must not enter the ready set:\n%s", next)
	}
}

func TestIdeaCommandCreateRequiresProject(t *testing.T) {
	r := newRunner(t)
	if err := r.runErr("idea", "create", "-t", "homeless"); err == nil {
		t.Fatal("idea create without a project should fail")
	}
}

func TestIdeaCommandListParityWithTaskList(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "spark"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "work"))

	list := r.run("idea", "list", "-p", projectID)
	if !strings.Contains(list, ideaID) {
		t.Fatalf("idea list should include %s:\n%s", ideaID, list)
	}
	if strings.Contains(list, taskID) {
		t.Fatalf("idea list must exclude the task %s:\n%s", taskID, list)
	}

	got := r.run("idea", "list", "-p", projectID, "-o", "json")
	want := r.run("task", "list", "-p", projectID, "-k", "idea", "-o", "json")
	if got != want {
		t.Fatalf("idea list JSON differs from task list -k idea:\nidea:\n%s\ntask:\n%s", got, want)
	}
}

func TestIdeaCommandSearchParityWithTaskSearch(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "spark"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "spark work"))

	hits := r.run("idea", "search", "spark", "-p", projectID)
	if !strings.Contains(hits, ideaID) {
		t.Fatalf("idea search should include %s:\n%s", ideaID, hits)
	}
	if strings.Contains(hits, taskID) {
		t.Fatalf("idea search must exclude the task %s:\n%s", taskID, hits)
	}

	got := r.run("idea", "search", "spark", "-p", projectID, "-o", "json")
	want := r.run("task", "search", "spark", "-p", projectID, "-k", "idea", "-o", "json")
	if got != want {
		t.Fatalf("idea search JSON differs from task search -k idea:\nidea:\n%s\ntask:\n%s", got, want)
	}
}

func TestIdeaCommandPromoteLinksOrigin(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "a spark", "-b", "context"))

	out := r.run("idea", "promote", ideaID)
	taskID := firstField(t, out)
	if taskID == ideaID || !strings.Contains(out, "kind: task") || !strings.Contains(out, "from: "+ideaID) {
		t.Fatalf("idea promote should create a task from the idea:\n%s", out)
	}

	if got := r.run("task", "get", taskID); !strings.Contains(got, "deps: "+ideaID) {
		t.Fatalf("promoted task should record the idea as its origin:\n%s", got)
	}
	if got := r.run("idea", "get", ideaID); !strings.Contains(got, "kind: idea") {
		t.Fatalf("the idea must survive promotion as history:\n%s", got)
	}
	if next := r.run("task", "next", "-p", projectID); !strings.Contains(next, taskID) {
		t.Fatalf("the promoted task should be ready:\n%s", next)
	}
}

func TestIdeaCommandIdeasRejectExecutableOps(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "spark"))
	r.run("actor", "create", "bob", "-k", "agent")

	if err := r.runErr("task", "assign", ideaID, "--actor", "bob"); err == nil {
		t.Fatal("an idea must not be assignable")
	}
	if err := r.runErr("task", "update", ideaID, "--groomed", "--acceptance", "x"); err == nil {
		t.Fatal("an idea must not be groomable")
	}
	if err := r.runErr("task", "start", ideaID); err == nil {
		t.Fatal("an idea must not start")
	}
}

func TestIdeaCommandGetHintsPromote(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "spark"))

	_, stderr := r.runSplit("idea", "get", ideaID)
	if !strings.Contains(stderr, "ft idea promote "+ideaID) {
		t.Fatalf("idea get should suggest promotion, not task actions:\n%s", stderr)
	}
}

func TestIdeaCommandSurface(t *testing.T) {
	r := newRunner(t)
	help := r.run("idea", "--help")
	for _, verb := range []string{"create", "list", "get", "promote", "search"} {
		if !strings.Contains(help, verb) {
			t.Fatalf("ft idea help should list the %q verb:\n%s", verb, help)
		}
	}
}
