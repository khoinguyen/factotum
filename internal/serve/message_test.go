package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
)

// postMsg sends a JSON body to the message transport with the token in the
// Authorization header, the only credential the endpoint accepts.
func postMsg(t *testing.T, target, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	return resp
}

// getMsg fetches a message-transport URL with the token header.
func getMsg(t *testing.T, target, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	return resp
}

func decodeMsg[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("Decode(%T) error = %v", new(T), err)
	}
	return out
}

// msgServer is a scoped, token-gated serve instance with the message transport
// enabled.
func msgServer(t *testing.T, f *fixture, project core.ProjectID) *httptest.Server {
	t.Helper()
	return newTestServer(t, f, Options{Project: project, Token: testToken, Tasks: f.tasks, Messages: f.messages})
}

// TestMsgSendInboxReadOverTransport is the acceptance path: a client sends a
// message, lists it in the inbox, and marks it read, all over HTTP against the
// project backend (no local store access).
func TestMsgSendInboxReadOverTransport(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := msgServer(t, f, project.ID)

	sent := decodeMsg[msgSendResponse](t, postMsg(t, ts.URL+"/api/msg/send", testToken,
		`{"from":"claude","to":"actor:bob","body":"hello remote"}`))
	if !sent.Sent || sent.State != string(core.MessageQueued) {
		t.Fatalf("send = %+v, want sent queued", sent)
	}
	if sent.ID == "" || sent.Project != string(project.ID) {
		t.Fatalf("send = %+v, want an id in %s", sent, project.ID)
	}

	inbox := decodeMsg[msgInboxResponse](t, getMsg(t, ts.URL+"/api/msg/inbox", testToken))
	if len(inbox.Messages) != 1 {
		t.Fatalf("inbox has %d messages, want 1: %+v", len(inbox.Messages), inbox)
	}
	got := inbox.Messages[0]
	if got.ID != sent.ID || got.Body != "hello remote" || got.From != "claude" || got.To != "actor:bob" {
		t.Fatalf("inbox message = %+v, want the sent message", got)
	}

	read := decodeMsg[msgReadResponse](t, postMsg(t, ts.URL+"/api/msg/read", testToken,
		`{"id":"`+sent.ID+`"}`))
	if !read.Read || read.State != string(core.MessageRead) {
		t.Fatalf("read = %+v, want read", read)
	}

	state := decodeMsg[msgInboxResponse](t, getMsg(t, ts.URL+"/api/msg/inbox?state=read", testToken))
	if len(state.Messages) != 1 || state.Messages[0].ID != sent.ID {
		t.Fatalf("state=read inbox = %+v, want the read message", state)
	}
	queued := decodeMsg[msgInboxResponse](t, getMsg(t, ts.URL+"/api/msg/inbox?state=queued", testToken))
	if len(queued.Messages) != 0 {
		t.Fatalf("state=queued inbox = %+v, want empty", queued)
	}
}

// TestMsgGetOverTransport proves the non-mutating fetch returns one message.
func TestMsgGetOverTransport(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := msgServer(t, f, project.ID)

	sent := decodeMsg[msgSendResponse](t, postMsg(t, ts.URL+"/api/msg/send", testToken,
		`{"to":"actor:bob","body":"find me"}`))
	fetched := decodeMsg[msgGetResponse](t, getMsg(t, ts.URL+"/api/msg/get/"+sent.ID, testToken))
	if fetched.Message.ID != sent.ID || fetched.Message.Body != "find me" {
		t.Fatalf("get = %+v, want the sent message", fetched.Message)
	}
	if fetched.Message.State != string(core.MessageQueued) {
		t.Fatalf("get changed state to %q, want queued", fetched.Message.State)
	}
}

