package cli

import (
	"strings"
	"testing"
)

// TestTaskGetShowsOriginForIdeaPromotion proves a promoted task's provenance is
// queryable from `task get`, in text and as a machine field.
func TestTaskGetShowsOriginForIdeaPromotion(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "Spark"))
	r.run("actor", "create", "claude", "-k", "agent")
	taskID := firstField(t, r.run("idea", "promote", ideaID, "--actor", "claude", "--acceptance", "it works"))

	got := r.run("task", "get", taskID)
	if !strings.Contains(got, "origin: "+ideaID+" Spark") {
		t.Fatalf("task get should show the origin idea with its title:\n%s", got)
	}

	jsonOut := r.run("task", "get", taskID, "-o", "json")
	if !strings.Contains(jsonOut, `"origin": "`+ideaID+`"`) {
		t.Fatalf("task get -o json should carry the origin:\n%s", jsonOut)
	}
	if !strings.Contains(jsonOut, `"origin_title": "Spark"`) {
		t.Fatalf("task get -o json should carry the origin title:\n%s", jsonOut)
	}

	yamlOut := r.run("task", "get", taskID, "-o", "yaml")
	if !strings.Contains(yamlOut, "origin: "+ideaID) {
		t.Fatalf("task get -o yaml should carry the origin:\n%s", yamlOut)
	}
	if !strings.Contains(yamlOut, "origin_title: Spark") {
		t.Fatalf("task get -o yaml should carry the origin title:\n%s", yamlOut)
	}
}

// TestTaskGetPrintsOriginBeforeDeps pins the text order: provenance reads
// before the work edges, so a reader learns where the task came from before
// what it waits on.
func TestTaskGetPrintsOriginBeforeDeps(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "Spark"))
	r.run("actor", "create", "claude", "-k", "agent")
	taskID := firstField(t, r.run("idea", "promote", ideaID, "--actor", "claude", "--acceptance", "it works"))
	blockerID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "blocker"))
	r.run("task", "dep", "create", taskID, blockerID)

	got := r.run("task", "get", taskID)
	originAt := strings.Index(got, "origin: "+ideaID)
	depsAt := strings.Index(got, "deps: "+blockerID)
	if originAt < 0 || depsAt < 0 {
		t.Fatalf("task get should print both the origin and its blocking dep:\n%s", got)
	}
	if originAt > depsAt {
		t.Fatalf("origin must print before deps (origin at %d, deps at %d):\n%s", originAt, depsAt, got)
	}
}

// TestTaskGetShowsOriginForBugTriage proves triage links a bug as the origin,
// exactly like an idea.
func TestTaskGetShowsOriginForBugTriage(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	bugID := firstField(t, r.run("bug", "create", "-p", projectID, "-t", "Crash"))
	r.run("actor", "create", "claude", "-k", "agent")
	taskID := firstField(t, r.run("bug", "triage", bugID, "--actor", "claude", "--acceptance", "it stops crashing"))

	got := r.run("task", "get", taskID)
	if !strings.Contains(got, "origin: "+bugID+" Crash") {
		t.Fatalf("task get should show the origin bug with its title:\n%s", got)
	}

	jsonOut := r.run("task", "get", taskID, "-o", "json")
	if !strings.Contains(jsonOut, `"origin": "`+bugID+`"`) {
		t.Fatalf("task get -o json should carry the origin bug:\n%s", jsonOut)
	}
}

// TestTaskGetOmitsOriginWhenNone proves a plain task and a task with only real
// dependencies carry no origin.
func TestTaskGetOmitsOriginWhenNone(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	blocker := firstField(t, r.run("task", "create", "-p", projectID, "-t", "blocker"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "work", "--dep", blocker))

	if got := r.run("task", "get", taskID); strings.Contains(got, "origin:") {
		t.Fatalf("a task with no capture origin must not print one:\n%s", got)
	}
	if got := r.run("task", "get", taskID, "-o", "json"); strings.Contains(got, `"origin"`) {
		t.Fatalf("a task with no capture origin must not carry one:\n%s", got)
	}
	if got := r.run("task", "get", taskID, "-o", "yaml"); strings.Contains(got, "origin:") || strings.Contains(got, "origin_title:") {
		t.Fatalf("a task with no capture origin must not carry one:\n%s", got)
	}
}
