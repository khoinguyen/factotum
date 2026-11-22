package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/jsonfile"
)

// legacyStore reopens the same jsonfile document across a hand edit, so a test
// can seed a link the current validator refuses - the shape an upgraded store
// carries and no current producer can write.
func legacyStore(t *testing.T, ctx context.Context, path string) store.Backend {
	t.Helper()
	backend, err := jsonfile.Open(ctx, store.Config{Backend: "jsonfile", Options: map[string]string{"path": path}})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return backend
}

// rewriteStore replaces the first occurrence of old with replacement in the
// store document, failing when the fixture is not there to rewrite.
func rewriteStore(t *testing.T, path, old, replacement string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	legacy := bytes.Replace(data, []byte(old), []byte(replacement), 1)
	if bytes.Equal(legacy, data) {
		t.Fatalf("fixture did not rewrite %q", old)
	}
	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

// TestTaskSetRepairsLegacyNoteLink proves a note written before the url rule
// cannot wedge its task: the ticket validator runs over every note link on
// update, so a legacy control-rune url would otherwise fail every status
// change. The ticket service repairs the note on the way out, and the update
// that follows persists the repair.
func TestTaskSetRepairsLegacyNoteLink(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factotum.json")
	clock := &stepClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ids := &seqIDs{}

	backend := legacyStore(t, ctx, path)
	if err := backend.Projects().Create(ctx, &core.Project{ID: "prj-1", Name: "Acme", Policy: core.DefaultResolutionPolicy()}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	tasks := NewTicketService(backend, clock, ids)
	task, err := tasks.Add(ctx, TicketInput{ProjectID: "prj-1", Title: "legacy note"})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := tasks.AddNote(ctx, task.ID, NoteInput{
		Body: "legacy",
		Links: []core.Link{
			{Kind: core.LinkDoc, URL: "https://example.invalid/spec"},
			{Kind: core.LinkDoc, URL: "https://example.invalid/design", Title: "Design doc"},
		},
	}); err != nil {
		t.Fatalf("AddNote() error = %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// A literal tab in a url was accepted on main; the current rule refuses it.
	rewriteStore(t, path, `"https://example.invalid/spec"`, `"https://example.invalid/a\tb"`)
	// So was a title of only spaces, which is blank by the current rule.
	rewriteStore(t, path, `"Design doc"`, `"   "`)

	backend = legacyStore(t, ctx, path)
	t.Cleanup(func() { _ = backend.Close() })
	clock.t = clock.t.Add(time.Minute)
	tasks = NewTicketService(backend, clock, ids)

	const repaired = "https://example.invalid/a%09b"
	got, err := tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Notes[0].Links[0].URL != repaired {
		t.Fatalf("Get() url = %q, want the repaired %q", got.Notes[0].Links[0].URL, repaired)
	}
	if got.Notes[0].Links[1].Title != "" {
		t.Fatalf("Get() title = %q, want the blank title dropped", got.Notes[0].Links[1].Title)
	}

	updated, err := tasks.SetStatus(ctx, task.ID, core.StatusInProgress)
	if err != nil {
		t.Fatalf("SetStatus() error = %v; a legacy note link must not wedge the task", err)
	}
	if updated.Status != core.StatusInProgress || updated.Notes[0].Links[0].URL != repaired {
		t.Fatalf("SetStatus() = %+v, want in_progress with the repaired note link", updated)
	}
	if updated.Notes[0].Links[1].Title != "" {
		t.Fatalf("SetStatus() title = %q, want the blank title dropped", updated.Notes[0].Links[1].Title)
	}
	stored, err := backend.Tickets().Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get(stored) error = %v", err)
	}
	if stored.Notes[0].Links[0].URL != repaired {
		t.Fatalf("stored url = %q, want SetStatus to persist the repair", stored.Notes[0].Links[0].URL)
	}
	if stored.Notes[0].Links[1].Title != "" {
		t.Fatalf("stored title = %q, want SetStatus to persist the dropped title", stored.Notes[0].Links[1].Title)
	}
}

// TestArtifactUpdateRepairsLegacyLink proves an artifact's legacy link does not
// wedge its next update, which revalidates every link the artifact carries.
func TestArtifactUpdateRepairsLegacyLink(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factotum.json")
	clock := &stepClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ids := &seqIDs{}

	backend := legacyStore(t, ctx, path)
	if err := backend.Projects().Create(ctx, &core.Project{ID: "prj-1", Name: "Acme", Policy: core.DefaultResolutionPolicy()}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	artifacts := NewArtifactService(backend, clock, ids)
	artifact, err := artifacts.Add(ctx, ArtifactInput{
		ProjectID: "prj-1",
		Kind:      core.ArtifactDoc,
		Title:     "spec",
		Links: []core.Link{
			{Kind: core.LinkDoc, URL: "https://example.invalid/spec"},
			{Kind: core.LinkDoc, URL: "https://example.invalid/design", Title: "Design doc"},
		},
	})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	rewriteStore(t, path, `"https://example.invalid/spec"`, `"https://example.invalid/a\nb"`)
	rewriteStore(t, path, `"Design doc"`, `"   "`)

	backend = legacyStore(t, ctx, path)
	t.Cleanup(func() { _ = backend.Close() })
	clock.t = clock.t.Add(time.Minute)
	artifacts = NewArtifactService(backend, clock, ids)

	const repaired = "https://example.invalid/a%0Ab"
	got, err := artifacts.Get(ctx, artifact.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Links[0].URL != repaired {
		t.Fatalf("Get() url = %q, want the repaired %q", got.Links[0].URL, repaired)
	}
	if got.Links[1].Title != "" {
		t.Fatalf("Get() title = %q, want the blank title dropped", got.Links[1].Title)
	}

	title := "spec v2"
	updated, err := artifacts.Update(ctx, artifact.ID, ArtifactPatch{Title: &title})
	if err != nil {
		t.Fatalf("Update() error = %v; a legacy artifact link must not wedge the update", err)
	}
	if updated.Title != title || updated.Links[0].URL != repaired {
		t.Fatalf("Update() = %+v, want the new title and the repaired link", updated)
	}
	if updated.Links[1].Title != "" {
		t.Fatalf("Update() title = %q, want the blank title dropped", updated.Links[1].Title)
	}
	stored, err := backend.Artifacts().Get(ctx, artifact.ID)
	if err != nil {
		t.Fatalf("Get(stored) error = %v", err)
	}
	if stored.Links[0].URL != repaired {
		t.Fatalf("stored url = %q, want Update to persist the repair", stored.Links[0].URL)
	}
	if stored.Links[1].Title != "" {
		t.Fatalf("stored title = %q, want Update to persist the dropped title", stored.Links[1].Title)
	}
}