// TestMsgRegisterClaimAckOverTransport proves the receiver protocol rides the
// same transport: register a run, claim a message addressed to its actor, ack it.
func TestMsgRegisterClaimAckOverTransport(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := msgServer(t, f, project.ID)

	sent := decodeMsg[msgSendResponse](t, postMsg(t, ts.URL+"/api/msg/send", testToken,
		`{"to":"actor:bob","body":"for the receiver"}`))

	registered := decodeMsg[msgRegisterResponse](t, postMsg(t, ts.URL+"/api/msg/register", testToken,
		`{"actor":"bob","harness":"opencode","host":"host-a","pid":42}`))
	if registered.RunID == "" {
		t.Fatalf("register = %+v, want a run id", registered)
	}

	claimed := decodeMsg[msgClaimResponse](t, postMsg(t, ts.URL+"/api/msg/claim", testToken,
		`{"run_id":"`+registered.RunID+`","actor":"bob"}`))
	if !claimed.Found || claimed.Message == nil || claimed.Message.ID != sent.ID {
		t.Fatalf("claim = %+v, want message %s", claimed, sent.ID)
	}
	if claimed.Message.State != string(core.MessageDelivered) {
		t.Fatalf("claimed state = %q, want delivered", claimed.Message.State)
	}

	acked := decodeMsg[msgOKResponse](t, postMsg(t, ts.URL+"/api/msg/ack", testToken,
		`{"id":"`+sent.ID+`","run_id":"`+registered.RunID+`","state":"read"}`))
	if !acked.OK {
		t.Fatalf("ack = %+v, want ok", acked)
	}

	empty := decodeMsg[msgClaimResponse](t, postMsg(t, ts.URL+"/api/msg/claim", testToken,
		`{"run_id":"`+registered.RunID+`","actor":"bob"}`))
	if empty.Found {
		t.Fatalf("second claim = %+v, want empty", empty)
	}

	if out := decodeMsg[msgOKResponse](t, postMsg(t, ts.URL+"/api/msg/heartbeat", testToken,
		`{"run_id":"`+registered.RunID+`"}`)); !out.OK {
		t.Fatalf("heartbeat = %+v, want ok", out)
	}
	if out := decodeMsg[msgOKResponse](t, postMsg(t, ts.URL+"/api/msg/deregister", testToken,
		`{"run_id":"`+registered.RunID+`"}`)); !out.OK {
		t.Fatalf("deregister = %+v, want ok", out)
	}
}

// TestMsgRequiresToken proves the gate is fail closed for every verb: a missing
// or wrong token is rejected and stores nothing.
func TestMsgRequiresToken(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := msgServer(t, f, project.ID)

	posts := map[string]string{
		"/api/msg/send":     `{"to":"actor:bob","body":"x"}`,
		"/api/msg/read":     `{"id":"msg-1"}`,
		"/api/msg/ack":      `{"id":"msg-1","run_id":"run-1","state":"read"}`,
		"/api/msg/register": `{"actor":"bob"}`,
	}
	for token := range map[string]bool{"": true, "wrong": true} {
		for path, body := range posts {
			resp := postMsg(t, ts.URL+path, token, body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("POST %s token %q status = %d, want 401", path, token, resp.StatusCode)
			}
		}
		resp := getMsg(t, ts.URL+"/api/msg/inbox", token)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("GET inbox token %q status = %d, want 401", token, resp.StatusCode)
		}
	}

	inbox := decodeMsg[msgInboxResponse](t, getMsg(t, ts.URL+"/api/msg/inbox", testToken))
	if len(inbox.Messages) != 0 {
		t.Fatalf("rejected writes stored %d messages, want 0", len(inbox.Messages))
	}
}

// TestMsgQueryTokenIsRejected proves the token is header-only on the message
// transport too, so it cannot leak through logs or a Referer header.
func TestMsgQueryTokenIsRejected(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := msgServer(t, f, project.ID)

	resp := postMsg(t, ts.URL+"/api/msg/send?token="+testToken, "", `{"to":"actor:bob","body":"x"}`)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("query-token status = %d, want 401", resp.StatusCode)
	}
}

// TestMsgDisabledWithoutToken proves the transport fails closed: with no
// configured token it refuses rather than allowing anonymous access.
func TestMsgDisabledWithoutToken(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Tasks: f.tasks, Messages: f.messages})

	resp := postMsg(t, ts.URL+"/api/msg/send", testToken, `{"to":"actor:bob","body":"x"}`)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 when the transport is not configured", resp.StatusCode)
	}
}

