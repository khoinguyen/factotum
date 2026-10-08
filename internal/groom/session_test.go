package groom

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleSession(id string, created time.Time) SessionRecord {
	return SessionRecord{
		ID:            id,
		Project:       "factotum",
		Mode:          "interactive",
		CreatedAt:     created,
		Scope:         []ScopeItem{{ID: "t-a", Kind: "idea", Title: "Maybe cache"}},
		Report:        "art-report",
		Deferred:      "art-deferred",
		Produced:      []string{"t-b"},
		Review:        "art-review",
		ReviewVerdict: string(VerdictNeedsRework),
		Blocked:       []string{"t-b"},
	}
}

func TestSessionWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	created := time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC)
	want := sampleSession("groom-abc", created)
	if err := WriteSession(dir, want); err != nil {
		t.Fatalf("WriteSession error = %v", err)
	}
	got, err := ReadSession(dir, want.ID)
	if err != nil {
		t.Fatalf("ReadSession error = %v", err)
	}
	if got.ID != want.ID || got.Project != want.Project || got.Mode != want.Mode {
		t.Fatalf("ReadSession = %+v, want id/project/mode from %+v", got, want)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("created_at = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
	if len(got.Scope) != 1 || got.Scope[0] != want.Scope[0] {
		t.Fatalf("scope = %+v, want %+v", got.Scope, want.Scope)
	}
	if got.Report != want.Report || got.Deferred != want.Deferred {
		t.Fatalf("report/deferred = %q/%q, want %q/%q", got.Report, got.Deferred, want.Report, want.Deferred)
	}
	if len(got.Produced) != 1 || got.Produced[0] != "t-b" {
		t.Fatalf("produced = %v, want [t-b]", got.Produced)
	}
	if got.Review != want.Review || got.ReviewVerdict != want.ReviewVerdict {
		t.Fatalf("review = %q/%q, want %q/%q", got.Review, got.ReviewVerdict, want.Review, want.ReviewVerdict)
	}
	if len(got.Blocked) != 1 || got.Blocked[0] != "t-b" {
		t.Fatalf("blocked = %v, want [t-b]", got.Blocked)
	}
}

// TestWriteSessionNormalizesEmptySlices pins the manifest shape: a session with
// no scope or produced tasks serializes both as `[]`, not `null`, so a reader
// can iterate them without a nil check.
func TestWriteSessionNormalizesEmptySlices(t *testing.T) {
	dir := t.TempDir()
	rec := SessionRecord{ID: "groom-empty", Project: "factotum", Mode: "interactive", CreatedAt: time.Now().UTC()}
	if err := WriteSession(dir, rec); err != nil {
		t.Fatalf("WriteSession error = %v", err)
	}
	got, err := ReadSession(dir, rec.ID)
	if err != nil {
		t.Fatalf("ReadSession error = %v", err)
	}
	if got.Scope == nil || got.Produced == nil || got.Blocked == nil {
		t.Fatalf("scope/produced/blocked = %v/%v/%v, want non-nil empty slices", got.Scope, got.Produced, got.Blocked)
	}
	data, err := os.ReadFile(SessionManifestPath(dir, rec.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"produced": []`) || !strings.Contains(string(data), `"scope": []`) || !strings.Contains(string(data), `"blocked": []`) {
		t.Fatalf("manifest did not serialize empty slices as arrays:\n%s", data)
	}
}

func TestReadSessionMissingIsNotExist(t *testing.T) {
	_, err := ReadSession(t.TempDir(), "groom-missing")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadSession(missing) error = %v, want fs.ErrNotExist", err)
	}
}

func TestListSessionsSortedAndSkipsEntriesWithoutManifest(t *testing.T) {
	dir := t.TempDir()
	later := sampleSession("groom-later", time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC))
	earlier := sampleSession("groom-earlier", time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC))
	if err := WriteSession(dir, later); err != nil {
		t.Fatal(err)
	}
	if err := WriteSession(dir, earlier); err != nil {
		t.Fatal(err)
	}
	// A directory without a manifest (an interrupted session) and a stray file
	// must not break the listing.
	if err := os.MkdirAll(SessionDir(dir, "groom-interrupted"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SessionsDirName, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ListSessions(dir)
	if err != nil {
		t.Fatalf("ListSessions error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListSessions returned %d sessions, want 2: %+v", len(got), got)
	}
	if got[0].ID != "groom-earlier" || got[1].ID != "groom-later" {
		t.Fatalf("ListSessions order = %q, %q; want ascending by created_at", got[0].ID, got[1].ID)
	}
}

func TestListSessionsMissingRootIsEmpty(t *testing.T) {
	got, err := ListSessions(t.TempDir())
	if err != nil {
		t.Fatalf("ListSessions error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ListSessions = %+v, want none", got)
	}
}
