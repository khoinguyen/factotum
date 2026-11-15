package cli

import (
	"strings"
	"testing"
)

func TestTaskClaimClaimsDistinctReadyTasks(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("actor", "create", "--kind", "agent", "claude")
	a := firstField(t, r.run("task", "create", "-p", projectID, "-t", "a", "--groomed", "--acceptance", "done"))
	b := firstField(t, r.run("task", "create", "-p", projectID, "-t", "b", "--groomed", "--acceptance", "done"))

	first := r.run("task", "claim", "--for", "claude", "-p", projectID)
	firstID := firstField(t, first)
	if firstID != a && firstID != b {
		t.Fatalf("claim returned unexpected task %q:\n%s", firstID, first)
	}
	if !strings.Contains(first, "assignee: claude") {
		t.Fatalf("claim should self-assign:\n%s", first)
	}
	if !strings.Contains(first, "status: todo") {
		t.Fatalf("claim without --start should leave the task in todo:\n%s", first)
	}

	second := firstField(t, r.run("task", "claim", "--for", "claude", "-p", projectID))
	if second == firstID {
		t.Fatalf("second claim must pick a different task, both were %s", second)
	}
	if err := r.runErr("task", "claim", "--for", "claude", "-p", projectID); err == nil {
		t.Fatal("claiming with no ready task left should error")
	}
}

func TestTaskClaimStartBeginsWork(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("actor", "create", "--kind", "agent", "claude")
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "a", "--groomed", "--acceptance", "done"))

	out := r.run("task", "claim", "--for", "claude", "--start", "-p", projectID)
	if !strings.Contains(out, "status: in_progress") {
		t.Fatalf("claim --start should report in_progress:\n%s", out)
	}
	if !strings.Contains(out, "assignee: claude") {
		t.Fatalf("claim --start should self-assign:\n%s", out)
	}

	get := r.run("task", "get", taskID, "-o", "json")
	if !strings.Contains(get, `"status": "in_progress"`) {
		t.Fatalf("structured output should show in_progress:\n%s", get)
	}
}

func TestTaskClaimRequiresActor(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("task", "create", "-p", projectID, "-t", "a")

	if err := r.runErr("task", "claim", "-p", projectID); err == nil {
		t.Fatal("claim without --for and without default_actor should error")
	}
}

// TestTaskClaimUsesLoopRoleDefault pins the last hole in the default-actor
// rule: claim resolves a configured default through app.LoopDefaultRole, the
// same grammar bindCaller applies to attribution, so a loop session whose only
// identity is a role — with no Actor record and no --for — can claim. Each role
// is the shape loop-agent.sh exports as FACTOTUM_DEFAULT_ACTOR.
func TestTaskClaimUsesLoopRoleDefault(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		want string
	}{
		{"chief", "chief", "chief"},
		{"builder", "builder-t-1", "builder-t-1"},
		{"reviewer", "reviewer-t-ww4tono3t6", "reviewer-t-ww4tono3t6"},
		{"qa", "qa-t-1", "qa-t-1"},
		{"grooming agent", "grooming-agent-art-ilsnyg2h3a", "grooming-agent-art-ilsnyg2h3a"},
		{"architecture reviewer", "architecture-reviewer-groom-abcde", "architecture-reviewer-groom-abcde"},
		{"canonical address", "actor:builder-t-1", "builder-t-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(t)
			r.getenv = func(string) string { return "" }
			projectID := firstField(t, r.run("project", "create", "Acme"))
			taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "a", "--groomed", "--acceptance", "done"))

			r.getenv = func(key string) string {
				if key == "FACTOTUM_DEFAULT_ACTOR" {
					return tc.env
				}
				return ""
			}
			out, stderr := r.runSplit("task", "claim", "-p", projectID)

			if !strings.Contains(out, "task_id: "+taskID) {
				t.Fatalf("claim did not take the only ready task:\n%s", out)
			}
			if !strings.Contains(out, "assignee: "+tc.want) {
				t.Fatalf("claim should assign the default role as %s:\n%s", tc.want, out)
			}
			if strings.Contains(stderr, "unattributed") {
				t.Fatalf("a loop-role default should not warn: %q", stderr)
			}
			if actors := r.run("actor", "list"); strings.Contains(actors, tc.want) {
				t.Fatalf("a loop role was auto-created as an Actor record:\n%s", actors)
			}
		})
	}
}

// TestTaskClaimDropsUnregisteredDefaultActor pins the other half: a typo'd
// default still warns and is dropped by bindCaller, so claim behaves exactly as
// it does with no default — a usage error, no assignment, and no event.
func TestTaskClaimDropsUnregisteredDefaultActor(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(string) string { return "" }
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "a", "--groomed", "--acceptance", "done"))

	r.getenv = func(key string) string {
		if key == "FACTOTUM_DEFAULT_ACTOR" {
			return "builer-t-1"
		}
		return ""
	}
	_, stderr := r.runSplit("task", "claim", "-p", projectID)

	if !strings.Contains(stderr, `default actor "builer-t-1" is not a registered actor`) {
		t.Fatalf("stderr = %q, want a warning naming the typo'd default", stderr)
	}
	if !strings.Contains(stderr, "provide --for <actor>, or set default_actor") {
		t.Fatalf("stderr = %q, want claim's no-default usage error", stderr)
	}

	r.getenv = func(string) string { return "" }
	if got := r.run("task", "get", taskID); strings.Contains(got, "assignee:") {
		t.Fatalf("an unregistered default was stamped as an assignee:\n%s", got)
	}
	for _, event := range listEvents(t, r) {
		if event.Kind == "task.assigned" {
			t.Fatalf("a dropped default still produced an assignment event: %+v", event)
		}
	}
}

// TestTaskClaimUsesRegisteredDefaultActor keeps the original behavior: a
// configured default that names an Actor record is used as that record, with no
// role grammar needed.
func TestTaskClaimUsesRegisteredDefaultActor(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(string) string { return "" }
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("actor", "create", "--kind", "agent", "claude")
	r.run("task", "create", "-p", projectID, "-t", "a", "--groomed", "--acceptance", "done")

	r.getenv = func(key string) string {
		if key == "FACTOTUM_DEFAULT_ACTOR" {
			return "claude"
		}
		return ""
	}
	out, stderr := r.runSplit("task", "claim", "-p", projectID)

	if !strings.Contains(out, "assignee: claude") {
		t.Fatalf("claim should assign the registered default:\n%s", out)
	}
	if strings.Contains(stderr, "unattributed") {
		t.Fatalf("a registered default should not warn: %q", stderr)
	}
}
