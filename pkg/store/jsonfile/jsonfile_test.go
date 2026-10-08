package jsonfile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/conformance"
)

func TestConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) store.Backend {
		path := filepath.Join(t.TempDir(), "factotum.json")
		backend, err := Open(context.Background(), store.Config{Backend: "jsonfile", Options: map[string]string{"path": path}})
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		t.Cleanup(func() { _ = backend.Close() })
		return backend
	})
}

func TestPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factotum.json")
	ctx := context.Background()
	cfg := store.Config{Backend: "jsonfile", Options: map[string]string{"path": path}}

	first, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	project := &core.Project{ID: "prj-1", Name: "Acme", Policy: core.DefaultResolutionPolicy()}
	if err := first.Projects().Create(ctx, project); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	second, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	reloaded, err := second.Projects().Get(ctx, "prj-1")
	if err != nil {
		t.Fatalf("Get() after reopen error = %v", err)
	}
	if reloaded.Name != "Acme" {
		t.Fatalf("Get() after reopen Name = %q, want Acme", reloaded.Name)
	}
}

// TestLoadKeepsLegacyTaskIDKey pins the on-disk wire key of the ticket link.
// core.Artifact and core.Event are embedded directly in the document, so the
// Task->Ticket rename must not change their JSON key ("TaskID"), or a database
// written by an earlier version silently loses the link on the next rewrite.
func TestLoadKeepsLegacyTaskIDKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factotum.json")
	legacy := `{
  "projects": [{"ID": "prj-1", "Name": "Acme"}],
  "tasks": [{"ID": "t-1", "ProjectID": "prj-1", "Kind": "task", "Title": "one", "Status": "todo"}],
  "artifacts": [{"ID": "art-1", "ProjectID": "prj-1", "TaskID": "t-1", "Kind": "memory", "Title": "m"}],
  "events": [{"ID": "ev-1", "ProjectID": "prj-1", "TaskID": "t-1", "Kind": "task.created", "Summary": "s"}]
}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ctx := context.Background()
	be, err := Open(ctx, store.Config{Backend: "jsonfile", Options: map[string]string{"path": path}})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	artifact, err := be.Artifacts().Get(ctx, "art-1")
	if err != nil {
		t.Fatalf("Artifacts().Get() error = %v", err)
	}
	if artifact.TicketID == nil || *artifact.TicketID != "t-1" {
		t.Fatalf("artifact ticket link = %v, want t-1", artifact.TicketID)
	}
	events, err := be.Events().List(ctx, store.EventFilter{})
	if err != nil {
		t.Fatalf("Events().List() error = %v", err)
	}
	if len(events) != 1 || events[0].TicketID == nil || *events[0].TicketID != "t-1" {
		t.Fatalf("event ticket link = %+v, want t-1", events)
	}

	if err := be.Projects().Create(ctx, &core.Project{ID: "prj-2", Name: "Beta"}); err != nil {
		t.Fatalf("Projects().Create() error = %v", err)
	}
	if err := be.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), `"TaskID": "t-1"`) {
		t.Fatalf("rewritten document lost the legacy TaskID key:\n%s", data)
	}
}

func TestLoadRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factotum.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := Open(context.Background(), store.Config{Backend: "jsonfile", Options: map[string]string{"path": path}}); err == nil {
		t.Fatal("Open() error = nil, want parse error")
	}
}
