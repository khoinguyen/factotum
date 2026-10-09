package sqlite

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

func openFileBackend(t *testing.T) (store.Backend, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "factotum.db")
	backend, err := Open(context.Background(), store.Config{Backend: "sqlite", Options: map[string]string{"path": path}})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	return backend, path
}

// TestFileBackendUsesWAL pins the concurrency fix: the file backend runs in WAL
// mode, so a reader reads the last committed snapshot while a writer holds the
// write lock instead of failing SQLITE_BUSY. WAL is a property of the database
// file, so a fresh connection to the same path observes it.
func TestFileBackendUsesWAL(t *testing.T) {
	_, path := openFileBackend(t)
	var mode string
	if err := openRaw(t, path).QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

// TestReadSucceedsWhileWriterHoldsExclusiveLock is the regression for Khoi's
// "list actors: database is locked (SQLITE_BUSY)". A writer holds an EXCLUSIVE
// transaction; in rollback-journal mode that blocks every reader, but in WAL
// mode the read proceeds against the last committed snapshot.
func TestReadSucceedsWhileWriterHoldsExclusiveLock(t *testing.T) {
	backend, path := openFileBackend(t)
	ctx := context.Background()
	if err := backend.Actors().Create(ctx, &core.Actor{ID: "a-1", Kind: core.ActorAgent, Name: "claude"}); err != nil {
		t.Fatalf("seed actor: %v", err)
	}

	db := openRaw(t, path)
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn() error = %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatalf("BEGIN EXCLUSIVE: %v", err)
	}
	defer func() { _, _ = conn.ExecContext(ctx, "ROLLBACK") }()
	if _, err := conn.ExecContext(ctx, "INSERT INTO actors (id, name, kind, data) VALUES ('a-2', 'x', 'agent', '{}')"); err != nil {
		t.Fatalf("write under exclusive lock: %v", err)
	}

	actors, err := backend.Actors().List(ctx)
	if err != nil {
		t.Fatalf("List() under a held write transaction error = %v, want nil", err)
	}
	if len(actors) != 1 {
		t.Fatalf("List() = %d actors, want the 1 committed actor", len(actors))
	}
}

// TestBackupFoldsWALIntoTheCopy proves the pre-migration backup is a complete
// snapshot in WAL mode: a commit that lives only in the -wal sidecar is folded
// into the main file before it is copied, so the backup can restore it.
func TestBackupFoldsWALIntoTheCopy(t *testing.T) {
	ctx := context.Background()
	backend, path := openFileBackend(t)
	b := backend.(*Backend)
	// Write through one pinned connection with autocheckpoint off, so the row
	// stays in the -wal sidecar and the main file alone is incomplete.
	conn, err := b.db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn() error = %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatalf("disable autocheckpoint: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO actors (id, name, kind, data) VALUES ('a-wal', 'claude', 'agent', '{}')"); err != nil {
		t.Fatalf("write actor: %v", err)
	}
	mainBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read main db: %v", err)
	}
	if strings.Contains(string(mainBytes), "a-wal") {
		t.Fatal("expected the actor to live only in the WAL sidecar before backup")
	}

	backupPath, err := b.backup(ctx, 0)
	if err != nil {
		t.Fatalf("backup() error = %v", err)
	}
	backupBytes, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !strings.Contains(string(backupBytes), "a-wal") {
		t.Fatal("backup is missing the WAL-resident actor")
	}
}
