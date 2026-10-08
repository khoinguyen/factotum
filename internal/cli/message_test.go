package cli

import (
	"strings"
	"testing"
)

func TestMessageSendInboxRead(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))

	sent := r.run("msg", "send", "actor:claude", "hello there", "--project", projectID)
	if !strings.Contains(sent, "sent: true") || !strings.Contains(sent, "to: actor:claude") {
		t.Fatalf("send output:\n%s", sent)
	}
	messageID := firstField(t, sent)
	if messageID == "" {
		t.Fatal("send did not print a message id")
	}

	inbox := r.run("msg", "inbox", "--project", projectID)
	if !strings.Contains(inbox, messageID) || !strings.Contains(inbox, "actor:claude") {
		t.Fatalf("inbox output:\n%s", inbox)
	}

	got := r.run("msg", "get", messageID)
	if !strings.Contains(got, "to: actor:claude") || !strings.Contains(got, "hello there") {
		t.Fatalf("get output:\n%s", got)
	}

	read := r.run("msg", "read", messageID)
	if !strings.Contains(read, "read: true") || !strings.Contains(read, "state: read") {
		t.Fatalf("read output:\n%s", read)
	}

	queued := r.run("msg", "inbox", "--project", projectID, "--state", "queued")
	if strings.Contains(queued, messageID) {
		t.Fatalf("queued inbox should not contain the read message:\n%s", queued)
	}
}

func TestMessageReadJSONCarriesReadNotSent(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))

	sentJSON := r.run("msg", "send", "actor:claude", "hello", "--project", projectID, "-o", "json")
	if !strings.Contains(sentJSON, `"sent": true`) {
		t.Fatalf("send json missing sent:true:\n%s", sentJSON)
	}
	messageID := firstField(t, r.run("msg", "send", "actor:claude", "second", "--project", projectID))

	readJSON := r.run("msg", "read", messageID, "-o", "json")
	if !strings.Contains(readJSON, `"read": true`) || strings.Contains(readJSON, `"sent"`) {
		t.Fatalf("read json should carry read:true and no sent:\n%s", readJSON)
	}
}

func TestMessageSendReplyTo(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	first := firstField(t, r.run("msg", "send", "actor:claude", "first", "--project", projectID))
	second := firstField(t, r.run("msg", "send", "actor:claude", "second", "--project", projectID, "--reply-to", first))

	got := r.run("msg", "get", second)
	if !strings.Contains(got, "reply_to: "+first) {
		t.Fatalf("get should show reply_to %s:\n%s", first, got)
	}
}

func TestMessageSendResolvesTicket(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("actor", "create", "claude", "--kind", "agent")
	taskID := firstField(t, r.run("task", "create", "--project", projectID, "--title", "work"))

	sent := r.run("msg", "send", taskID, "on it", "--project", projectID)
	if !strings.Contains(sent, "sent: true") {
		t.Fatalf("send output:\n%s", sent)
	}
	inbox := r.run("msg", "inbox", "--project", projectID)
	if !strings.Contains(inbox, taskID) {
		t.Fatalf("inbox should carry the originating task:\n%s", inbox)
	}
}
