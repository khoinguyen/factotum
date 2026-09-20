package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

func sqliteConfig(path string) store.Config {
	return store.Config{Backend: "sqlite", Options: map[string]string{"path": path}}
}

func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func userVersion(t *testing.T, path string) int {
	t.Helper()
	var version int
	if err := openRaw(t, path).QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	return version
}

func backups(t *testing.T, path string) []string {
	t.Helper()
	matches, err := filepath.Glob(path + ".bak-*")
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	return matches
}

func TestMigrateFreshStampsVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factotum.db")
	backend, err := Open(context.Background(), sqliteConfig(path))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })

	if got := userVersion(t, path); got != currentSchemaVersion {
		t.Fatalf("user_version = %d, want %d", got, currentSchemaVersion)
	}
	if got := backups(t, path); len(got) != 0 {
		t.Fatalf("fresh database should not be backed up, got %v", got)
	}
}

func TestMigrateUpgradesPreReleaseWithBackup(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factotum.db")

	// A pre-release database: tasks without the repo column and no version stamp.
	raw := openRaw(t, path)
	if _, err := raw.Exec(`
CREATE TABLE tasks (
  id         TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  kind       TEXT NOT NULL,
  status     TEXT NOT NULL,
  data       TEXT NOT NULL
);`); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	task := core.Task{ID: "t-old", ProjectID: "prj", Kind: core.KindTask, Title: "old", Status: core.StatusTodo}
	if _, err := raw.Exec(
		"INSERT INTO tasks (id, project_id, kind, status, data) VALUES (?, ?, ?, ?, ?)",
		string(task.ID), string(task.ProjectID), string(task.Kind), string(task.Status),
		`{"ID":"t-old","ProjectID":"prj","Kind":"task","Title":"old","Status":"todo"}`,
	); err != nil {
		t.Fatalf("insert old task: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	backend, err := Open(ctx, sqliteConfig(path))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })

	if got := userVersion(t, path); got != currentSchemaVersion {
		t.Fatalf("user_version = %d, want %d", got, currentSchemaVersion)
	}
	if got := backups(t, path); len(got) != 1 {
		t.Fatalf("upgrade should create one backup, got %v", got)
	}
	reloaded, err := backend.Tasks().Get(ctx, "t-old")
	if err != nil {
		t.Fatalf("Get(old task) error = %v", err)
	}
	if reloaded.Title != "old" {
		t.Fatalf("Get(old task).Title = %q, want old", reloaded.Title)
	}
}

func TestMigrateNotifiesOnUpgrade(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factotum.db")
	raw := openRaw(t, path)
	if _, err := raw.Exec("CREATE TABLE tasks (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, kind TEXT NOT NULL, status TEXT NOT NULL, data TEXT NOT NULL)"); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	_ = raw.Close()

	var notices []string
	cfg := sqliteConfig(path)
	cfg.Noticef = func(format string, args ...any) { notices = append(notices, fmt.Sprintf(format, args...)) }
	backend, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })

	joined := strings.Join(notices, "\n")
	if !strings.Contains(joined, "v0") || !strings.Contains(joined, fmt.Sprintf("v%d", currentSchemaVersion)) || !strings.Contains(joined, ".bak-") {
		t.Fatalf("notices = %v, want the from/to versions and the backup path", notices)
	}
}

func TestMigrateFreshDoesNotNotify(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factotum.db")
	var notices []string
	cfg := sqliteConfig(path)
	cfg.Noticef = func(format string, args ...any) { notices = append(notices, fmt.Sprintf(format, args...)) }
	backend, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if len(notices) != 0 {
		t.Fatalf("fresh database should not notify, got %v", notices)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factotum.db")
	backend, err := Open(context.Background(), sqliteConfig(path))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	_ = backend.Close()

	backend, err = Open(context.Background(), sqliteConfig(path))
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })

	if got := backups(t, path); len(got) != 0 {
		t.Fatalf("reopening an up-to-date database should not back up, got %v", got)
	}
	if got := userVersion(t, path); got != currentSchemaVersion {
		t.Fatalf("user_version = %d, want %d", got, currentSchemaVersion)
	}
}

func TestMigrateRejectsNewerVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factotum.db")
	raw := openRaw(t, path)
	if _, err := raw.Exec("PRAGMA user_version = " + itoa(currentSchemaVersion+1)); err != nil {
		t.Fatalf("stamp newer version: %v", err)
	}
	_ = raw.Close()

	_, err := Open(context.Background(), sqliteConfig(path))
	if err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("Open() error = %v, want a newer-version rejection", err)
	}
}

func TestMigrateFailingStepRollsBack(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factotum.db")

	backend, err := Open(ctx, sqliteConfig(path))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	task := core.Task{ID: "t-1", ProjectID: "prj", Kind: core.KindTask, Title: "keep", Status: core.StatusTodo}
	if err := backend.Tasks().Create(ctx, &task); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	_ = backend.Close()

	oldVersion, oldMigrations := currentSchemaVersion, migrations
	t.Cleanup(func() { currentSchemaVersion, migrations = oldVersion, oldMigrations })
	currentSchemaVersion = oldVersion + 1
	migrations = append(append([]migration{}, oldMigrations...), migration{
		version: currentSchemaVersion,
		apply:   func(context.Context, *sql.Tx) error { return errors.New("boom") },
	})

	if _, err := Open(ctx, sqliteConfig(path)); err == nil {
		t.Fatal("Open() should fail when a migration step fails")
	}
	if got := userVersion(t, path); got != oldVersion {
		t.Fatalf("user_version = %d, want the last good version %d", got, oldVersion)
	}

	currentSchemaVersion, migrations = oldVersion, oldMigrations
	backend, err = Open(ctx, sqliteConfig(path))
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if _, err := backend.Tasks().Get(ctx, "t-1"); err != nil {
		t.Fatalf("data lost after failed migration: %v", err)
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}
