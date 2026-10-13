package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

const promptPath = "/api/prompt"

// postPrompt sends a JSON prompt write with the token in the Authorization
// header, the only credential the write side accepts.
func postPrompt(t *testing.T, target, token, body string) *http.Response {
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

func decodePrompt(t *testing.T, resp *http.Response) promptResult {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	var result promptResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Decode(prompt) error = %v", err)
	}
	return result
}

// prompts lists the prompt artifacts in a project so a test can assert a prompt
// stored exactly one, or none.
func prompts(t *testing.T, f *fixture, project core.ProjectID) []*core.Artifact {
	t.Helper()
	kind := core.ArtifactPrompt
	list, err := f.artifacts.List(context.Background(), store.ArtifactFilter{ProjectID: project, Kind: &kind})
	if err != nil {
		t.Fatalf("List(docs) error = %v", err)
	}
	return list
}

// inbox lists every message in a project so a test can assert the chief was told
// (or not).
func inbox(t *testing.T, f *fixture, project core.ProjectID) []*core.Message {
	t.Helper()
	list, err := f.messages.Inbox(context.Background(), app.InboxQuery{ProjectID: project})
	if err != nil {
		t.Fatalf("Inbox() error = %v", err)
	}
	return list
}

// newPromptServer is the common fixture: a scoped, token-gated server with the
// artifact and message services the prompt write needs.
func newPromptServer(t *testing.T, f *fixture, project core.ProjectID) *httptest.Server {
	t.Helper()
	return newTestServer(t, f, Options{
		Project:   project,
		Token:     testToken,
		Artifacts: f.artifacts,
		Messages:  f.messages,
	})
}

// TestPromptStoresPromptArtifact proves the trigger surface stores the prompt text
// as a document artifact: a title derived from the first line and the raw prompt
// kept as the body, in the scoped project.
func TestPromptStoresPromptArtifact(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newPromptServer(t, f, project.ID)

	resp := postPrompt(t, ts.URL+promptPath, testToken, `{"text":"Add a dark mode\n\nUsers want it at night."}`)
	if resp.StatusCode != http.StatusCreated {
		_ = resp.Body.Close()
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	result := decodePrompt(t, resp)
	if result.ID == "" || result.URL != "/doc/"+string(result.ID) {
		t.Fatalf("result = %+v, want an id and its /doc URL", result)
	}

	artifact, err := f.artifacts.Get(context.Background(), result.ID)
	if err != nil {
		t.Fatalf("Get(doc) error = %v", err)
	}
	if artifact.Kind != core.ArtifactPrompt {
		t.Fatalf("artifact kind = %q, want prompt", artifact.Kind)
	}
	if artifact.ProjectID != project.ID {
		t.Fatalf("artifact project = %q, want %q", artifact.ProjectID, project.ID)
	}
	if artifact.Title != "Add a dark mode" {
		t.Fatalf("artifact title = %q, want the first line", artifact.Title)
	}
	if artifact.Body != "Add a dark mode\n\nUsers want it at night." {
		t.Fatalf("artifact body = %q, want the whole prompt", artifact.Body)
	}
	if got := prompts(t, f, project.ID); len(got) != 1 {
		t.Fatalf("stored %d docs, want 1", len(got))
	}
}

// TestPromptNotifiesChief proves the write side ft-msgs the chief, naming the
// artifact it just stored so the chief can find it.
func TestPromptNotifiesChief(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newPromptServer(t, f, project.ID)

	resp := postPrompt(t, ts.URL+promptPath, testToken, `{"text":"Add a dark mode"}`)
	if resp.StatusCode != http.StatusCreated {
		_ = resp.Body.Close()
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	result := decodePrompt(t, resp)

	messages := inbox(t, f, project.ID)
	if len(messages) != 1 {
		t.Fatalf("sent %d messages, want 1", len(messages))
	}
	message := messages[0]
	if message.To != core.ActorAddress("chief") {
		t.Fatalf("message to = %q, want actor:chief", message.To)
	}
	if message.From != nil {
		t.Fatalf("message from = %v, want a system message", *message.From)
	}
	if !strings.Contains(message.Body, string(result.ID)) {
		t.Fatalf("message body %q does not name artifact %s", message.Body, result.ID)
	}
	if !strings.Contains(message.Body, "Add a dark mode") {
		t.Fatalf("message body %q does not name the prompt title", message.Body)
	}
	if len(message.Links) != 1 || message.Links[0].URL != "/doc/"+string(result.ID) {
		t.Fatalf("message links = %+v, want a doc link to the artifact", message.Links)
	}
}

// TestPromptRequiresToken proves the gate: a write with no token or a wrong
// token is rejected and touches neither the artifacts nor the message log.
func TestPromptRequiresToken(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newPromptServer(t, f, project.ID)

	for _, token := range []string{"", "nope"} {
		resp := postPrompt(t, ts.URL+promptPath, token, `{"text":"Add a dark mode"}`)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token %q status = %d, want 401", token, resp.StatusCode)
		}
	}
	if got := prompts(t, f, project.ID); len(got) != 0 {
		t.Fatalf("rejected writes created %d docs, want 0", len(got))
	}
	if got := inbox(t, f, project.ID); len(got) != 0 {
		t.Fatalf("rejected writes sent %d messages, want 0", len(got))
	}
}

// TestPromptRejectsEmptyText proves an empty prompt is a bad request, not an
// empty artifact or a message to the chief.
func TestPromptRejectsEmptyText(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newPromptServer(t, f, project.ID)

	for _, text := range []string{"", "   ", "\n\t"} {
		body, err := json.Marshal(promptRequest{Text: text})
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		resp := postPrompt(t, ts.URL+promptPath, testToken, string(body))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("text %q status = %d, want 400", text, resp.StatusCode)
		}
	}
	if got := prompts(t, f, project.ID); len(got) != 0 {
		t.Fatalf("empty prompts created %d docs, want 0", len(got))
	}
	if got := inbox(t, f, project.ID); len(got) != 0 {
		t.Fatalf("empty prompts sent %d messages, want 0", len(got))
	}
}

