package cli

import (
	"os"
	"strings"
	"testing"
)

// TestCallerAttributionPrecedence is the documented precedence, end to end:
// --actor > FACTOTUM_CALLER > FACTOTUM_DEFAULT_ACTOR > project/user default_actor.
// Each case drives `project create` and reads the By stamp on project.created.
func TestCallerAttributionPrecedence(t *testing.T) {
	cases := []struct {
		name       string
		fileActor  string // project-scoped default_actor
		defaultEnv string // FACTOTUM_DEFAULT_ACTOR
		callerEnv  string // FACTOTUM_CALLER
		flagActor  string // --actor
		want       string
	}{
		{name: "project default actor", fileActor: "proj", want: "proj"},
		{name: "default actor env beats the file", fileActor: "proj", defaultEnv: "def", want: "def"},
		{name: "caller env beats the default actor env", defaultEnv: "def", callerEnv: "alice", want: "alice"},
		{name: "flag actor beats the caller env", callerEnv: "alice", flagActor: "bob", want: "bob"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(t)
			// Register every actor the case names with a neutral environment, so
			// each one is a known actor before the case env is applied: the case
			// is about which source wins, not which names exist.
			r.getenv = func(string) string { return "" }
			registered := map[string]bool{}
			for _, name := range []string{"alice", tc.fileActor, tc.defaultEnv, tc.callerEnv, tc.flagActor} {
				if name == "" || registered[name] {
					continue
				}
				registered[name] = true
				r.run("actor", "create", name)
			}
			if tc.fileActor != "" {
				if err := os.WriteFile(r.projectPath, []byte("default_actor = \""+tc.fileActor+"\"\n"), 0o644); err != nil {
					t.Fatalf("write project config: %v", err)
				}
			}
			r.getenv = func(key string) string {
				switch key {
				case "FACTOTUM_DEFAULT_ACTOR":
					return tc.defaultEnv
				case "FACTOTUM_CALLER":
					return tc.callerEnv
				default:
					return ""
				}
			}

			args := []string{"project", "create", "Acme"}
			if tc.flagActor != "" {
				args = append(args, "--actor", tc.flagActor)
			}
			r.run(args...)

			created := findEvent(t, listEvents(t, r), "project.created")
			if created.By == nil || *created.By != tc.want {
				t.Fatalf("project.created By = %v, want %q", created.By, tc.want)
			}
		})
	}
}

// TestUnregisteredCallerIsUnattributedWithWarning pins the failure mode: an
// explicit caller that names no registered actor is dropped (no attribution),
// warned about clearly, and never fails the command or auto-creates an actor.
func TestUnregisteredCallerIsUnattributedWithWarning(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(key string) string {
		if key == "FACTOTUM_CALLER" {
			return "ghost"
		}
		return ""
	}

	_, stderr := r.runSplit("project", "create", "Acme")

	created := findEvent(t, listEvents(t, r), "project.created")
	if created.By != nil {
		t.Fatalf("project.created By = %v, want none for an unregistered caller", created.By)
	}
	if !strings.Contains(stderr, `caller "ghost" is not a registered actor`) {
		t.Fatalf("stderr = %q, want an unregistered-caller warning naming ghost", stderr)
	}
	if actors := r.run("actor", "list"); strings.Contains(actors, "ghost") {
		t.Fatalf("an unregistered caller was auto-created:\n%s", actors)
	}
}

// TestUnregisteredActorFlagIsUnattributedWithWarning pins the --actor rule: an
// explicit actor that names no registered record and is not one of the four
// loop-role prefixes (chief, builder-, reviewer-, qa-) is warned about and left
// unattributed, never stamped verbatim on the event or used as the auto-QA
// reminder target. A role-shaped name the loop only sets as a configured default
// (grooming-agent-<artifact>, architecture-reviewer-<feature>) is not exempt
// here: name it as the default, not as --actor.
func TestUnregisteredActorFlagIsUnattributedWithWarning(t *testing.T) {
	for _, name := range []string{"ghostrider", "grooming-agent-art-7"} {
		t.Run(name, func(t *testing.T) {
			r := newRunner(t)
			r.getenv = func(string) string { return "" }

			_, stderr := r.runSplit("project", "create", "Acme", "--actor", name)

			created := findEvent(t, listEvents(t, r), "project.created")
			if created.By != nil {
				t.Fatalf("project.created By = %v, want none for an unregistered actor", created.By)
			}
			if !strings.Contains(stderr, `actor "`+name+`" is not a registered actor`) {
				t.Fatalf("stderr = %q, want an unregistered-actor warning naming %s", stderr, name)
			}
			if actors := r.run("actor", "list"); strings.Contains(actors, name) {
				t.Fatalf("an unregistered actor was auto-created:\n%s", actors)
			}
		})
	}
}

// TestLoopRoleActorFlagNeedsNoRecord pins the exemption: a loop role is a
// mailbox, not a store actor, so --actor builder-<task> attributes the event
// without a warning even though no Actor record exists.
func TestLoopRoleActorFlagNeedsNoRecord(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(string) string { return "" }

	_, stderr := r.runSplit("project", "create", "Acme", "--actor", "builder-t-1")

	created := findEvent(t, listEvents(t, r), "project.created")
	if created.By == nil || *created.By != "builder-t-1" {
		t.Fatalf("project.created By = %v, want builder-t-1", created.By)
	}
	if strings.Contains(stderr, "unattributed") {
		t.Fatalf("a loop role should not warn: %q", stderr)
	}
}

// TestConfiguredDefaultActorIsTrustedAsARole pins the scope of the --actor
// registration check: a configured default is set once by the loop to a
// role-shaped identity (loop-agent.sh exports FACTOTUM_DEFAULT_ACTOR for the
// builder, reviewer, qa, chief, and grooming-agent roles), so it is attributed
// as given even without an Actor record.
func TestConfiguredDefaultActorIsTrustedAsARole(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(key string) string {
		if key == "FACTOTUM_DEFAULT_ACTOR" {
			return "grooming-agent-art-1"
		}
		return ""
	}

	_, stderr := r.runSplit("project", "create", "Acme")

	created := findEvent(t, listEvents(t, r), "project.created")
	if created.By == nil || *created.By != "grooming-agent-art-1" {
		t.Fatalf("project.created By = %v, want grooming-agent-art-1", created.By)
	}
	if strings.Contains(stderr, "unattributed") {
		t.Fatalf("a configured role should not warn: %q", stderr)
	}
}

// TestRegisteredCallerAttributesEvents pins the happy path in isolation: a
// caller naming a registered actor stamps its events with that actor.
func TestRegisteredCallerAttributesEvents(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(string) string { return "" }
	r.run("actor", "create", "alice")

	r.getenv = func(key string) string {
		if key == "FACTOTUM_CALLER" {
			return "alice"
		}
		return ""
	}
	_, stderr := r.runSplit("project", "create", "Acme")

	created := findEvent(t, listEvents(t, r), "project.created")
	if created.By == nil || *created.By != "alice" {
		t.Fatalf("project.created By = %v, want alice", created.By)
	}
	if strings.Contains(stderr, "unattributed") {
		t.Fatalf("a registered caller should not warn: %q", stderr)
	}
}
