package cli

import (
	"strings"
	"testing"
)

// TestAssignAndWaitAcceptActorReferenceForms pins ft task assign --actor and
// ft task wait --on to the same reference parsing the global --actor and
// ft msg send --from use: a bare id, the canonical actor:<id> address, and a
// colon-bearing actor name all reach the same Actor record.
func TestAssignAndWaitAcceptActorReferenceForms(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("actor", "create", "claude", "-k", "agent")
	r.run("actor", "create", "team:alice", "-k", "agent")

	cases := []struct {
		name string
		ref  string
		want string
	}{
		{"bare id", "claude", "claude"},
		{"canonical address", "actor:claude", "claude"},
		{"colon-bearing name", "team:alice", "team-alice"},
		{"canonical address of a colon-bearing name", "actor:team-alice", "team-alice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "work for "+tc.name))

			assigned := r.run("task", "assign", taskID, "--actor", tc.ref)
			if !strings.Contains(assigned, "assignee: "+tc.want) {
				t.Fatalf("assign --actor %q:\n%s\nwant assignee: %s", tc.ref, assigned, tc.want)
			}
			waiting := r.run("task", "wait", taskID, "--on", tc.ref)
			if !strings.Contains(waiting, "waiting_on: "+tc.want) {
				t.Fatalf("wait --on %q:\n%s\nwant waiting_on: %s", tc.ref, waiting, tc.want)
			}
		})
	}
}

// TestAssignAndWaitRejectMalformedActorReference pins the other half: a doubled
// or empty actor address is a malformed reference, not an actor named
// "actor:claude", so both commands say so instead of reporting that the actor
// was not found.
func TestAssignAndWaitRejectMalformedActorReference(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("actor", "create", "claude", "-k", "agent")
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "work"))

	for _, ref := range []string{"actor:actor:claude", "actor:"} {
		if err := r.runErr("task", "assign", taskID, "--actor", ref); err == nil || !strings.Contains(err.Error(), "actor reference") {
			t.Fatalf("assign --actor %q error = %v, want an actor reference error", ref, err)
		}
		if err := r.runErr("task", "wait", taskID, "--on", ref); err == nil || !strings.Contains(err.Error(), "actor reference") {
			t.Fatalf("wait --on %q error = %v, want an actor reference error", ref, err)
		}
	}
}

// TestMessageSendRejectsDoubledAndEmptyActorTarget reproduces the reported
// transcript: `ft msg send actor:actor:chief` used to store a message addressed
// to a mailbox no actor can claim.
func TestMessageSendRejectsDoubledAndEmptyActorTarget(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))

	cases := []struct {
		target string
		want   string
	}{
		{"actor:actor:chief", "actor reference"},
		{"actor:", "must be kind:id"},
	}
	for _, tc := range cases {
		if err := r.runErr("msg", "send", tc.target, "-b", "x", "--project", projectID, "--from", "chief"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("send to %q error = %v, want one mentioning %q", tc.target, err, tc.want)
		}
	}
	if all := r.run("msg", "inbox", "-p", projectID, "-o", "json"); strings.Contains(all, "actor:actor") {
		t.Fatalf("a doubled actor address was stored:\n%s", all)
	}

	// The canonical form still names the actor mailbox.
	sent := r.run("msg", "send", "actor:chief", "-b", "x", "--project", projectID, "--from", "chief")
	if !strings.Contains(sent, "to: actor:chief") {
		t.Fatalf("send to actor:chief:\n%s", sent)
	}
}
