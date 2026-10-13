package serve

import (
	"context"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

// linkCapture records a capture's provenance back to a prompt artifact: a note
// whose doc link names the prompt's /doc page. It is the compat link-back the
// read side keeps honoring alongside the canonical origin_artifact.
func linkCapture(t *testing.T, f *fixture, capture *core.Ticket, artifact *core.Artifact) {
	t.Helper()
	if _, err := f.tasks.AddNote(context.Background(), capture.ID, app.NoteInput{
		Body:  "Groomed from " + string(artifact.ID),
		Links: []core.Link{{Kind: core.LinkDoc, URL: artifactURL(artifact)}},
	}); err != nil {
		t.Fatalf("AddNote(%s) error = %v", capture.ID, err)
	}
}

// addCaptureFromPrompt records a capture the way the grooming agent does: its
// origin_artifact names the prompt, and it carries no note. This is the real
// link-back the read surface must project.
func addCaptureFromPrompt(t *testing.T, f *fixture, project core.ProjectID, kind core.TicketKind, title string, prompt *core.Artifact) *core.Ticket {
	t.Helper()
	task, err := f.tasks.Add(context.Background(), app.TicketInput{
		ProjectID:      project,
		Kind:           kind,
		Title:          title,
		OriginArtifact: &prompt.ID,
	})
	if err != nil {
		t.Fatalf("Add(%s %q) error = %v", kind, title, err)
	}
	return task
}

// TestNotificationsEmptyWithoutPrompts proves the feed is empty when nothing has
// been captured from the web app.
func TestNotificationsEmptyWithoutPrompts(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newTestServer(t, f, Options{Project: project.ID})

	var doc notificationsDoc
	getDoc(t, ts.URL+"/api/notifications", &doc)
	if len(doc.Notifications) != 0 {
		t.Fatalf("notifications = %+v, want none", doc.Notifications)
	}
}

// TestNotificationNeedsLinkedCapture proves a prompt is not "groomed complete"
// until a capture links back to it, and that the link flips it on: the toast the
// web app shows must not fire for a bare, un-groomed prompt.
func TestNotificationNeedsLinkedCapture(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newPromptServer(t, f, project.ID)

	resp := postPrompt(t, ts.URL+promptPath, testToken, `{"text":"Rework onboarding"}`)
	result := decodePrompt(t, resp)

	var before notificationsDoc
	getDoc(t, ts.URL+"/api/notifications", &before)
	if len(before.Notifications) != 0 {
		t.Fatalf("ungroomed prompt produced %+v, want none", before.Notifications)
	}

	artifact, err := f.artifacts.Get(context.Background(), result.ID)
	if err != nil {
		t.Fatalf("Get(prompt) error = %v", err)
	}
	capture := f.addIdea(t, project.ID, "Guided first run", "")
	linkCapture(t, f, capture, artifact)

	var after notificationsDoc
	getDoc(t, ts.URL+"/api/notifications", &after)
	if len(after.Notifications) != 1 {
		t.Fatalf("notifications = %+v, want one", after.Notifications)
	}
	got := after.Notifications[0]
	if got.ID != result.ID {
		t.Fatalf("notification id = %q, want the prompt artifact %q", got.ID, result.ID)
	}
	if got.URL != "/doc/"+string(result.ID) {
		t.Fatalf("notification url = %q, want the prompt artifact page", got.URL)
	}
	if got.Title != "Rework onboarding" {
		t.Fatalf("notification title = %q, want the prompt title", got.Title)
	}
	if got.Captures != 1 {
		t.Fatalf("notification captures = %d, want 1", got.Captures)
	}
}

// TestNotificationCountsEveryLinkedCapture proves one prompt groomed into many
// captures still yields a single notification, with the count of linked
// captures: the app opens one page, never a branch on the count.
func TestNotificationCountsEveryLinkedCapture(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newPromptServer(t, f, project.ID)

	resp := postPrompt(t, ts.URL+promptPath, testToken, `{"text":"Rework onboarding"}`)
	result := decodePrompt(t, resp)
	artifact, err := f.artifacts.Get(context.Background(), result.ID)
	if err != nil {
		t.Fatalf("Get(prompt) error = %v", err)
	}

	first := f.addIdea(t, project.ID, "Guided first run", "")
	second := f.addIdea(t, project.ID, "Skip the tour", "")
	linkCapture(t, f, first, artifact)
	linkCapture(t, f, second, artifact)

	var doc notificationsDoc
	getDoc(t, ts.URL+"/api/notifications", &doc)
	if len(doc.Notifications) != 1 {
		t.Fatalf("notifications = %+v, want exactly one", doc.Notifications)
	}
	if doc.Notifications[0].Captures != 2 {
		t.Fatalf("notification captures = %d, want 2", doc.Notifications[0].Captures)
	}
}

// TestNotificationIgnoresUnrelatedLinks proves only links back to the prompt
// artifact count: a capture linked at some other document is not a completion of
// this prompt's grooming.
func TestNotificationIgnoresUnrelatedLinks(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newPromptServer(t, f, project.ID)

	resp := postPrompt(t, ts.URL+promptPath, testToken, `{"text":"Rework onboarding"}`)
	result := decodePrompt(t, resp)

	other, err := f.artifacts.Add(context.Background(), app.ArtifactInput{
		ProjectID: project.ID, Kind: core.ArtifactSpec, Title: "An unrelated spec",
	})
	if err != nil {
		t.Fatalf("Add(spec) error = %v", err)
	}
	capture := f.addIdea(t, project.ID, "Guided first run", "")
	linkCapture(t, f, capture, other)

	var doc notificationsDoc
	getDoc(t, ts.URL+"/api/notifications", &doc)
	if len(doc.Notifications) != 0 {
		t.Fatalf("notifications = %+v, want none for prompt %s", doc.Notifications, result.ID)
	}
}

// TestArtifactPageListsLinkedCaptures proves the prompt artifact page is the one
// UI for single or many: it carries the captures linked back to the prompt, and
// a plain document with no linked capture carries none.
func TestArtifactPageListsLinkedCaptures(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newPromptServer(t, f, project.ID)

	resp := postPrompt(t, ts.URL+promptPath, testToken, `{"text":"Rework onboarding"}`)
	result := decodePrompt(t, resp)
	artifact, err := f.artifacts.Get(context.Background(), result.ID)
	if err != nil {
		t.Fatalf("Get(prompt) error = %v", err)
	}
	capture := f.addIdea(t, project.ID, "Guided first run", "")
	linkCapture(t, f, capture, artifact)

	var page struct {
		Captures []taskLink `json:"captures"`
	}
	getDoc(t, ts.URL+"/api/doc/"+string(result.ID), &page)
	if len(page.Captures) != 1 {
		t.Fatalf("captures = %+v, want one", page.Captures)
	}
	if page.Captures[0].ID != capture.ID {
		t.Fatalf("capture id = %q, want %q", page.Captures[0].ID, capture.ID)
	}
	if page.Captures[0].URL != "/idea/"+string(capture.ID) {
		t.Fatalf("capture url = %q, want its idea page", page.Captures[0].URL)
	}

	spec, err := f.artifacts.Add(context.Background(), app.ArtifactInput{
		ProjectID: project.ID, Kind: core.ArtifactSpec, Title: "An unrelated spec",
	})
	if err != nil {
		t.Fatalf("Add(spec) error = %v", err)
	}
	var plain struct {
		Captures []taskLink `json:"captures"`
	}
	getDoc(t, ts.URL+"/api/doc/"+string(spec.ID), &plain)
	if len(plain.Captures) != 0 {
		t.Fatalf("plain doc carries captures = %+v, want none", plain.Captures)
	}
}

// TestNotificationFiresOnOriginArtifactCapture proves the real grooming path: a
// capture linked back only through its origin_artifact (no note at all) still
// completes the prompt's grooming and is announced, so a real prompt -> groom ->
// toast fires with no manual note step.
func TestNotificationFiresOnOriginArtifactCapture(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newPromptServer(t, f, project.ID)

	resp := postPrompt(t, ts.URL+promptPath, testToken, `{"text":"Rework onboarding"}`)
	result := decodePrompt(t, resp)
	artifact, err := f.artifacts.Get(context.Background(), result.ID)
	if err != nil {
		t.Fatalf("Get(prompt) error = %v", err)
	}
	addCaptureFromPrompt(t, f, project.ID, core.KindIdea, "Guided first run", artifact)

	var doc notificationsDoc
	getDoc(t, ts.URL+"/api/notifications", &doc)
	if len(doc.Notifications) != 1 {
		t.Fatalf("notifications = %+v, want one", doc.Notifications)
	}
	got := doc.Notifications[0]
	if got.ID != result.ID {
		t.Fatalf("notification id = %q, want the prompt artifact %q", got.ID, result.ID)
	}
	if got.URL != "/doc/"+string(result.ID) {
		t.Fatalf("notification url = %q, want the prompt artifact page", got.URL)
	}
	if got.Captures != 1 {
		t.Fatalf("notification captures = %d, want 1", got.Captures)
	}
}

// TestPromptPageListsOriginArtifactCapture proves the prompt page shows a capture
// linked only by origin_artifact: the page and the toast derive from the same
// lookup, so they never disagree.
func TestPromptPageListsOriginArtifactCapture(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newPromptServer(t, f, project.ID)

	resp := postPrompt(t, ts.URL+promptPath, testToken, `{"text":"Rework onboarding"}`)
	result := decodePrompt(t, resp)
	artifact, err := f.artifacts.Get(context.Background(), result.ID)
	if err != nil {
		t.Fatalf("Get(prompt) error = %v", err)
	}
	capture := addCaptureFromPrompt(t, f, project.ID, core.KindIdea, "Guided first run", artifact)

	var page struct {
		Captures []taskLink `json:"captures"`
	}
	getDoc(t, ts.URL+"/api/doc/"+string(result.ID), &page)
	if len(page.Captures) != 1 {
		t.Fatalf("captures = %+v, want one", page.Captures)
	}
	if page.Captures[0].ID != capture.ID {
		t.Fatalf("capture id = %q, want %q", page.Captures[0].ID, capture.ID)
	}
	if page.Captures[0].URL != "/idea/"+string(capture.ID) {
		t.Fatalf("capture url = %q, want its idea page", page.Captures[0].URL)
	}
}

// TestNotificationIgnoresOriginArtifactAtNonPrompt proves only a prompt artifact
// qualifies: a capture whose origin_artifact names a spec is not a completion of
// any prompt's grooming.
func TestNotificationIgnoresOriginArtifactAtNonPrompt(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newPromptServer(t, f, project.ID)

	resp := postPrompt(t, ts.URL+promptPath, testToken, `{"text":"Rework onboarding"}`)
	_ = decodePrompt(t, resp)

	spec, err := f.artifacts.Add(context.Background(), app.ArtifactInput{
		ProjectID: project.ID, Kind: core.ArtifactSpec, Title: "An unrelated spec",
	})
	if err != nil {
		t.Fatalf("Add(spec) error = %v", err)
	}
	addCaptureFromPrompt(t, f, project.ID, core.KindIdea, "Guided first run", spec)

	var doc notificationsDoc
	getDoc(t, ts.URL+"/api/notifications", &doc)
	if len(doc.Notifications) != 0 {
		t.Fatalf("notifications = %+v, want none for a non-prompt origin", doc.Notifications)
	}
}

// TestOriginArtifactCaptureLinkedTwoWaysCountsOnce proves a capture that names
// the prompt both as its origin and in a note doc-link is counted once, not
// twice: the two link-back mechanisms must not double a capture.
func TestOriginArtifactCaptureLinkedTwoWaysCountsOnce(t *testing.T) {
	f := newFixture(t)
	project := f.addProject(t, "acme", "Acme")
	ts := newPromptServer(t, f, project.ID)

	resp := postPrompt(t, ts.URL+promptPath, testToken, `{"text":"Rework onboarding"}`)
	result := decodePrompt(t, resp)
	artifact, err := f.artifacts.Get(context.Background(), result.ID)
	if err != nil {
		t.Fatalf("Get(prompt) error = %v", err)
	}
	capture := addCaptureFromPrompt(t, f, project.ID, core.KindIdea, "Guided first run", artifact)
	linkCapture(t, f, capture, artifact)

	var doc notificationsDoc
	getDoc(t, ts.URL+"/api/notifications", &doc)
	if len(doc.Notifications) != 1 {
		t.Fatalf("notifications = %+v, want exactly one", doc.Notifications)
	}
	if doc.Notifications[0].Captures != 1 {
		t.Fatalf("notification captures = %d, want 1 (a capture linked two ways counts once)", doc.Notifications[0].Captures)
	}
}
