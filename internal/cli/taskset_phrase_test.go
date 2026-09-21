package cli

import (
	"strings"
	"testing"
)

func TestTaskSetFieldAssignmentUnaffectedByPhrasePath(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "soak"))

	if out := r.run("task", "set", taskID, "status=done"); !strings.Contains(out, "status: done") {
		t.Fatalf("field=value assignment should still work:\n%s", out)
	}
}

func TestTaskSetPhraseWithoutJudgeIsAUsageError(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "soak"))

	// No '=' means a phrase; with no judge configured (tests are offline), the
	// command reports that a judge is required instead of mutating the task.
	err := r.runErr("task", "set", taskID, "mark it done")
	if err == nil || !strings.Contains(err.Error(), "judge") {
		t.Fatalf("expected a judge-required usage error, got %v", err)
	}
}
