package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/harness/opencode"
	"github.com/khoinguyen/factotum/pkg/harness/pi"
)

func TestMessageInstallStagesReceiver(t *testing.T) {
	r := newRunner(t)
	cases := []struct {
		harness string
		bytes   []byte
	}{
		{"opencode", opencode.MsgPluginBytes()},
		{"pi", pi.MsgExtensionBytes()},
	}
	for _, tc := range cases {
		t.Run(tc.harness, func(t *testing.T) {
			dir := t.TempDir()
			out := r.run("msg", "install", "--harness", tc.harness, "--dir", dir)
			if !strings.Contains(out, "installed: true") || !strings.Contains(out, "harness: "+tc.harness) {
				t.Fatalf("install output:\n%s", out)
			}
			path := filepath.Join(dir, "factotum-msg.js")
			if !strings.Contains(out, "path: "+path) {
				t.Fatalf("install should print the staged path %s:\n%s", path, out)
			}
			if got := readFile(t, path); got != string(tc.bytes) {
				t.Fatalf("staged %s bytes do not match the embedded receiver", tc.harness)
			}
			// Idempotent: a second install overwrites in place.
			r.run("msg", "install", "--harness", tc.harness, "--dir", dir)
			if got := readFile(t, path); got != string(tc.bytes) {
				t.Fatalf("re-install changed the staged %s receiver", tc.harness)
			}
		})
	}
}

func TestMessageInstallRejectsUnknownHarness(t *testing.T) {
	r := newRunner(t)
	if err := r.runErr("msg", "install", "--harness", "nope", "--dir", t.TempDir()); err == nil {
		t.Fatal("install --harness nope should fail")
	}
}

func TestMessageInstallDefaultDir(t *testing.T) {
	getenv := func(key string) string {
		switch key {
		case "XDG_CONFIG_HOME":
			return "/cfg"
		case "HOME":
			return "/home/u"
		}
		return ""
	}
	got, err := receiverDir("opencode", getenv, "/work")
	if err != nil {
		t.Fatalf("receiverDir(opencode) error = %v", err)
	}
	if want := filepath.Join("/cfg", "opencode", "plugins"); got != want {
		t.Fatalf("receiverDir(opencode) = %q, want %q", got, want)
	}

	// With no XDG_CONFIG_HOME the XDG default under HOME applies.
	homeOnly := func(key string) string {
		if key == "HOME" {
			return "/home/u"
		}
		return ""
	}
	got, err = receiverDir("opencode", homeOnly, "/work")
	if err != nil {
		t.Fatalf("receiverDir(opencode, HOME) error = %v", err)
	}
	if want := filepath.Join("/home/u", ".config", "opencode", "plugins"); got != want {
		t.Fatalf("receiverDir(opencode, HOME) = %q, want %q", got, want)
	}

	// pi loads project-local extensions from the agent's working directory.
	got, err = receiverDir("pi", getenv, "/work")
	if err != nil {
		t.Fatalf("receiverDir(pi) error = %v", err)
	}
	if want := filepath.Join("/work", ".pi", "extensions"); got != want {
		t.Fatalf("receiverDir(pi) = %q, want %q", got, want)
	}
}

func TestMessageSendRoleTargetAndFrom(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))

	// A bare role is sugar for actor:<role>, and a role sender needs no
	// pre-registered actor: the loop registers actors on demand.
	sent := r.run("msg", "send", "reviewer-t-1", "please review", "--project", projectID, "--from", "builder-t-1")
	if !strings.Contains(sent, "to: actor:reviewer-t-1") {
		t.Fatalf("send to a role should address actor:reviewer-t-1:\n%s", sent)
	}
	messageID := firstField(t, sent)
	got := r.run("msg", "get", messageID)
	if !strings.Contains(got, "from: builder-t-1") {
		t.Fatalf("get should show the role sender:\n%s", got)
	}

	// A non-role, unregistered sender still fails loudly, so a typo is not
	// silently accepted.
	if err := r.runErr("msg", "send", "actor:claude", "hi", "--project", projectID, "--from", "nobody-here"); err == nil || !strings.Contains(err.Error(), "sender actor") {
		t.Fatalf("non-role unknown sender error = %v, want a sender actor error", err)
	}
}

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
