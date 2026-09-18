package jsonfile

import (
	"context"
	"os"
	"path/filepath"
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

func TestLoadRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factotum.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := Open(context.Background(), store.Config{Backend: "jsonfile", Options: map[string]string{"path": path}}); err == nil {
		t.Fatal("Open() error = nil, want parse error")
	}
}
