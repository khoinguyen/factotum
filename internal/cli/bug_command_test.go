package cli

import (
	"strings"
	"testing"
)

func TestBugCommandCreateAndGet(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))

	out := r.run("bug", "create", "-p", projectID, "-t", "it crashes", "-b", "context")
	bugID := firstField(t, out)
	if !strings.Contains(out, "kind: bug") {
		t.Fatalf("bug create should report kind: bug:\n%s", out)
	}

	got := r.run("bug", "get", bugID)
	for _, want := range []string{"(todo) " + bugID + ": it crashes", "kind: bug", "project: " + projectID, "context"} {
		if !strings.Contains(got, want) {
			t.Fatalf("bug get missing %q:\n%s", want, got)
		}
	}

	if next := r.run("task", "next", "-p", projectID); strings.Contains(next, bugID) {
		t.Fatalf("a bug must not enter the ready set:\n%s", next)
	}
}

func TestBugCommandCreateRequiresProject(t *testing.T) {
	r := newRunner(t)
	if err := r.runErr("bug", "create", "-t", "homeless"); err == nil {
		t.Fatal("bug create without a project should fail")
	}
}

func TestBugCommandListParityWithTaskList(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	bugID := firstField(t, r.run("bug", "create", "-p", projectID, "-t", "it crashes"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "work"))

	list := r.run("bug", "list", "-p", projectID)
	if !strings.Contains(list, bugID) {
		t.Fatalf("bug list should include %s:\n%s", bugID, list)
	}
	if strings.Contains(list, taskID) {
		t.Fatalf("bug list must exclude the task %s:\n%s", taskID, list)
	}

	got := r.run("bug", "list", "-p", projectID, "-o", "json")
	want := r.run("task", "list", "-p", projectID, "-k", "bug", "-o", "json")
	if got != want {
		t.Fatalf("bug list JSON differs from task list -k bug:\nbug:\n%s\ntask:\n%s", got, want)
	}
}

func TestBugCommandSearchParityWithTaskSearch(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	bugID := firstField(t, r.run("bug", "create", "-p", projectID, "-t", "it crashes"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "it crashes at work"))

	hits := r.run("bug", "search", "crashes", "-p", projectID)
	if !strings.Contains(hits, bugID) {
		t.Fatalf("bug search should include %s:\n%s", bugID, hits)
	}
	if strings.Contains(hits, taskID) {
		t.Fatalf("bug search must exclude the task %s:\n%s", taskID, hits)
	}

	got := r.run("bug", "search", "crashes", "-p", projectID, "-o", "json")
	want := r.run("task", "search", "crashes", "-p", projectID, "-k", "bug", "-o", "json")
	if got != want {
		t.Fatalf("bug search JSON differs from task search -k bug:\nbug:\n%s\ntask:\n%s", got, want)
	}
}

func TestBugCommandTriageLinksOrigin(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	bugID := firstField(t, r.run("bug", "create", "-p", projectID, "-t", "it crashes", "-b", "context"))

	out := r.run("bug", "triage", bugID)
	taskID := firstField(t, out)
	if taskID == bugID || !strings.Contains(out, "kind: task") || !strings.Contains(out, "from: "+bugID) {
		t.Fatalf("bug triage should create a task from the bug:\n%s", out)
	}

	if got := r.run("task", "get", taskID); !strings.Contains(got, "deps: "+bugID) {
		t.Fatalf("triaged task should record the bug as its origin:\n%s", got)
	}
	if got := r.run("bug", "get", bugID); !strings.Contains(got, "kind: bug") {
		t.Fatalf("the bug must survive triage as history:\n%s", got)
	}
	if next := r.run("task", "next", "-p", projectID); !strings.Contains(next, taskID) {
		t.Fatalf("the triaged task should be ready:\n%s", next)
	}
}

func TestTaskPromoteAcceptsBug(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	bugID := firstField(t, r.run("bug", "create", "-p", projectID, "-t", "it crashes"))

	out := r.run("task", "promote", bugID)
	taskID := firstField(t, out)
	if !strings.Contains(out, "from: "+bugID) {
		t.Fatalf("task promote should accept a bug capture:\n%s", out)
	}
	if got := r.run("task", "get", taskID); !strings.Contains(got, "deps: "+bugID) {
		t.Fatalf("promoted task should record the bug as its origin:\n%s", got)
	}
}

func TestBugCommandBugsRejectExecutableOps(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	bugID := firstField(t, r.run("bug", "create", "-p", projectID, "-t", "it crashes"))
	r.run("actor", "create", "bob", "-k", "agent")

	if err := r.runErr("task", "assign", bugID, "--actor", "bob"); err == nil {
		t.Fatal("a bug must not be assignable")
	}
	if err := r.runErr("task", "update", bugID, "--groomed", "--acceptance", "x"); err == nil {
		t.Fatal("a bug must not be groomable")
	}
	if err := r.runErr("task", "start", bugID); err == nil {
		t.Fatal("a bug must not start")
	}
}

func TestBugCommandGetHintsTriage(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	bugID := firstField(t, r.run("bug", "create", "-p", projectID, "-t", "it crashes"))

	_, stderr := r.runSplit("bug", "get", bugID)
	if !strings.Contains(stderr, "ft bug triage "+bugID) {
		t.Fatalf("bug get should suggest triage, not task actions:\n%s", stderr)
	}
}

func TestBugCommandSurface(t *testing.T) {
	r := newRunner(t)
	help := r.run("bug", "--help")
	for _, verb := range []string{"create", "list", "get", "triage", "search"} {
		if !strings.Contains(help, verb) {
			t.Fatalf("ft bug help should list the %q verb:\n%s", verb, help)
		}
	}
}
