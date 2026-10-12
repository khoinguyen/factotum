package cli

import (
	"encoding/json"
	"strings"
	"testing"
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
