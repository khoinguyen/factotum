package cli

import (
	"strings"
	"testing"
)

func TestTaskGetJSONIsLossless(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "one", "--label", "urgent"))
	r.run("task", "note", "create", taskID, "-b", "remember this", "--link", "issue=https://example.com/1")

	out := r.run("task", "get", taskID, "-o", "json")
	for _, want := range []string{"remember this", `"notes"`, `"labels"`, "https://example.com/1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("task get -o json is missing %q:\n%s", want, out)
		}
	}
}

func TestTaskNextJSONIsLossless(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "rankable"))

	out := r.run("task", "next", "-p", projectID, "-o", "json")
	for _, want := range []string{taskID, "rankable", projectID, `"status"`, `"repo"`, `"score"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("task next -o json is missing %q:\n%s", want, out)
		}
	}
}
