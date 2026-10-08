package core

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseAddress(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		wantKind AddressKind
		wantID   string
		wantErr  bool
	}{
		{"actor", "actor:act-1", AddressActor, "act-1", false},
		{"run", "run:run-1", AddressRun, "run-1", false},
		{"task", "task:t-1", AddressTask, "t-1", false},
		{"unknown kind", "group:x", "", "", true},
		{"missing separator", "act-1", "", "", true},
		{"empty id", "actor:", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, id, err := ParseAddress(tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseAddress(%q) error = %v, wantErr %v", tt.value, err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if kind != tt.wantKind || id != tt.wantID {
				t.Fatalf("ParseAddress(%q) = (%q, %q), want (%q, %q)", tt.value, kind, id, tt.wantKind, tt.wantID)
			}
		})
	}
}

func TestAddressHelpers(t *testing.T) {
	if got, want := ActorAddress("act-1"), Address("actor:act-1"); got != want {
		t.Fatalf("ActorAddress = %q, want %q", got, want)
	}
	if got, want := RunAddress("run-1"), Address("run:run-1"); got != want {
		t.Fatalf("RunAddress = %q, want %q", got, want)
	}
	if got, want := TaskAddress("t-1"), Address("task:t-1"); got != want {
		t.Fatalf("TaskAddress = %q, want %q", got, want)
	}
	if got := TaskAddress("t-1").Kind(); got != AddressTask {
		t.Fatalf("Kind = %q, want %q", got, AddressTask)
	}
	if got := Address("nope").Kind(); got != "" {
		t.Fatalf("Kind(malformed) = %q, want empty", got)
	}
}

func TestMessageStateTerminal(t *testing.T) {
	tests := []struct {
		state    MessageState
		terminal bool
		valid    bool
	}{
		{MessageQueued, false, true},
		{MessageDelivered, false, true},
		{MessageRead, true, true},
		{MessageFailed, true, true},
		{"lost", false, false},
	}
	for _, tt := range tests {
		if got := tt.state.Terminal(); got != tt.terminal {
			t.Errorf("MessageState(%q).Terminal() = %v, want %v", tt.state, got, tt.terminal)
		}
		if got := tt.state.Valid(); got != tt.valid {
			t.Errorf("MessageState(%q).Valid() = %v, want %v", tt.state, got, tt.valid)
		}
	}
}

func validMessage() Message {
	taskID := TicketID("t-1")
	return Message{
		ID:        "msg-1",
		ProjectID: "prj-1",
		To:        TaskAddress(taskID),
		TaskID:    &taskID,
		Body:      "hello",
		State:     MessageQueued,
	}
}

func TestMessageValidate(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*Message)
		bodyLimit int
		wantErr   bool
	}{
		{"valid", func(*Message) {}, 0, false},
		{"missing id", func(m *Message) { m.ID = "" }, 0, true},
		{"missing project", func(m *Message) { m.ProjectID = "" }, 0, true},
		{"missing to", func(m *Message) { m.To = "" }, 0, true},
		{"unknown address kind", func(m *Message) { m.To = "group:x" }, 0, true},
		{"unknown state", func(m *Message) { m.State = "lost" }, 0, true},
		{"blank body", func(m *Message) { m.Body = "   " }, 0, true},
		{"oversize body", func(m *Message) { m.Body = strings.Repeat("x", 10) }, 5, true},
		{"body within limit", func(m *Message) { m.Body = "12345" }, 5, false},
		{"task disagreement", func(m *Message) { id := TicketID("t-2"); m.TaskID = &id }, 0, true},
		{"task address without task id", func(m *Message) { m.TaskID = nil }, 0, true},
		{"actor address needs no task", func(m *Message) { m.To = ActorAddress("act-1"); m.TaskID = nil }, 0, false},
		{"bad link", func(m *Message) { m.Links = []Link{{Kind: "bogus", URL: "u"}} }, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := validMessage()
			tt.mutate(&msg)
			if err := msg.Validate(tt.bodyLimit); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestMessageValidateInvalid(t *testing.T) {
	msg := validMessage()
	msg.ID = ""
	if err := msg.Validate(0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate() error = %v, want ErrInvalid", err)
	}
}

func TestRunValidateAndLive(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	run := Run{ID: "run-1", ProjectID: "prj-1", ActorID: "act-1", LeaseUntil: now.Add(time.Minute)}
	if err := run.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !run.Live(now) {
		t.Fatal("Live(now) = false, want true")
	}
	if run.Live(now.Add(2 * time.Minute)) {
		t.Fatal("Live(past lease) = true, want false")
	}

	tests := []struct {
		name string
		run  Run
	}{
		{"missing id", Run{ProjectID: "prj-1", ActorID: "act-1"}},
		{"missing project", Run{ID: "run-1", ActorID: "act-1"}},
		{"missing actor", Run{ID: "run-1", ProjectID: "prj-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run.Validate(); err == nil {
				t.Fatal("Validate() = nil, want error")
			}
		})
	}
}
