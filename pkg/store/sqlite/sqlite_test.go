package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/conformance"
)

func TestConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) store.Backend {
		path := filepath.Join(t.TempDir(), "factotum.db")
		backend, err := Open(context.Background(), store.Config{Backend: "sqlite", Options: map[string]string{"path": path}})
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		t.Cleanup(func() { _ = backend.Close() })
		return backend
	})
}

func TestConformanceInMemory(t *testing.T) {
	conformance.Run(t, func(t *testing.T) store.Backend {
		backend, err := Open(context.Background(), store.Config{Backend: "sqlite", Options: map[string]string{"path": ":memory:"}})
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		t.Cleanup(func() { _ = backend.Close() })
		return backend
	})
}
