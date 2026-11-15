package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
)

type listedEvent struct {
	Kind    string         `json:"Kind"`
	By      *string        `json:"By"`
	Session string         `json:"Session"`
	Data    map[string]any `json:"Data"`
}

func listEvents(t *testing.T, r *runner) []listedEvent {
	t.Helper()
	var events []listedEvent
	if err := json.Unmarshal([]byte(r.run("event", "list", "-o", "json")), &events); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	return events
}

func findEvent(t *testing.T, events []listedEvent, kind string) listedEvent {
	t.Helper()
	for _, event := range events {
		if event.Kind == kind {
			return event
		}
	}
	t.Fatalf("no %s event in %+v", kind, events)
	return listedEvent{}
}

// TestCommandsAttributeEventsToCaller pins that a command run by an agent
// stamps its events with the actor and chief session from the environment.
func TestCommandsAttributeEventsToCaller(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(key string) string {
		switch key {
		case "FACTOTUM_DEFAULT_ACTOR":
			return "chief"
		case "FACTOTUM_SESSION":
			return "sess-1"
		}
		return ""
	}

	r.run("project", "create", "Acme")
	created := findEvent(t, listEvents(t, r), "project.created")
	if created.By == nil || *created.By != "chief" {
		t.Fatalf("project.created By = %v, want chief", created.By)
	}
	if created.Session != "sess-1" {
		t.Fatalf("project.created Session = %q, want sess-1", created.Session)
	}
}

// TestCommandsWithoutSessionStayUnattributed pins that an ordinary session with
// no actor/session env does not invent attribution.
func TestCommandsWithoutSessionStayUnattributed(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(string) string { return "" }

	r.run("project", "create", "Acme")
	created := findEvent(t, listEvents(t, r), "project.created")
	if created.By != nil || created.Session != "" {
		t.Fatalf("project.created attribution = (%v, %q), want (nil, empty)", created.By, created.Session)
	}
}

func TestSessionStartMintsAndRecords(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(key string) string {
		if key == "FACTOTUM_DEFAULT_ACTOR" {
			return "chief"
		}
		return ""
	}
	project := firstField(t, r.run("project", "create", "Acme"))

	sid := firstField(t, r.run("session", "start", "-p", project))
	if sid == "" {
		t.Fatal("session start printed no session_id")
	}

	started := findEvent(t, listEvents(t, r), "session.started")
	if started.Session != sid {
		t.Fatalf("session.started Session = %q, want %q", started.Session, sid)
	}
	if started.By == nil || *started.By != "chief" {
		t.Fatalf("session.started By = %v, want chief", started.By)
	}
}

// TestSessionStartMintsDespiteInheritedSession pins F1: a reincarnated chief is
// launched from a shell that already carries the outgoing session, so start must
// mint a fresh id rather than reuse the inherited one.
func TestSessionStartMintsDespiteInheritedSession(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(key string) string {
		switch key {
		case "FACTOTUM_DEFAULT_ACTOR":
			return "chief"
		case "FACTOTUM_SESSION":
			return "oldsess"
		}
		return ""
	}
	project := firstField(t, r.run("project", "create", "Acme"))

	sid := firstField(t, r.run("session", "start", "-p", project))
	if sid == "" || sid == "oldsess" {
		t.Fatalf("session start = %q, want a freshly minted id, not the inherited oldsess", sid)
	}
}

func TestSessionStartHonorsExplicitSession(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(string) string { return "" }
	project := firstField(t, r.run("project", "create", "Acme"))

	sid := firstField(t, r.run("session", "start", "-p", project, "--session", "explicit1"))
	if sid != "explicit1" {
		t.Fatalf("session start --session = %q, want explicit1", sid)
	}
}

// TestSessionAckRecordsEnvironmentSession pins the child side of the groom
// guard's acknowledgement: `ft session ack` records an event attributed to the
// session its environment carries, so the run that injected the id can see the
// child picked it up.
func TestSessionAckRecordsEnvironmentSession(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(key string) string {
		if key == "FACTOTUM_SESSION" {
			return "groom-1"
		}
		return ""
	}
	project := firstField(t, r.run("project", "create", "Acme"))

	out := r.run("session", "ack", "-p", project)
	if !strings.Contains(out, "acked: true") || !strings.Contains(out, "session_id: groom-1") {
		t.Fatalf("session ack output missing the acknowledgement:\n%s", out)
	}
	acked := findEvent(t, listEvents(t, r), "session.acked")
	if acked.Session != "groom-1" {
		t.Fatalf("session.acked Session = %q, want groom-1", acked.Session)
	}
}

// TestSessionAckWithoutSessionFails pins that the acknowledgement cannot be
// conjured: with no session in the environment the command fails and records
// nothing, so naming a session in a prompt is not enough to satisfy it.
func TestSessionAckWithoutSessionFails(t *testing.T) {
	r := newRunner(t)
	project := firstField(t, r.run("project", "create", "Acme"))

	err := r.runErrWithGetenv(func(string) string { return "" }, "session", "ack", "-p", project)
	if err == nil {
		t.Fatal("session ack with no session error = nil, want it to name the missing environment")
	}
	if !strings.Contains(err.Error(), "FACTOTUM_SESSION") {
		t.Fatalf("error = %v, want it to name FACTOTUM_SESSION", err)
	}
	if events := r.run("event", "list", "-o", "json"); strings.Contains(events, "session.acked") {
		t.Fatalf("an unshipped ack still recorded one:\n%s", events)
	}
}

// runErrWithGetenv executes one command with the runner's fakes but the given
// environment getter and returns the error, so a test can drive an invocation
// whose environment differs from the runner's own.
func (r *runner) runErrWithGetenv(getenv func(string) string, args ...string) error {
	r.t.Helper()
	var out bytes.Buffer
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, &out, &out, getenv)
	base := r.setup(deps)
	root := NewRoot(deps)
	root.SetArgs(append(append([]string{}, base...), args...))
	root.SetOut(&out)
	root.SetErr(&out)
	return root.Execute()
}

// TestRegisterOmitsEmptySession pins F3: an optional single-result field is
// omitted from text output when empty, not printed blank.
func TestRegisterOmitsEmptySession(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(string) string { return "" }
	project := firstField(t, r.run("project", "create", "Acme"))

	out := r.run("msg", "agent", "register", "--actor", "builder-t-1", "-p", project)
	if strings.Contains(out, "session:") {
		t.Fatalf("register output = %q, want no session line when unset", out)
	}
}

func TestRegisterRecordsSessionFromEnv(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(key string) string {
		if key == "FACTOTUM_SESSION" {
			return "sess-1"
		}
		return ""
	}
	project := firstField(t, r.run("project", "create", "Acme"))

	out := r.run("msg", "agent", "register", "--actor", "builder-t-1", "-p", project)
	if !strings.Contains(out, "session: sess-1") {
		t.Fatalf("register output = %q, want session: sess-1", out)
	}
}

func TestSessionRetireNamesSuccessor(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(key string) string {
		if key == "FACTOTUM_DEFAULT_ACTOR" {
			return "chief"
		}
		return ""
	}
	project := firstField(t, r.run("project", "create", "Acme"))
	sid := firstField(t, r.run("session", "start", "-p", project))

	r.run("session", "retire", "-p", project, "--session", sid, "--successor", "next1")

	retired := findEvent(t, listEvents(t, r), "session.retired")
	if retired.Session != sid {
		t.Fatalf("session.retired Session = %q, want %q", retired.Session, sid)
	}
	if retired.Data["successor"] != "next1" {
		t.Fatalf("session.retired successor = %v, want next1", retired.Data["successor"])
	}
}
