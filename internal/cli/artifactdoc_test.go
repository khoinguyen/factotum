package cli

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
)

// TestArtifactDocGolden pins the exact structured shape of an artifact doc, so
// a field rename or an accidental Go-name leak fails loudly.
func TestArtifactDocGolden(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	taskID := core.TaskID("t-1")
	artifact := &core.Artifact{
		ID: "art-1", ProjectID: "prj-1", TaskID: &taskID, Kind: core.ArtifactSpec,
		Title: "Spec", Brief: "b", Path: "p.md", Body: "body",
		Links:     []core.Link{{Kind: core.LinkPR, URL: "https://x/1"}},
		CreatedAt: at, UpdatedAt: at,
	}
	got, err := json.Marshal(artifactDocFrom(artifact))
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := `{"id":"art-1","project_id":"prj-1","task_id":"t-1","kind":"spec","title":"Spec","brief":"b","path":"p.md","body":"body","links":[{"kind":"pr","url":"https://x/1"}],"created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"}`
	if string(got) != want {
		t.Fatalf("artifactDoc golden mismatch:\n got %s\nwant %s", got, want)
	}
}

// TestMemoryDocGolden pins the memory shape: kind and path are omitted, so the
// memory verbs keep their established output.
func TestMemoryDocGolden(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	artifact := &core.Artifact{
		ID: "art-2", ProjectID: "prj-1", Kind: core.ArtifactMemory,
		Title: "Note", Brief: "b", Body: "body", CreatedAt: at, UpdatedAt: at,
	}
	got, err := json.Marshal(memoryDocFrom(artifact))
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := `{"id":"art-2","project_id":"prj-1","title":"Note","brief":"b","body":"body","created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"}`
	if string(got) != want {
		t.Fatalf("memoryDoc golden mismatch:\n got %s\nwant %s", got, want)
	}
}
