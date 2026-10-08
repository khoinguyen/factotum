package serve

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

const testToken = "s3cret"

// noRedirectClient stops at the capture redirect, so a test can assert the 303
// and its Location instead of following into the idea page.
var noRedirectClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func postForm(t *testing.T, target string, form url.Values) *http.Response {
	t.Helper()
	resp, err := noRedirectClient.PostForm(target, form)
	if err != nil {
		t.Fatalf("PostForm(%s) error = %v", target, err)
	}
	return resp
}

func postBearer(t *testing.T, target, token, text string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(url.Values{"text": {text}}.Encode()))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	return resp
}

func ideas(t *testing.T, f *fixture, project core.ProjectID) []*core.Ticket {
	t.Helper()
	kind := core.KindIdea
	list, err := f.tasks.List(context.Background(), store.TicketFilter{ProjectID: project, Kind: &kind})
	if err != nil {
		t.Fatalf("List(ideas) error = %v", err)
	}
	return list
}

// TestCapturePageRendersForm proves the write side is a page of the same app,
// reachable at /capture and shaped for a phone: a text field and a token field
// posting back to /capture.
func TestCapturePageRendersForm(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	body := getBody(t, ts.URL+"/capture")
	for _, want := range []string{`action="/capture"`, `name="text"`, `name="token"`, "Capture"} {
		if !strings.Contains(body, want) {
			t.Fatalf("capture page missing %q:\n%s", want, body)
		}
	}
}

// TestCaptureRequiresToken proves the gate: a write with no token or a wrong
// token is rejected and stores nothing. The token is the whole auth boundary.
func TestCaptureRequiresToken(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	cases := []struct {
		name string
		form url.Values
	}{
		{"missing token", url.Values{"text": {"an idea"}}},
		{"wrong token", url.Values{"text": {"an idea"}, "token": {"nope"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postForm(t, ts.URL+"/capture", tc.form)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.StatusCode)
			}
		})
	}

	if got := ideas(t, f, project.ID); len(got) != 0 {
		t.Fatalf("rejected writes created %d ideas, want 0", len(got))
	}
}

// TestCaptureWithFormToken proves an authenticated form write stores an idea and
// redirects the browser to its detail page.
func TestCaptureWithFormToken(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	resp := postForm(t, ts.URL+"/capture", url.Values{"text": {"Add a dark mode"}, "token": {testToken}})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/idea/") {
		t.Fatalf("Location = %q, want /idea/<id>", loc)
	}

	task, err := f.tasks.Get(context.Background(), core.TicketID(strings.TrimPrefix(loc, "/idea/")))
	if err != nil {
		t.Fatalf("Get(captured) error = %v", err)
	}
	if task.Kind != core.KindIdea || task.Status != core.StatusTodo {
		t.Fatalf("captured task = %+v, want idea in todo", task)
	}
	if task.ProjectID != project.ID {
		t.Fatalf("captured project = %q, want %q", task.ProjectID, project.ID)
	}
	if task.Title != "Add a dark mode" {
		t.Fatalf("title = %q, want the sentence", task.Title)
	}
}

// TestCaptureAcceptsBearerToken proves a non-browser client can authenticate
// with the Authorization header instead of a form field.
func TestCaptureAcceptsBearerToken(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	resp := postBearer(t, ts.URL+"/capture", testToken, "From a script")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if got := ideas(t, f, project.ID); len(got) != 1 {
		t.Fatalf("captured %d ideas, want 1", len(got))
	}
}

// TestCaptureRejectsBearerLengthMismatch is the cheap companion to the
// constant-time comparison: a token that only shares a prefix never passes.
func TestCaptureRejectsBearerLengthMismatch(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	resp := postBearer(t, ts.URL+"/capture", testToken+"x", "sneaky")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

// TestCaptureRejectsEmptyText proves a capture with no idea text is a bad
// request, not an empty idea.
func TestCaptureRejectsEmptyText(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	for _, text := range []string{"", "   ", "\n\t"} {
		resp := postForm(t, ts.URL+"/capture", url.Values{"text": {text}, "token": {testToken}})
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("text %q status = %d, want 400", text, resp.StatusCode)
		}
	}
	if got := ideas(t, f, project.ID); len(got) != 0 {
		t.Fatalf("empty captures created %d ideas, want 0", len(got))
	}
}

