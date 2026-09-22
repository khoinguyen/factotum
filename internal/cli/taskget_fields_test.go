package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestTaskGetFieldsProjectsText(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "work", "--priority", "7"))

	out := r.run("task", "get", taskID, "--fields", "title,status")
	if !strings.Contains(out, "title: work") || !strings.Contains(out, "status: todo") {
		t.Fatalf("projection missing requested fields:\n%s", out)
	}
	for _, unwanted := range []string{"project:", "priority:", "(todo)"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("projection leaked %q:\n%s", unwanted, out)
		}
	}
}

func TestTaskGetFieldsProjectsJSON(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "work", "--priority", "7"))

	out := r.run("task", "get", taskID, "--fields", "title,priority", "-o", "json")
	var projected map[string]any
	if err := json.Unmarshal([]byte(out), &projected); err != nil {
		t.Fatalf("task get --fields json: %v\n%s", err, out)
	}
	if len(projected) != 2 || projected["title"] != "work" || projected["priority"] != float64(7) {
		t.Fatalf("projection = %v, want title and priority only", projected)
	}
}

func TestTaskGetFieldsRejectsUnknown(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "work"))

	err := r.runErr("task", "get", taskID, "--fields", "nope")
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("unknown field error = %v, want ErrUsage", err)
	}
}

func TestTaskGetFieldsProjectsNotReadyJSON(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	blocker := firstField(t, r.run("task", "create", "-p", projectID, "-t", "blocker"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "work", "--dep", blocker))

	out := r.run("task", "get", taskID, "--fields", "not_ready", "-o", "json")
	var projected map[string]struct {
		ReasonCode string `json:"reason_code"`
		Detail     string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(out), &projected); err != nil {
		t.Fatalf("task get --fields not_ready json: %v\n%s", err, out)
	}
	got, ok := projected["not_ready"]
	if !ok {
		t.Fatalf("projection missing not_ready:\n%s", out)
	}
	if got.ReasonCode != "dep_unresolved" || got.Detail != blocker {
		t.Fatalf("not_ready = %+v, want dep_unresolved/%s", got, blocker)
	}
}

func TestTaskGetFieldsProjectsNotReadyText(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	blocker := firstField(t, r.run("task", "create", "-p", projectID, "-t", "blocker"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "work", "--dep", blocker))

	out := r.run("task", "get", taskID, "--fields", "not_ready")
	want := "not_ready: dep_unresolved: " + blocker
	if !strings.Contains(out, want) {
		t.Fatalf("projection missing %q:\n%s", want, out)
	}
}

func TestTaskGetWithoutFieldsIsUnchanged(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "work"))

	out := r.run("task", "get", taskID)
	if !strings.HasPrefix(out, "(todo) "+taskID+": work") {
		t.Fatalf("default task get changed:\n%s", out)
	}
}
