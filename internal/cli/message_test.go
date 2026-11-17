package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/harness/opencode"
	"github.com/khoinguyen/factotum/pkg/harness/pi"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/jsonfile"
)

func TestMessageInstallStagesReceiver(t *testing.T) {
	r := newRunner(t)
	cases := []struct {
		harness string
		bytes   []byte
	}{
		{"opencode", opencode.MsgPluginBytes()},
		{"opencode2", opencode.MsgPluginBytes()},
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
			// Idempotent: a second install leaves the current receiver in place.
			r.run("msg", "install", "--harness", tc.harness, "--dir", dir)
			if got := readFile(t, path); got != string(tc.bytes) {
				t.Fatalf("re-install changed the staged %s receiver", tc.harness)
			}
		})
	}
}

// TestMessageInstallSkipsRewriteWhenCurrent pins the no-op: when the staged
// receiver already matches the embedded source, install must not write it, so
// OpenCode's plugin file-watcher never sees a change. A rewrite reloads the
// receiver in every running server and strands an idle agent's claim loop (the
// bug this task fixes). The mtime is first set to a known past value, so an
// unchanged mtime proves no write happened without depending on clock
// granularity.
func TestMessageInstallSkipsRewriteWhenCurrent(t *testing.T) {
	r := newRunner(t)
	dir := t.TempDir()
	r.run("msg", "install", "--harness", opencode.Name, "--dir", dir)
	path := filepath.Join(dir, receiverFile)

	stamp := time.Unix(1_600_000_000, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	r.run("msg", "install", "--harness", opencode.Name, "--dir", dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.ModTime().Equal(stamp) {
		t.Fatalf("install rewrote a current receiver: mtime = %v, want %v", info.ModTime(), stamp)
	}

	// A staged file that differs is still replaced, so a stale receiver is
	// never left in place.
	if err := os.WriteFile(path, []byte("stale receiver"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	r.run("msg", "install", "--harness", opencode.Name, "--dir", dir)
	if got := readFile(t, path); got != string(opencode.MsgPluginBytes()) {
		t.Fatal("install did not replace a stale receiver")
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

// TestMessageInstallVocabAliasesV2Receiver pins that the V2 harness key stages
// the same OpenCode receiver into the same discovery directory: OpenCode V2
// loads global plugins from <config>/opencode/plugins (verified against
// opencode v2.0.27), so no separate staging dir is needed.
func TestMessageInstallVocabAliasesV2Receiver(t *testing.T) {
	getenv := func(key string) string {
		if key == "XDG_CONFIG_HOME" {
			return "/cfg"
		}
		return ""
	}
	got, err := receiverDir("opencode2", getenv, "/work")
	if err != nil {
		t.Fatalf("receiverDir(opencode2) error = %v", err)
	}
	if want := filepath.Join("/cfg", "opencode", "plugins"); got != want {
		t.Fatalf("receiverDir(opencode2) = %q, want %q", got, want)
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

// TestMessageSendFromAcceptsBareAndCanonicalActorRefs pins --from to accept a
// bare actor id and the canonical actor:<id> address as the same sender, and to
// reject a reference it cannot resolve rather than store it verbatim.
func TestMessageSendFromAcceptsBareAndCanonicalActorRefs(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))

	for _, ref := range []string{"builder-t-1", "actor:builder-t-1"} {
		sent := r.run("msg", "send", "actor:claude", "please review", "--project", projectID, "--from", ref)
		messageID := firstField(t, sent)
		if got := r.run("msg", "get", messageID); !strings.Contains(got, "from: builder-t-1") {
			t.Fatalf("--from %q stored sender:\n%s\nwant from: builder-t-1", ref, got)
		}
	}

	// A colon-bearing actor name is not an address: it still resolves by name,
	// so narrowing the rejection must not break it.
	actorID := firstField(t, r.run("actor", "create", "team:alice", "--kind", "agent"))
	if actorID != "team-alice" {
		t.Fatalf("actor id = %q, want team-alice", actorID)
	}
	sent := r.run("msg", "send", "actor:claude", "hi", "--project", projectID, "--from", "team:alice")
	if got := r.run("msg", "get", firstField(t, sent)); !strings.Contains(got, "from: team-alice") {
		t.Fatalf("--from with a colon-bearing name:\n%s\nwant from: team-alice", got)
	}

	for _, ref := range []string{"actor:actor:chief", "run:r-1", "actor:"} {
		if err := r.runErr("msg", "send", "actor:claude", "hi", "--project", projectID, "--from", ref); err == nil || !strings.Contains(err.Error(), "sender actor") {
			t.Fatalf("--from %q error = %v, want a sender actor error", ref, err)
		}
	}
}

// TestMessageInboxForAcceptsCanonicalActorAddress pins --for to the same rule:
// a bare id and its canonical address filter the same mailbox.
func TestMessageInboxForAcceptsCanonicalActorAddress(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("msg", "send", "actor:chief", "hello", "--project", projectID)

	for _, ref := range []string{"chief", "actor:chief"} {
		if out := r.run("msg", "inbox", "--project", projectID, "--for", ref); !strings.Contains(out, "actor:chief") {
			t.Fatalf("inbox --for %q:\n%s\nwant it to list the message to actor:chief", ref, out)
		}
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
	if !strings.Contains(read, "read: true") || !strings.Contains(read, "hello there") {
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

// TestMessageReadShowsMessageAndAcks pins the shape of `ft msg read`: it shows
// the message `ft msg get` shows (from, task, reply_to, body) behind the `read`
// ack line, in both text and -o json, so a caller that acks a message never has
// to follow up with `ft msg get` to see what it said.
func TestMessageReadShowsMessageAndAcks(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "--project", projectID, "--title", "work"))
	first := firstField(t, r.run("msg", "send", "task:"+taskID, "first", "--project", projectID, "--from", "builder-t-1"))
	second := firstField(t, r.run("msg", "send", "task:"+taskID, "the body text", "--project", projectID, "--from", "builder-t-1", "--reply-to", first))

	stamp := regexp.MustCompile(`(?m)^created_at: .+$`)
	gotText := stamp.ReplaceAllString(strings.TrimSpace(r.run("msg", "read", second)), "created_at: <ts>")
	wantText := fmt.Sprintf(`read: true
(message) %s: read
from: builder-t-1
to: task:%s
task: %s
reply_to: %s
project: %s
created_at: <ts>

=== Body ===
the body text`, second, taskID, taskID, first, projectID)
	if gotText != wantText {
		t.Fatalf("read text output:\n%s\nwant:\n%s", gotText, wantText)
	}

	var gotJSON map[string]any
	out := r.run("msg", "read", second, "-o", "json")
	if err := json.Unmarshal([]byte(out), &gotJSON); err != nil {
		t.Fatalf("read -o json is not JSON: %v\n%s", err, out)
	}
	if _, err := time.Parse(time.RFC3339, fmt.Sprint(gotJSON["created_at"])); err != nil {
		t.Fatalf("read -o json created_at = %v, want an RFC3339 timestamp", gotJSON["created_at"])
	}
	delete(gotJSON, "created_at")
	wantJSON := map[string]any{
		"id":       second,
		"read":     true,
		"from":     "builder-t-1",
		"to":       "task:" + taskID,
		"task_id":  taskID,
		"reply_to": first,
		"state":    "read",
		"body":     "the body text",
		"project":  projectID,
	}
	if !reflect.DeepEqual(gotJSON, wantJSON) {
		t.Fatalf("read -o json:\n%#v\nwant:\n%#v", gotJSON, wantJSON)
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

// TestMessageGetJSONCarriesContent pins `ft msg get -o json` to the same
// message a human reads in the text block: a machine consumer needs the body
// and the reply edge, not just the inbox summary row.
func TestMessageGetJSONCarriesContent(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	first := firstField(t, r.run("msg", "send", "actor:claude", "first", "--project", projectID))
	second := firstField(t, r.run("msg", "send", "actor:claude", "the body text", "--project", projectID, "--reply-to", first))

	out := r.run("msg", "get", second, "-o", "json")
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("get -o json is not JSON: %v\n%s", err, out)
	}
	if got["body"] != "the body text" {
		t.Fatalf("get -o json body = %v, want the body text:\n%s", got["body"], out)
	}
	if got["reply_to"] != first {
		t.Fatalf("get -o json reply_to = %v, want %s:\n%s", got["reply_to"], first, out)
	}
	if got["id"] != second || got["to"] != "actor:claude" || got["state"] != "queued" || got["project"] != projectID {
		t.Fatalf("get -o json lost the summary fields:\n%s", out)
	}
}

// TestMessageGetShowsLinks pins that `ft msg get` and `ft msg read` show the
// attachments a sender added with --link, in the text block and in -o json/yaml.
// A link the store holds but no read surface prints is not a hand-off: the
// receiver never learns the artifact exists.
func TestMessageGetShowsLinks(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	messageID := firstField(t, r.run("msg", "send", "actor:linker", "see the issue", "--project", projectID,
		"--from", "builder-t-1", "--link", "issue=https://example.invalid/2", "--link", "doc=https://example.invalid/spec"))

	wantBlock := "\n=== Links ===\n[issue] https://example.invalid/2\n[doc] https://example.invalid/spec\n"
	if got := r.run("msg", "get", messageID); !strings.Contains(got, wantBlock) {
		t.Fatalf("get should print a Links section after the body:\n%s", got)
	}
	if got := r.run("msg", "read", messageID); !strings.Contains(got, wantBlock) {
		t.Fatalf("read should print the same Links section as get:\n%s", got)
	}

	var gotJSON map[string]any
	out := r.run("msg", "get", messageID, "-o", "json")
	if err := json.Unmarshal([]byte(out), &gotJSON); err != nil {
		t.Fatalf("get -o json is not JSON: %v\n%s", err, out)
	}
	assertMessageLinks(t, gotJSON["links"], "https://example.invalid/2", "https://example.invalid/spec")

	readJSON := runJSON(t, r, "msg", "read", messageID)
	assertMessageLinks(t, readJSON["links"], "https://example.invalid/2", "https://example.invalid/spec")

	yamlOut := r.run("msg", "get", messageID, "-o", "yaml")
	for _, want := range []string{"links:", "kind: issue", "url: https://example.invalid/2"} {
		if !strings.Contains(yamlOut, want) {
			t.Fatalf("get -o yaml missing %q:\n%s", want, yamlOut)
		}
	}
}

// TestMessageGetShowsLinkTitle pins the human block against a titled link.
// core.Link carries a Title (groom attaches "session <id>"), and JSON/yaml
// already print it, so the text block must not drop what the machine shapes
// carry - a reader otherwise loses the attachment's name. A link without a
// title keeps its exact previous line.
func TestMessageGetShowsLinkTitle(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	messageID := firstField(t, r.run("msg", "send", "actor:linker", "see the session", "--project", projectID))
	// ft msg send has no title flag; seed the attachment a peer CLI command
	// (groom) writes, then read it back through the normal get/read path.
	setMessageLinks(t, r.path, messageID, []core.Link{
		{Kind: core.LinkURL, URL: "https://example.invalid/session", Title: "session groom-1"},
		{Kind: core.LinkIssue, URL: "https://example.invalid/2"},
	})

	wantBlock := "\n=== Links ===\n" +
		"[url] https://example.invalid/session - session groom-1\n" +
		"[issue] https://example.invalid/2\n"
	if got := r.run("msg", "get", messageID); !strings.Contains(got, wantBlock) {
		t.Fatalf("get should print a link's title after its url:\n%s", got)
	}
	if got := r.run("msg", "read", messageID); !strings.Contains(got, wantBlock) {
		t.Fatalf("read should print the same titled link as get:\n%s", got)
	}
}

// setMessageLinks replaces a stored message's attachments, the one field
// `ft msg send` cannot set.
func setMessageLinks(t *testing.T, path, messageID string, links []core.Link) {
	t.Helper()
	ctx := context.Background()
	backend, err := jsonfile.Open(ctx, store.Config{Options: map[string]string{"path": path}})
	if err != nil {
		t.Fatalf("open store %s: %v", path, err)
	}
	defer func() { _ = backend.Close() }()
	message, err := backend.Messages().Get(ctx, core.MessageID(messageID))
	if err != nil {
		t.Fatalf("get message %s: %v", messageID, err)
	}
	message.Links = links
	if err := backend.Messages().Update(ctx, message); err != nil {
		t.Fatalf("update message %s: %v", messageID, err)
	}
}

// TestMessageInboxJSONCarriesLinks pins the list entry's links: a receiver that
// scans the inbox machine-readably must see the attachment without a follow-up
// get.
func TestMessageInboxJSONCarriesLinks(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	messageID := firstField(t, r.run("msg", "send", "actor:linker", "see the issue", "--project", projectID,
		"--link", "issue=https://example.invalid/2"))

	var entries []map[string]any
	out := r.run("msg", "inbox", "--project", projectID, "-o", "json")
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("inbox -o json is not a JSON array: %v\n%s", err, out)
	}
	if len(entries) != 1 || entries[0]["id"] != messageID {
		t.Fatalf("inbox -o json = %v, want the sent message %s", entries, messageID)
	}
	assertMessageLinks(t, entries[0]["links"], "https://example.invalid/2")
}

// TestMessageClaimWireCarriesLinks pins the receiver's claim payload: the
// plugin parses this JSON, so a link dropped here is a link no injected turn
// can show even though get and inbox are correct.
func TestMessageClaimWireCarriesLinks(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	messageID := firstField(t, r.run("msg", "send", "actor:linker", "see the issue", "--project", projectID,
		"--link", "issue=https://example.invalid/2"))

	registered := runJSON(t, r, "msg", "agent", "register", "--actor", "linker", "--harness", "opencode", "--project", projectID)
	runID, _ := registered["run_id"].(string)
	if runID == "" {
		t.Fatalf("register json missing run_id: %v", registered)
	}
	claimed := runJSON(t, r, "msg", "agent", "claim", "--run", runID, "--actor", "linker", "--project", projectID)
	if claimed["found"] != true {
		t.Fatalf("claim found = %v, want true: %v", claimed["found"], claimed)
	}
	message, ok := claimed["message"].(map[string]any)
	if !ok || message["id"] != messageID {
		t.Fatalf("claim message = %v, want %s", claimed["message"], messageID)
	}
	assertMessageLinks(t, message["links"], "https://example.invalid/2")
}

// TestMessagesWithoutLinksKeepTheirShape pins the other half: a message with no
// attachments prints no empty Links section and gains no links key, so the
// link fix does not churn every existing output.
func TestMessagesWithoutLinksKeepTheirShape(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	messageID := firstField(t, r.run("msg", "send", "actor:linker", "plain text", "--project", projectID))

	if got := r.run("msg", "get", messageID); strings.Contains(got, "Links") {
		t.Fatalf("a linkless message should print no Links section:\n%s", got)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(r.run("msg", "get", messageID, "-o", "json")), &detail); err != nil {
		t.Fatalf("get -o json is not JSON: %v", err)
	}
	if _, ok := detail["links"]; ok {
		t.Fatalf("a linkless get -o json should omit links: %v", detail)
	}

	var entries []map[string]any
	if err := json.Unmarshal([]byte(r.run("msg", "inbox", "--project", projectID, "-o", "json")), &entries); err != nil {
		t.Fatalf("inbox -o json is not a JSON array: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("inbox -o json = %v, want one entry", entries)
	}
	if _, ok := entries[0]["links"]; ok {
		t.Fatalf("a linkless inbox entry should omit links: %v", entries[0])
	}

	registered := runJSON(t, r, "msg", "agent", "register", "--actor", "linker", "--harness", "opencode", "--project", projectID)
	runID, _ := registered["run_id"].(string)
	claimed := runJSON(t, r, "msg", "agent", "claim", "--run", runID, "--actor", "linker", "--project", projectID)
	message, ok := claimed["message"].(map[string]any)
	if !ok {
		t.Fatalf("claim message = %v, want a message", claimed["message"])
	}
	if _, ok := message["links"]; ok {
		t.Fatalf("a linkless claim message should omit links: %v", message)
	}
}

// assertMessageLinks checks the links field of a read surface against the URLs
// the sender attached, in the {kind, url} shape every CLI link field uses.
func assertMessageLinks(t *testing.T, got any, urls ...string) {
	t.Helper()
	links, ok := got.([]any)
	if !ok {
		t.Fatalf("links = %v (%T), want an array", got, got)
	}
	if len(links) != len(urls) {
		t.Fatalf("links = %v, want %d", links, len(urls))
	}
	for i, url := range urls {
		link, ok := links[i].(map[string]any)
		if !ok {
			t.Fatalf("link %d = %v, want an object", i, links[i])
		}
		if link["url"] != url {
			t.Fatalf("link %d url = %v, want %s", i, link["url"], url)
		}
		if kind, _ := link["kind"].(string); kind == "" {
			t.Fatalf("link %d has no kind: %v", i, link)
		}
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

// TestMessageSendRefusesAmbiguousTicket pins the ticket rule at the CLI surface:
// the normal loop state has a builder and a reviewer live on the same task, so
// `ft msg send <ticket>` must refuse and name both actor addresses instead of
// delivering to whichever run id sorts first. The refusal stores nothing.
func TestMessageSendRefusesAmbiguousTicket(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "--project", projectID, "--title", "work"))
	builder, reviewer := "builder-"+taskID, "reviewer-"+taskID
	for i, actor := range []string{builder, reviewer} {
		runJSON(t, r, "msg", "agent", "register", "--actor", actor, "--task", taskID,
			"--pid", strconv.Itoa(i+1), "--project", projectID)
	}

	_, _, err := r.runSplitErr("msg", "send", taskID, "hand off", "--project", projectID)
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("ambiguous ticket send error = %v, want ErrInvalid", err)
	}
	for _, want := range []string{"actor:" + builder, "actor:" + reviewer, "ft msg send actor:"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ambiguous ticket send error = %v, want it to name %q", err, want)
		}
	}
	if inbox := r.run("msg", "inbox", "--project", projectID); strings.Contains(inbox, "hand off") {
		t.Fatalf("an ambiguous ticket send stored the message:\n%s", inbox)
	}
}

// TestMessageSendRefusalExampleNamesThePeerFromTheSender pins that guidance at
// the CLI surface: with --from set, the ambiguous-ticket example addresses the
// sender's counterpart, so a role that copies it verbatim reaches the peer and
// not itself.
func TestMessageSendRefusalExampleNamesThePeerFromTheSender(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "--project", projectID, "--title", "work"))
	builder, reviewer := "builder-"+taskID, "reviewer-"+taskID
	// Register the builder first: the lowest run id is the sender, so an example
	// chosen by run-id order would hand the builder its own address.
	for i, actor := range []string{builder, reviewer} {
		runJSON(t, r, "msg", "agent", "register", "--actor", actor, "--task", taskID,
			"--pid", strconv.Itoa(i+1), "--project", projectID)
	}

	_, _, err := r.runSplitErr("msg", "send", taskID, "hand off", "--project", projectID, "--from", "actor:"+builder)
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("ambiguous ticket send error = %v, want ErrInvalid", err)
	}
	if want := "ft msg send actor:" + reviewer; !strings.Contains(err.Error(), want) {
		t.Fatalf("ambiguous ticket send error = %v, want the example to name the peer %q", err, want)
	}
	if self := "ft msg send actor:" + builder; strings.Contains(err.Error(), self) {
		t.Fatalf("ambiguous ticket send error = %v, want the example not to name the sender %q", err, self)
	}
}

// TestMessageSendWarnsWhenTicketResolvesToMailboxWithoutLiveRun pins the other
// silent half of ticket sugar: when the ticket falls back to a mailbox (here the
// agent assignee) that has no live run, the send must say so on stderr, because
// until a receiver registers the message sits queued. A live run for the
// mailbox silences the warning.
func TestMessageSendWarnsWhenTicketResolvesToMailboxWithoutLiveRun(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	actorID := firstField(t, r.run("actor", "create", "claude", "--kind", "agent"))
	taskID := firstField(t, r.run("task", "create", "--project", projectID, "--title", "work"))
	r.run("task", "assign", taskID, "--actor", actorID)

	stdout, stderr := r.runSplit("msg", "send", taskID, "on it", "--project", projectID)
	if !strings.Contains(stdout, "to: actor:"+actorID) {
		t.Fatalf("send should resolve the ticket to the assignee mailbox:\n%s", stdout)
	}
	if !strings.Contains(stderr, "no live run") || !strings.Contains(stderr, "actor:"+actorID) {
		t.Fatalf("send stderr:\n%s\nwant a warning that no live run can claim actor:%s", stderr, actorID)
	}

	runJSON(t, r, "msg", "agent", "register", "--actor", actorID, "--project", projectID)
	_, stderr = r.runSplit("msg", "send", taskID, "again", "--project", projectID)
	if strings.Contains(stderr, "no live run") {
		t.Fatalf("send stderr:\n%s\nwant no warning once a live run can claim the mailbox", stderr)
	}
}
