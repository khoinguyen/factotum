package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestTaskSnoozeUntilDefersAndUnsnoozes(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "park me"))

	r.run("task", "snooze", taskID, "--until", "2030-01-01")
	if out := r.run("task", "next", "--project", projectID); strings.Contains(out, taskID) {
		t.Fatalf("snoozed task should not be ready:\n%s", out)
	}
	if get := r.run("task", "get", taskID); !strings.Contains(get, "snoozed: until 2030-01-01T00:00:00Z") {
		t.Fatalf("task get missing snooze:\n%s", get)
	}

	r.run("task", "unsnooze", taskID)
	if out := r.run("task", "next", "--project", projectID); !strings.Contains(out, taskID) {
		t.Fatalf("unsnoozed task should be ready:\n%s", out)
	}
}

func TestTaskSnoozeIndefinite(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "park me"))

	r.run("task", "snooze", taskID, "--indefinite")
	if out := r.run("task", "next", "--project", projectID); strings.Contains(out, taskID) {
		t.Fatalf("indefinitely snoozed task should not be ready:\n%s", out)
	}
	r.run("task", "unsnooze", taskID)
	if out := r.run("task", "next", "--project", projectID); !strings.Contains(out, taskID) {
		t.Fatalf("unsnoozed task should be ready:\n%s", out)
	}
}

func TestTaskSnoozeUntilTask(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	blocker := firstField(t, r.run("task", "create", "-p", projectID, "-t", "blocker"))
	parked := firstField(t, r.run("task", "create", "-p", projectID, "-t", "parked"))

	r.run("task", "snooze", parked, "--until-task", blocker)
	next := r.run("task", "next", "--project", projectID)
	if strings.Contains(next, parked) || !strings.Contains(next, blocker) {
		t.Fatalf("parked task should wait on blocker:\n%s", next)
	}

	r.run("task", "done", blocker)
	if next := r.run("task", "next", "--project", projectID); !strings.Contains(next, parked) {
		t.Fatalf("parked task should wake when the blocker resolves:\n%s", next)
	}
}

func TestTaskSnoozeRequiresOneCondition(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "park me"))

	err := r.runErr("task", "snooze", taskID)
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("snooze without a condition error = %v, want ErrUsage", err)
	}
}

func TestTaskGetJSONIncludesSnooze(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "park me"))
	r.run("task", "snooze", taskID, "--until", "2030-01-01")

	out := r.run("task", "get", taskID, "-o", "json")
	var doc struct {
		Snooze *struct {
			Until string `json:"until"`
		} `json:"snooze"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("task get json: %v\n%s", err, out)
	}
	if doc.Snooze == nil || doc.Snooze.Until != "2030-01-01T00:00:00Z" {
		t.Fatalf("snooze = %+v, want until 2030-01-01", doc.Snooze)
	}
}
