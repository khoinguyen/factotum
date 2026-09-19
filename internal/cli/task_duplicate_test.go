package cli

import (
	"strings"
	"testing"
)

func TestTaskCreateWarnsOnDuplicateTitle(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	first := firstField(t, r.run("task", "create", "-p", projectID, "-t", "Add pagination"))

	_, stderr := r.runSplit("task", "create", "-p", projectID, "-t", "add PAGINATION")
	if !strings.Contains(stderr, "already exists") || !strings.Contains(stderr, first) {
		t.Fatalf("duplicate title should warn and point at the existing task:\n%s", stderr)
	}

	_, stderr = r.runSplit("task", "create", "-p", projectID, "-t", "Something else")
	if strings.Contains(stderr, "already exists") {
		t.Fatalf("distinct title should not warn:\n%s", stderr)
	}
}
