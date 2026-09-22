package cli

import (
	"strings"
	"testing"
)

func TestTaskContextBundlesEverything(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	dep := firstField(t, r.run("task", "create", "-p", projectID, "-t", "the dependency"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "main", "--dep", dep))
	r.run("task", "note", "create", taskID, "-b", "a decision")
	r.run("doc", "create", "-p", projectID, "-t", "Memory one", "-k", "memory", "-b", "remember", "--task", taskID)

	out := r.run("task", "context", taskID)
	for _, want := range []string{"the dependency", "a decision", "Memory one", "events:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("task context missing %q:\n%s", want, out)
		}
	}

	jsonOut := r.run("task", "context", taskID, "-o", "json")
	for _, want := range []string{`"task"`, `"deps"`, `"notes"`, `"memory"`, `"events"`, "a decision", "Memory one"} {
		if !strings.Contains(jsonOut, want) {
			t.Fatalf("task context -o json missing %q:\n%s", want, jsonOut)
		}
	}
}

func TestTaskContextShowsMemoryBriefNotBody(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "main"))
	r.run("memory", "create", "-p", projectID, "-t", "Memory one", "--brief", "load me when testing", "-b", "SECRET BODY", "--task", taskID)

	out := r.run("task", "context", taskID)
	if !strings.Contains(out, "load me when testing") {
		t.Fatalf("task context should show the memory brief:\n%s", out)
	}
	if strings.Contains(out, "SECRET BODY") {
		t.Fatalf("task context should not inline the memory body:\n%s", out)
	}
}

func TestTaskContextFieldSelection(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "main"))
	r.run("task", "note", "create", taskID, "-b", "a decision")

	out := r.run("task", "context", taskID, "--fields", "task")
	if !strings.Contains(out, "task_id: "+taskID) {
		t.Fatalf("context should always include the task:\n%s", out)
	}
	if strings.Contains(out, "a decision") {
		t.Fatalf("--fields task should omit notes:\n%s", out)
	}
}
