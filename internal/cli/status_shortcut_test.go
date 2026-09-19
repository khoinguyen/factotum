package cli

import (
	"strings"
	"testing"
)

func TestTopLevelStatusShortcuts(t *testing.T) {
	cases := []struct {
		command string
		status  string
	}{
		{"start", "in_progress"},
		{"review", "ready_for_review"},
		{"done", "done"},
		{"block", "blocked"},
		{"cancel", "cancelled"},
		{"reopen", "todo"},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			r := newRunner(t)
			projectID := firstField(t, r.run("project", "create", "Acme"))
			taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "x"))

			out := r.run(tc.command, taskID)
			if fields := strings.Fields(out); len(fields) < 2 || fields[0] != taskID || fields[1] != tc.status {
				t.Fatalf("ft %s %s output = %q, want %s %s", tc.command, taskID, out, taskID, tc.status)
			}
			if got := r.run("task", "get", taskID); !strings.HasPrefix(got, "("+tc.status+") ") {
				t.Fatalf("ft %s did not set %s:\n%s", tc.command, tc.status, got)
			}
		})
	}
}

func TestDoneShortcutMatchesTaskAndSet(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	shortcut := firstField(t, r.run("task", "create", "-p", projectID, "-t", "shortcut"))
	nested := firstField(t, r.run("task", "create", "-p", projectID, "-t", "nested"))
	viaSet := firstField(t, r.run("task", "create", "-p", projectID, "-t", "via set"))

	r.run("done", shortcut)
	r.run("task", "done", nested)
	r.run("task", "set", viaSet, "status=done")

	for _, id := range []string{shortcut, nested, viaSet} {
		if got := r.run("task", "get", id); !strings.HasPrefix(got, "(done) ") {
			t.Fatalf("task %s not done:\n%s", id, got)
		}
	}
}
