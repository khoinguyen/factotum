package cli

import (
	"strings"
	"testing"
)

func TestTaskListAndNextFilterByLabel(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	groomed := firstField(t, r.run("task", "create", "-p", projectID, "-t", "groom", "--label", "needs-grooming"))
	ready := firstField(t, r.run("task", "create", "-p", projectID, "-t", "ready", "--label", "ready"))
	both := firstField(t, r.run("task", "create", "-p", projectID, "-t", "both", "--label", "needs-grooming", "--label", "urgent"))

	listed := r.run("task", "list", "-p", projectID, "--label", "needs-grooming")
	if !strings.Contains(listed, groomed) || !strings.Contains(listed, both) || strings.Contains(listed, ready) {
		t.Fatalf("task list --label needs-grooming wrong:\n%s", listed)
	}

	next := r.run("task", "next", "-p", projectID, "--label", "needs-grooming")
	if !strings.Contains(next, groomed) || !strings.Contains(next, both) || strings.Contains(next, ready) {
		t.Fatalf("task next --label needs-grooming wrong:\n%s", next)
	}

	and := r.run("task", "list", "-p", projectID, "--label", "needs-grooming", "--label", "urgent")
	if !strings.Contains(and, both) || strings.Contains(and, groomed) {
		t.Fatalf("multiple --label should require all labels:\n%s", and)
	}
}
