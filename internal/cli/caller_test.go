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

// TestUnregisteredDefaultActorIsUnattributedWithWarning pins the default path to
// the same rule as --actor, so a typo in FACTOTUM_DEFAULT_ACTOR / default_actor
// cannot stamp events. A default is trusted only when it names a registered
// actor or a loop role carrying a well-formed ft id; a role typo or a token that merely
// starts with a role name is warned about and dropped.
func TestUnregisteredDefaultActorIsUnattributedWithWarning(t *testing.T) {
	for _, name := range []string{"builer-t-1", "builder-gpt-4", "qa-bot", "ghostrider"} {
		t.Run(name, func(t *testing.T) {
			r := newRunner(t)
			r.getenv = func(key string) string {
				if key == "FACTOTUM_DEFAULT_ACTOR" {
					return name
				}
				return ""
			}

			_, stderr := r.runSplit("project", "create", "Acme")

			created := findEvent(t, listEvents(t, r), "project.created")
			if created.By != nil {
				t.Fatalf("project.created By = %v, want none for an unregistered default", created.By)
			}
			if !strings.Contains(stderr, `default actor "`+name+`" is not a registered actor`) {
				t.Fatalf("stderr = %q, want a warning naming the default actor %s", stderr, name)
			}
			if actors := r.run("actor", "list"); strings.Contains(actors, name) {
				t.Fatalf("an unregistered default was auto-created:\n%s", actors)
			}
		})
	}
}

// TestConfiguredLoopDefaultActorIsAttributed pins the exemption that keeps the
// loop attributed: the role shapes loop-agent.sh exports as
// FACTOTUM_DEFAULT_ACTOR are trusted as given, with no Actor record and no
// warning. That covers the four LoopRole roles and the grooming-agent and
// architecture-reviewer roles, which the loop only ever sets as a default.
func TestConfiguredLoopDefaultActorIsAttributed(t *testing.T) {
	for _, name := range []string{
		"chief",
		"builder-t-1",
		"reviewer-t-1",
		"qa-t-1",
		"grooming-agent-art-1",
		"architecture-reviewer-groom-abcde",
	} {
		t.Run(name, func(t *testing.T) {
			r := newRunner(t)
			r.getenv = func(key string) string {
				if key == "FACTOTUM_DEFAULT_ACTOR" {
					return name
				}
				return ""
			}

			_, stderr := r.runSplit("project", "create", "Acme")

			created := findEvent(t, listEvents(t, r), "project.created")
			if created.By == nil || *created.By != name {
				t.Fatalf("project.created By = %v, want %s", created.By, name)
			}
			if strings.Contains(stderr, "unattributed") {
				t.Fatalf("a loop default role should not warn: %q", stderr)
			}
		})
	}
}

// TestRegisteredActorBeatsTheRoleGrammar pins the order of the check: the store
// record is tried first, so a default whose name looks role-shaped is attributed
// because the actor exists, not because it matches a grammar.
func TestRegisteredActorBeatsTheRoleGrammar(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(string) string { return "" }
	r.run("actor", "create", "builder-gpt-4")

	r.getenv = func(key string) string {
		if key == "FACTOTUM_DEFAULT_ACTOR" {
			return "builder-gpt-4"
		}
		return ""
	}
	_, stderr := r.runSplit("project", "create", "Acme")

	created := findEvent(t, listEvents(t, r), "project.created")
	if created.By == nil || *created.By != "builder-gpt-4" {
		t.Fatalf("project.created By = %v, want the registered actor builder-gpt-4", created.By)
	}
	if strings.Contains(stderr, "unattributed") {
		t.Fatalf("a registered default should not warn: %q", stderr)
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