// TestMsgDisabledForAllProjects proves the transport needs one scoped project:
// all-projects serving has no single backend to message through.
func TestMsgDisabledForAllProjects(t *testing.T) {
	f := newFixture(t)
	f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{All: true, Token: testToken, Tasks: f.tasks, Messages: f.messages})

	resp := postMsg(t, ts.URL+"/api/msg/send", testToken, `{"to":"actor:bob","body":"x"}`)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 in all-projects mode", resp.StatusCode)
	}
}

// TestMsgSendRejectsBadRequests proves the boundary validates before storing:
// unknown targets and empty bodies are 400, not stored messages.
func TestMsgSendRejectsBadRequests(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := msgServer(t, f, project.ID)

	cases := []struct {
		name string
		body string
	}{
		{"unknown address kind", `{"to":"bogus:bob","body":"x"}`},
		{"empty address id", `{"to":"actor:","body":"x"}`},
		{"empty target", `{"to":"   ","body":"x"}`},
		{"empty body", `{"to":"actor:bob","body":"   "}`},
		{"malformed json", `not json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postMsg(t, ts.URL+"/api/msg/send", testToken, tc.body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
		})
	}

	inbox := decodeMsg[msgInboxResponse](t, getMsg(t, ts.URL+"/api/msg/inbox", testToken))
	if len(inbox.Messages) != 0 {
		t.Fatalf("bad sends stored %d messages, want 0", len(inbox.Messages))
	}
}

// TestMsgGetMissingIsNotFound proves a fetch for an unknown id is a clean 404.
func TestMsgGetMissingIsNotFound(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := msgServer(t, f, project.ID)

	resp := getMsg(t, ts.URL+"/api/msg/get/msg-nope", testToken)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestMsgMethodNotAllowed proves each verb enforces its method.
func TestMsgMethodNotAllowed(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := msgServer(t, f, project.ID)

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/msg/send", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
	if got := resp.Header.Get("Allow"); !strings.Contains(got, "POST") {
		t.Fatalf("Allow = %q, want POST", got)
	}
}

// TestMsgItemShapePinsKeys pins the inbox/get item JSON to the documented
// superset, so dropping a key (project, body, ...) or drifting from the shape a
// remote client parses fails loudly instead of only in the server's decoders.
func TestMsgItemShapePinsKeys(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	task := f.addTask(t, project.ID, "work")
	ts := msgServer(t, f, project.ID)

	root := decodeMsg[msgSendResponse](t, postMsg(t, ts.URL+"/api/msg/send", testToken,
		`{"from":"alice","to":"actor:bob","body":"root"}`))
	sent := decodeMsg[msgSendResponse](t, postMsg(t, ts.URL+"/api/msg/send", testToken,
		`{"from":"alice","to":"task:`+string(task.ID)+`","body":"child","reply_to":"`+root.ID+`"}`))

	want := []string{"body", "created_at", "from", "id", "project", "reply_to", "state", "task_id", "to"}

	inbox := decodeMsg[struct {
		Messages []map[string]any `json:"messages"`
	}](t, getMsg(t, ts.URL+"/api/msg/inbox", testToken))
	var item map[string]any
	for _, message := range inbox.Messages {
		if message["id"] == sent.ID {
			item = message
		}
	}
	if item == nil {
		t.Fatalf("inbox did not contain %s: %+v", sent.ID, inbox.Messages)
	}
	if keys := sortedKeys(item); !slices.Equal(keys, want) {
		t.Fatalf("inbox item keys = %v, want %v", keys, want)
	}

	get := decodeMsg[struct {
		Message map[string]any `json:"message"`
	}](t, getMsg(t, ts.URL+"/api/msg/get/"+sent.ID, testToken))
	if keys := sortedKeys(get.Message); !slices.Equal(keys, want) {
		t.Fatalf("get item keys = %v, want %v", keys, want)
	}
}

func sortedKeys(obj map[string]any) []string {
	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