// TestPromptRejectsMalformedBody proves a non-JSON body is a bad request, not a
// panic or a 500.
func TestPromptRejectsMalformedBody(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newPromptServer(t, f, project.ID)

	resp := postPrompt(t, ts.URL+promptPath, testToken, "not json")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestPromptTruncatesLongTitle keeps a whole paragraph from becoming an
// unbounded title while the artifact body keeps every word.
func TestPromptTruncatesLongTitle(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newPromptServer(t, f, project.ID)

	long := strings.Repeat("word ", 60)
	body, err := json.Marshal(promptRequest{Text: long})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	resp := postPrompt(t, ts.URL+promptPath, testToken, string(body))
	if resp.StatusCode != http.StatusCreated {
		_ = resp.Body.Close()
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	result := decodePrompt(t, resp)
	artifact, err := f.artifacts.Get(context.Background(), result.ID)
	if err != nil {
		t.Fatalf("Get(doc) error = %v", err)
	}
	if len([]rune(artifact.Title)) > captureTitleMax {
		t.Fatalf("title has %d runes, want <= %d: %q", len([]rune(artifact.Title)), captureTitleMax, artifact.Title)
	}
	if artifact.Body != strings.TrimSpace(long) {
		t.Fatalf("artifact body lost the raw prompt")
	}
}

// TestPromptDisabledWithoutToken proves writes fail closed: with no configured
// token the endpoint refuses rather than letting an anonymous prompt through.
func TestPromptDisabledWithoutToken(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{
		Project: project.ID, Artifacts: f.artifacts, Messages: f.messages,
	})

	resp := postPrompt(t, ts.URL+promptPath, testToken, `{"text":"Add a dark mode"}`)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 when prompt is not configured", resp.StatusCode)
	}
	if got := inbox(t, f, project.ID); len(got) != 0 {
		t.Fatalf("disabled prompt sent %d messages, want 0", len(got))
	}
}

// TestPromptDisabledForAllProjects proves prompt needs one scoped project to
// attribute the artifact to; all-projects serving has no prompt target.
func TestPromptDisabledForAllProjects(t *testing.T) {
	f := newFixture(t)
	one := f.addProject(t, "one", "One")
	ts := newTestServer(t, f, Options{
		All: true, Token: testToken, Artifacts: f.artifacts, Messages: f.messages,
	})

	resp := postPrompt(t, ts.URL+promptPath, testToken, `{"text":"Add a dark mode"}`)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 in all-projects mode", resp.StatusCode)
	}
	if got := prompts(t, f, one.ID); len(got) != 0 {
		t.Fatalf("all-projects prompt created %d docs, want 0", len(got))
	}
}

// TestPromptMethodNotAllowed proves only POST writes; the read methods the
// capture config uses are not accepted for a prompt.
func TestPromptMethodNotAllowed(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newPromptServer(t, f, project.ID)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req, err := http.NewRequest(method, ts.URL+promptPath, strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("NewRequest(%s) error = %v", method, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s error = %v", method, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want 405", method, resp.StatusCode)
		}
		if got := resp.Header.Get("Allow"); !strings.Contains(got, "POST") {
			t.Fatalf("%s Allow = %q, want POST", method, got)
		}
	}
}

// TestPromptArtifactRetrievable proves the stored prompt is viewable at its doc
// page: the existing /api/doc route resolves the doc artifact the write stored.
func TestPromptArtifactRetrievable(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newPromptServer(t, f, project.ID)

	resp := postPrompt(t, ts.URL+promptPath, testToken, `{"text":"Add a dark mode"}`)
	result := decodePrompt(t, resp)

	var doc struct {
		ID    string `json:"id"`
		Kind  string `json:"kind"`
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	getDoc(t, ts.URL+"/api/doc/"+string(result.ID), &doc)
	if doc.ID != string(result.ID) || doc.Kind != "prompt" || doc.Title != "Add a dark mode" || doc.Body != "Add a dark mode" {
		t.Fatalf("doc = %+v, want the stored prompt", doc)
	}
}

// TestPromptDoesNotCloseReadSide proves the prompt gate never leaks onto reads:
// with a token configured, the dashboard is still open.
func TestPromptDoesNotCloseReadSide(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newPromptServer(t, f, project.ID)

	for _, path := range []string{"/", "/api/snapshot"} {
		if body := getBody(t, ts.URL+path); body == "" {
			t.Fatalf("GET %s returned an empty body", path)
		}
	}
}
