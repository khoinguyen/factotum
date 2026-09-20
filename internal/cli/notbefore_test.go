package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTaskSetNotBeforeDefersFromNext(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "--project", projectID, "--title", "soak"))

	r.run("task", "set", taskID, "not_before=2030-01-01")

	if out := r.run("task", "next", "--project", projectID); strings.Contains(out, taskID) {
		t.Fatalf("deferred task should not be ready:\n%s", out)
	}
	get := r.run("task", "get", taskID)
	if !strings.Contains(get, "not_before: 2030-01-01T00:00:00Z") {
		t.Fatalf("task get missing not_before:\n%s", get)
	}

	r.run("task", "set", taskID, "not_before=")
	if out := r.run("task", "next", "--project", projectID); !strings.Contains(out, taskID) {
		t.Fatalf("cleared task should be ready:\n%s", out)
	}
}

func TestTaskGetJSONIncludesNotBefore(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "--project", projectID, "--title", "soak"))
	r.run("task", "set", taskID, "not_before=2030-01-01")

	out := r.run("task", "get", taskID, "-o", "json")
	var doc struct {
		NotBefore string `json:"not_before"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("task get json: %v\n%s", err, out)
	}
	if doc.NotBefore != "2030-01-01T00:00:00Z" {
		t.Fatalf("not_before = %q, want 2030-01-01T00:00:00Z", doc.NotBefore)
	}
}

func TestTaskSetNotBeforeRejectsInvalid(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "--project", projectID, "--title", "soak"))

	err := r.runErr("task", "set", taskID, "not_before=soon")
	if err == nil || !strings.Contains(err.Error(), "not_before") {
		t.Fatalf("expected a not_before parse error, got %v", err)
	}
}
