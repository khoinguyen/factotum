package cli

import (
	"strings"
	"testing"
)

// TestTaskAssignAcceptsLoopRole pins the mutation half of the identity rule:
// `task assign --actor` resolves a loop role with no Actor record the same way
// claim and readiness do, so the loop can hand work to a role mailbox.
func TestTaskAssignAcceptsLoopRole(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "role work"))

	assigned := r.run("task", "assign", taskID, "--actor", "builder-t-1")
	if !strings.Contains(assigned, "assignee: builder-t-1") {
		t.Fatalf("task assign --actor role:\n%s\nwant assignee: builder-t-1", assigned)
	}
	if got := r.run("task", "get", taskID); !strings.Contains(got, "assignee: (agent) builder-t-1") {
		t.Fatalf("a role-assigned task should read as agent work:\n%s", got)
	}
}

// TestTaskWaitOnAcceptsLoopRole pins `task wait --on` to the same rule, so a
// task can wait on a role mailbox with no Actor record.
func TestTaskWaitOnAcceptsLoopRole(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "waiting work"))

	waited := r.run("task", "wait", taskID, "--on", "chief")
	if !strings.Contains(waited, "waiting_on: chief") {
		t.Fatalf("task wait --on role:\n%s\nwant waiting_on: chief", waited)
	}
}

// TestTaskClaimExplicitLoopRole pins an explicit `--for` to the identity rule
// claim's configured default already followed, so both spellings agree.
func TestTaskClaimExplicitLoopRole(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "role work", "--groomed", "--acceptance", "works"))

	claimed := r.run("task", "claim", "-p", projectID, "--for", "builder-t-1")
	if !strings.Contains(claimed, taskID) {
		t.Fatalf("task claim --for role did not take %s:\n%s", taskID, claimed)
	}
}

// TestCapturePromoteAcceptsLoopRole pins both capture surfaces to the identity
// rule: promoting or triaging hands the new task to a role mailbox with no Actor
// record, and the result is agent-ready (next lists it).
func TestCapturePromoteAcceptsLoopRole(t *testing.T) {
	cases := []struct {
		name   string
		source func(r *runner, projectID string) string
		refine func(r *runner, captureID string, actorRef string) string
	}{
		{
			name: "idea promote",
			source: func(r *runner, projectID string) string {
				return firstField(r.t, r.run("idea", "create", "-p", projectID, "-t", "a spark"))
			},
			refine: func(r *runner, captureID string, actorRef string) string {
				return r.run("idea", "promote", captureID, "--actor", actorRef, "--acceptance", "it works")
			},
		},
		{
			name: "bug triage",
			source: func(r *runner, projectID string) string {
				return firstField(r.t, r.run("bug", "create", "-p", projectID, "-t", "it crashes"))
			},
			refine: func(r *runner, captureID string, actorRef string) string {
				return r.run("bug", "triage", captureID, "--actor", actorRef, "--acceptance", "it stops crashing")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(t)
			projectID := firstField(t, r.run("project", "create", "Acme"))
			captureID := tc.source(r, projectID)

			out := tc.refine(r, captureID, "reviewer-t-1")
			taskID := firstField(t, out)
			if got := r.run("task", "get", taskID); !strings.Contains(got, "assignee: (agent) reviewer-t-1") {
				t.Fatalf("promoted task should be assigned to the role:\n%s", got)
			}
			if next := r.run("task", "next", "-p", projectID); !strings.Contains(next, taskID) {
				t.Fatalf("a role-assigned promoted task should be agent-next:\n%s", next)
			}
		})
	}
}

// TestMutationAcceptsRegisteredRoleShapedRecord pins record-before-grammar on
// the mutation surfaces: a registered human whose id looks role-shaped resolves
// as the human record, and promotion still refuses a human.
func TestMutationAcceptsRegisteredRoleShapedRecord(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("actor", "create", "builder-t-1", "-k", "human")
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "role work"))

	r.run("task", "assign", taskID, "--actor", "builder-t-1")
	if got := r.run("task", "get", taskID); !strings.Contains(got, "assignee: (human) builder-t-1") {
		t.Fatalf("a registered role-shaped human must win over the role grammar:\n%s", got)
	}

	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "a spark"))
	err := r.runErr("idea", "promote", ideaID, "--actor", "builder-t-1", "--acceptance", "it works")
	if err == nil || !strings.Contains(err.Error(), "not an agent") {
		t.Fatalf("promote to a registered role-shaped human error = %v, want a not-an-agent error", err)
	}
}

// TestMutationRejectsRoleLookalike pins the boundary: a token that merely starts
// with a role name is neither a record nor a role, so it still fails loudly
// instead of silently assigning work to a mailbox nobody claims.
func TestMutationRejectsRoleLookalike(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "role work"))
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "a spark"))

	for _, ref := range []string{"builder-gpt-4", "builer-t-1"} {
		if err := r.runErr("task", "assign", taskID, "--actor", ref); err == nil || !strings.Contains(err.Error(), ref) {
			t.Fatalf("assign --actor %q error = %v, want an unknown-actor error naming it", ref, err)
		}
		if err := r.runErr("idea", "promote", ideaID, "--actor", ref, "--acceptance", "it works"); err == nil || !strings.Contains(err.Error(), ref) {
			t.Fatalf("promote --actor %q error = %v, want an unknown-actor error naming it", ref, err)
		}
	}
}
