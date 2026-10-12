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
			// Register the caller with a neutral environment so it is a known
			// actor before the case env is applied.
			r.getenv = func(string) string { return "" }
			r.run("actor", "create", "alice")
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
