package cli

import (
	"strings"
	"testing"
)

func TestTaskGetShowsLabelsAndPriority(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "x", "--label", "urgent", "--label", "ui", "--priority", "5"))

	text := r.run("task", "get", taskID)
	if !strings.Contains(text, "labels: urgent, ui") || !strings.Contains(text, "priority: 5") {
		t.Fatalf("task get should show labels and priority:\n%s", text)
	}

	jsonOut := r.run("task", "get", taskID, "-o", "json")
	if !strings.Contains(jsonOut, `"labels"`) || !strings.Contains(jsonOut, `"priority": 5`) {
		t.Fatalf("task get -o json should carry labels and priority:\n%s", jsonOut)
	}

	plain := firstField(t, r.run("task", "create", "-p", projectID, "-t", "y"))
	if got := r.run("task", "get", plain); strings.Contains(got, "labels:") || strings.Contains(got, "priority:") {
		t.Fatalf("unlabelled, unbumped task should not print labels/priority:\n%s", got)
	}
}
