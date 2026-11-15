package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
)

// claimAsRole leaves one groomed task claimed by a loop role that has no Actor
// record, and returns the runner, project, and task id.
func claimAsRole(t *testing.T) (*runner, string, string) {
	t.Helper()
	r := newRunner(t)
	r.getenv = func(string) string { return "" }
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "role work", "--groomed", "--acceptance", "works"))
	r.getenv = func(key string) string {
		if key == "FACTOTUM_DEFAULT_ACTOR" {
			return "builder-t-1"
		}
		return ""
	}
	if out := r.run("task", "claim", "-p", projectID); !strings.Contains(out, taskID) {
		t.Fatalf("claim did not take %s:\n%s", taskID, out)
	}
	r.getenv = func(string) string { return "" }
	return r, projectID, taskID
}

// TestTaskNextForLoopRole pins the read-only --for filter to the identity rule
// readiness uses: a loop role with no Actor record filters to the task it holds,
// and --explain takes the same filter. A role lookalike is still an unknown actor.
func TestTaskNextForLoopRole(t *testing.T) {
	r, projectID, roleTask := claimAsRole(t)
	humanTask := firstField(t, r.run("task", "create", "-p", projectID, "-t", "human work", "--groomed", "--acceptance", "works"))

	out := r.run("task", "next", "--for", "builder-t-1", "-p", projectID)
	if !strings.Contains(out, roleTask) {
		t.Fatalf("task next --for role missing the role's task %s:\n%s", roleTask, out)
	}
	if strings.Contains(out, humanTask) {
		t.Fatalf("task next --for role listed another actor's task %s:\n%s", humanTask, out)
	}

	r.run("task", "set", roleTask, "status=blocked")
	explain := r.run("task", "next", "--explain", "--for", "builder-t-1", "-p", projectID)
	if !strings.Contains(explain, roleTask) || !strings.Contains(explain, "blocked") {
		t.Fatalf("task next --explain --for role should list the blocked role task %s:\n%s", roleTask, explain)
	}

	// A lookalike merely starts with a role name, so it stays an unknown actor.
	out2, err := runTaskNextCapture(r, "task", "next", "-p", projectID, "--for", "builder-gpt-4")
	if err == nil || !strings.Contains(err.Error()+out2, "builder-gpt-4") {
		t.Fatalf("task next --for lookalike = (%q, %v), want an unknown-actor error", out2, err)
	}
}

// TestTaskGetShowsLoopRoleKind pins the single-task view to the readiness
// identity: a role-claimed task names its agent kind like any registered agent,
// so the detail view agrees with the bucket the task sits in.
func TestTaskGetShowsLoopRoleKind(t *testing.T) {
	r, _, roleTask := claimAsRole(t)
	out := r.run("task", "get", roleTask)
	if !strings.Contains(out, "assignee: (agent) builder-t-1") {
		t.Fatalf("task get should label the role assignee as an agent:\n%s", out)
	}
}

// runTaskNextCapture executes a command that is expected to fail, returning its
// combined output and error; run/runSplit fail the test on a non-usage error.
func runTaskNextCapture(r *runner, args ...string) (string, error) {
	var out bytes.Buffer
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, &out, &out, r.getenv)
	base := r.setup(deps)
	root := NewRoot(deps)
	root.SetArgs(append(append([]string{}, base...), args...))
	root.SetOut(&out)
	root.SetErr(&out)
	err := root.Execute()
	return out.String(), err
}