// TestCaptureDisabledWithoutToken proves writes fail closed: with no configured
// token the endpoint refuses and the page says so, rather than allowing an
// anonymous write.
func TestCaptureDisabledWithoutToken(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Tasks: f.tasks})

	resp := postForm(t, ts.URL+"/capture", url.Values{"text": {"an idea"}})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 when capture is not configured", resp.StatusCode)
	}
	if got := ideas(t, f, project.ID); len(got) != 0 {
		t.Fatalf("disabled capture created %d ideas, want 0", len(got))
	}
	if body := getBody(t, ts.URL+"/capture"); !strings.Contains(body, "not configured") {
		t.Fatalf("capture page should say capture is not configured:\n%s", body)
	}
}

// TestCaptureDisabledForAllProjects proves capture needs one scoped project to
// attribute the idea to; all-projects serving has no capture target.
func TestCaptureDisabledForAllProjects(t *testing.T) {
	f := newFixture(t)
	one := f.addProject(t, "one", "One")
	ts := newTestServer(t, f, Options{All: true, Token: testToken, Tasks: f.tasks})

	resp := postForm(t, ts.URL+"/capture", url.Values{"text": {"an idea"}, "token": {testToken}})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 in all-projects mode", resp.StatusCode)
	}
	if got := ideas(t, f, one.ID); len(got) != 0 {
		t.Fatalf("all-projects capture created %d ideas, want 0", len(got))
	}
}

func TestCaptureMethodNotAllowed(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req, err := http.NewRequest(method, ts.URL+"/capture", strings.NewReader(""))
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

// TestCaptureDoesNotCloseReadSide proves the write gate never leaks onto reads:
// with a token configured, the dashboard is still open.
func TestCaptureDoesNotCloseReadSide(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	f.addTask(t, project.ID, "Fix the widget")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	for _, path := range []string{"/", "/fragment"} {
		if body := getBody(t, ts.URL+path); body == "" {
			t.Fatalf("GET %s returned an empty body", path)
		}
	}
}

// TestIdeaFromSentence pins the capture split: the first line becomes the
// title, the rest is kept verbatim as the body, and a single-line capture keeps
// the whole sentence so nothing typed is lost.
func TestIdeaFromSentence(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		title string
		body  string
	}{
		{"single line", "Add a dark mode", "Add a dark mode", "Add a dark mode"},
		{"multi line", "Add a dark mode\n\nUsers want it at night.", "Add a dark mode", "Users want it at night."},
		{"collapses title spacing", "Fix   the   widget", "Fix the widget", "Fix   the   widget"},
		{"trims blank edges", "\n\n  Ship the thing  \n\n", "Ship the thing", "Ship the thing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, body := ideaFromSentence(tc.raw)
			if title != tc.title || body != tc.body {
				t.Fatalf("ideaFromSentence(%q) = (%q, %q), want (%q, %q)", tc.raw, title, body, tc.title, tc.body)
			}
		})
	}
}

// TestCaptureTruncatesLongTitle keeps a whole paragraph from becoming an
// unbounded title while the raw body keeps every word.
func TestCaptureTruncatesLongTitle(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks})

	long := strings.Repeat("word ", 60)
	resp := postForm(t, ts.URL+"/capture", url.Values{"text": {long}, "token": {testToken}})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	task, err := f.tasks.Get(context.Background(), core.TicketID(strings.TrimPrefix(resp.Header.Get("Location"), "/idea/")))
	if err != nil {
		t.Fatalf("Get(captured) error = %v", err)
	}
	if len([]rune(task.Title)) > 100 {
		t.Fatalf("title has %d runes, want <= 100: %q", len([]rune(task.Title)), task.Title)
	}
	if task.Description != strings.TrimSpace(long) {
		t.Fatalf("description lost the raw sentence")
	}
}
