package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// unstampedDB writes a pre-release database: a tasks table with no version
// stamp, so Open sees an existing schema older than this binary's.
func unstampedDB(t *testing.T, path string) {
	t.Helper()
	raw := openRaw(t, path)
	if _, err := raw.Exec("CREATE TABLE tasks (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, kind TEXT NOT NULL, status TEXT NOT NULL, data TEXT NOT NULL)"); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}
}

// consentedConfig returns a config that grants explicit migration consent.
func consentedConfig(path string) store.Config {
	cfg := sqliteConfig(path)
	cfg.Options["migrate"] = "yes"
	return cfg
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
	task := core.Ticket{ID: "t-old", ProjectID: "prj", Kind: core.KindTask, Title: "old", Status: core.StatusTodo}
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

	backend, err := Open(ctx, consentedConfig(path))
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
	reloaded, err := backend.Tickets().Get(ctx, "t-old")
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
	cfg := consentedConfig(path)
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
	if err == nil {
		t.Fatal("Open() error = nil, want a newer-version rejection")
	}
	msg := err.Error()
	for _, want := range []string{
		"newer than this binary supports",
		fmt.Sprintf("(%d)", currentSchemaVersion),
		"mise run install",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Open() error = %q, want it to contain %q", msg, want)
		}
	}
}

func TestMigrateOlderRequiresConsent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factotum.db")
	unstampedDB(t, path)

	_, err := Open(ctx, sqliteConfig(path))
	if err == nil {
		t.Fatal("Open() error = nil, want a migration-consent refusal")
	}
	msg := err.Error()
	for _, want := range []string{"older", fmt.Sprintf("v%d", currentSchemaVersion), "migrate=yes"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Open() error = %q, want it to contain %q", msg, want)
		}
	}
	if got := userVersion(t, path); got == currentSchemaVersion {
		t.Fatalf("refused migration still changed user_version to %d", got)
	}
	if got := backups(t, path); len(got) != 0 {
		t.Fatalf("refusing before consent should not back up, got %v", got)
	}
}

func TestMigrateOlderWithConsentBacksUpAndMigrates(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factotum.db")
	unstampedDB(t, path)

	backend, err := Open(ctx, consentedConfig(path))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })

	if got := userVersion(t, path); got != currentSchemaVersion {
		t.Fatalf("user_version = %d, want %d", got, currentSchemaVersion)
	}
	if got := backups(t, path); len(got) != 1 {
		t.Fatalf("consented migration should create one backup, got %v", got)
	}
}

func TestMigrateConsentDoesNotBypassNewerRejection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factotum.db")
	raw := openRaw(t, path)
	if _, err := raw.Exec("PRAGMA user_version = " + itoa(currentSchemaVersion+1)); err != nil {
		t.Fatalf("stamp newer version: %v", err)
	}
	_ = raw.Close()

	_, err := Open(context.Background(), consentedConfig(path))
	if err == nil {
		t.Fatal("Open() with consent on a newer database should still be rejected")
	}
	if !strings.Contains(err.Error(), "newer than this binary supports") {
		t.Fatalf("Open() error = %q, want the newer-version rejection", err)
	}
}

func TestMigrateConsentValue(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		value   string
		consent bool
	}{
		{name: "yes", value: "yes", consent: true},
		{name: "case-insensitive", value: "YES", consent: true},
		{name: "trimmed", value: "  yes  ", consent: true},
		{name: "no", value: "no", consent: false},
		{name: "absent", value: "", consent: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "factotum.db")
			unstampedDB(t, path)
			cfg := sqliteConfig(path)
			if tt.value != "" {
				cfg.Options["migrate"] = tt.value
			}
			backend, err := Open(ctx, cfg)
			if !tt.consent {
				if err == nil {
					_ = backend.Close()
					t.Fatalf("Open(migrate=%q) error = nil, want a consent refusal", tt.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("Open(migrate=%q) error = %v, want consent accepted", tt.value, err)
			}
			_ = backend.Close()
		})
	}
}

func TestMigrateFailingStepRollsBack(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factotum.db")

	backend, err := Open(ctx, sqliteConfig(path))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	task := core.Ticket{ID: "t-1", ProjectID: "prj", Kind: core.KindTask, Title: "keep", Status: core.StatusTodo}
	if err := backend.Tickets().Create(ctx, &task); err != nil {
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

	if _, err := Open(ctx, consentedConfig(path)); err == nil {
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
	if _, err := backend.Tickets().Get(ctx, "t-1"); err != nil {
		t.Fatalf("data lost after failed migration: %v", err)
	}
}

func TestMigrateV2ToV3RebuildsArtifactIndex(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factotum.db")

	// Build a v2 database: the base schema plus the two-column artifact FTS
	// index, with one artifact indexed the old (title, body) way.
	raw := openRaw(t, path)
	tx, err := raw.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, step := range migrations {
		if step.version > 2 {
			break
		}
		if err := step.apply(ctx, tx); err != nil {
			t.Fatalf("apply v%d: %v", step.version, err)
		}
	}
	data, err := encode(&core.Artifact{ID: "art-1", ProjectID: "prj-1", Kind: core.ArtifactMemory, Title: "Terraform notes", Body: "apply in devops"})
	if err != nil {
		t.Fatalf("encode artifact: %v", err)
	}
	res, err := tx.Exec(
		"INSERT INTO artifacts (id, project_id, task_id, kind, data) VALUES (?, ?, ?, ?, ?)",
		"art-1", "prj-1", nil, string(core.ArtifactMemory), data,
	)
	if err != nil {
		t.Fatalf("insert artifact: %v", err)
	}
	rowid, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	if _, err := tx.Exec("INSERT INTO artifacts_fts(rowid, title, body) VALUES (?, ?, ?)", rowid, "Terraform notes", "apply in devops"); err != nil {
		t.Fatalf("index old artifact: %v", err)
	}
	if _, err := tx.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatalf("stamp v2: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	// Reopen: the v3 migration must rebuild the index with the brief column.
	backend, err := Open(ctx, consentedConfig(path))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if got := userVersion(t, path); got != currentSchemaVersion {
		t.Fatalf("user_version = %d, want %d", got, currentSchemaVersion)
	}

	// The pre-existing title/body stay searchable after the rebuild.
	hits, err := backend.Artifacts().Search(ctx, store.ArtifactFilter{ProjectID: "prj-1"}, "terraform")
	if err != nil {
		t.Fatalf("Search(terraform) error = %v", err)
	}
	if len(hits) != 1 || hits[0].Artifact.ID != "art-1" {
		t.Fatalf("Search(terraform) after v3 = %v, want [art-1]", hitIDs(hits))
	}

	// The rebuilt index has a brief column and indexes new briefs.
	if err := backend.Artifacts().Create(ctx, &core.Artifact{ID: "art-2", ProjectID: "prj-1", Kind: core.ArtifactMemory, Title: "unrelated", Brief: "widget guide", Body: "other"}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	hits, err = backend.Artifacts().Search(ctx, store.ArtifactFilter{ProjectID: "prj-1"}, "widget")
	if err != nil {
		t.Fatalf("Search(widget) error = %v", err)
	}
	if len(hits) != 1 || hits[0].Artifact.ID != "art-2" {
		t.Fatalf("Search(widget) after v3 = %v, want [art-2]", hitIDs(hits))
	}
}

func TestMigrateV3ToV4BackfillsTaskIndex(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factotum.db")

	// Build a v3 database: the base schema plus the artifact FTS index, with one
	// task inserted but no task FTS index yet.
	raw := openRaw(t, path)
	tx, err := raw.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, step := range migrations {
		if step.version > 3 {
			break
		}
		if err := step.apply(ctx, tx); err != nil {
			t.Fatalf("apply v%d: %v", step.version, err)
		}
	}
	data, err := encode(&core.Ticket{ID: "t-1", ProjectID: "prj-1", Kind: core.KindTask, Title: "Terraform notes", Description: "apply in devops", Status: core.StatusTodo})
	if err != nil {
		t.Fatalf("encode task: %v", err)
	}
	if _, err := tx.Exec(
		"INSERT INTO tasks (id, project_id, repo, kind, status, data) VALUES (?, ?, ?, ?, ?, ?)",
		"t-1", "prj-1", nil, string(core.KindTask), string(core.StatusTodo), data,
	); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	if _, err := tx.Exec("PRAGMA user_version = 3"); err != nil {
		t.Fatalf("stamp v3: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	// Reopen: the v4 migration must build and backfill the task index.
	backend, err := Open(ctx, consentedConfig(path))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if got := userVersion(t, path); got != currentSchemaVersion {
		t.Fatalf("user_version = %d, want %d", got, currentSchemaVersion)
	}
	hits, err := backend.Tickets().Search(ctx, store.TicketFilter{ProjectID: "prj-1"}, "terraform")
	if err != nil {
		t.Fatalf("Search(terraform) error = %v", err)
	}
	if len(hits) != 1 || hits[0].Ticket.ID != "t-1" {
		t.Fatalf("Search(terraform) after v4 = %v, want [t-1]", taskHitIDs(hits))
	}
}

func TestMigrateV4ToV5BackfillsTaskDeps(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factotum.db")

	// Build a v4 database: the base schema plus the search indexes, with tasks
	// whose dependency edges predate the reverse index.
	raw := openRaw(t, path)
	tx, err := raw.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, step := range migrations {
		if step.version > 4 {
			break
		}
		if err := step.apply(ctx, tx); err != nil {
			t.Fatalf("apply v%d: %v", step.version, err)
		}
	}
	tasks := []*core.Ticket{
		{ID: "t-1", ProjectID: "prj-1", Kind: core.KindTask, Title: "one", Status: core.StatusTodo},
		{ID: "t-2", ProjectID: "prj-1", Kind: core.KindTask, Title: "two", Status: core.StatusTodo, Deps: []core.TicketID{"t-1"}},
		{ID: "t-3", ProjectID: "prj-1", Kind: core.KindTask, Title: "three", Status: core.StatusTodo, Deps: []core.TicketID{"t-1", "t-2"}},
	}
	for _, task := range tasks {
		data, err := encode(task)
		if err != nil {
			t.Fatalf("encode %s: %v", task.ID, err)
		}
		if _, err := tx.Exec(
			"INSERT INTO tasks (id, project_id, repo, kind, status, data) VALUES (?, ?, ?, ?, ?, ?)",
			string(task.ID), string(task.ProjectID), nil, string(task.Kind), string(task.Status), data,
		); err != nil {
			t.Fatalf("insert %s: %v", task.ID, err)
		}
	}
	if _, err := tx.Exec("PRAGMA user_version = 4"); err != nil {
		t.Fatalf("stamp v4: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	// Reopen: the v5 migration must build and backfill the dependency index.
	backend, err := Open(ctx, consentedConfig(path))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if got := userVersion(t, path); got != currentSchemaVersion {
		t.Fatalf("user_version = %d, want %d", got, currentSchemaVersion)
	}
	dep := core.TicketID("t-1")
	dependents, err := backend.Tickets().List(ctx, store.TicketFilter{ProjectID: "prj-1", DependsOn: &dep})
	if err != nil {
		t.Fatalf("List(DependsOn t-1) error = %v", err)
	}
	if len(dependents) != 2 || dependents[0].ID != "t-2" || dependents[1].ID != "t-3" {
		t.Fatalf("List(DependsOn t-1) = %v, want [t-2 t-3]", dependents)
	}
}

func TestMigrateV7ToV8AddsPipelineCaptureIndex(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factotum.db")

	// Build a v7 database: the schema through the pipelines table, with one
	// pipeline already stored.
	raw := openRaw(t, path)
	tx, err := raw.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, step := range migrations {
		if step.version > 7 {
			break
		}
		if err := step.apply(ctx, tx); err != nil {
			t.Fatalf("apply v%d: %v", step.version, err)
		}
	}
	existing := &core.Pipeline{
		ID:        "pl-old",
		ProjectID: "prj-1",
		CaptureID: "i-old",
		State:     core.PipelineQueued,
		Gate:      core.GateNone,
		CreatedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	}
	data, err := encode(existing)
	if err != nil {
		t.Fatalf("encode pipeline: %v", err)
	}
	if _, err := tx.Exec(
		"INSERT INTO pipelines (id, project_id, capture_id, state, gate, created_at, updated_at, data) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		string(existing.ID), string(existing.ProjectID), string(existing.CaptureID), string(existing.State), string(existing.Gate), formatTimeKey(existing.CreatedAt), formatTimeKey(existing.UpdatedAt), data,
	); err != nil {
		t.Fatalf("insert pipeline: %v", err)
	}
	if _, err := tx.Exec("PRAGMA user_version = 7"); err != nil {
		t.Fatalf("stamp v7: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	// Reopen: the v8 migration adds the unique capture index without touching
	// the stored pipeline.
	backend, err := Open(ctx, consentedConfig(path))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if got := userVersion(t, path); got != currentSchemaVersion {
		t.Fatalf("user_version = %d, want %d", got, currentSchemaVersion)
	}
	if _, err := backend.Pipelines().Get(ctx, "pl-old"); err != nil {
		t.Fatalf("Get(pl-old) after v8 error = %v", err)
	}

	var count int
	if err := openRaw(t, path).QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_pipelines_capture_unique'").Scan(&count); err != nil {
		t.Fatalf("inspect index: %v", err)
	}
	if count != 1 {
		t.Fatalf("unique capture index count = %d, want 1", count)
	}

	// The index is the backstop: a raw duplicate insert is rejected even
	// bypassing the repo's explicit check.
	duplicate := *existing
	duplicate.ID = "pl-dupe"
	dupData, err := encode(&duplicate)
	if err != nil {
		t.Fatalf("encode duplicate: %v", err)
	}
	if _, err := openRaw(t, path).Exec(
		"INSERT INTO pipelines (id, project_id, capture_id, state, gate, created_at, updated_at, data) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		string(duplicate.ID), string(duplicate.ProjectID), string(duplicate.CaptureID), string(duplicate.State), string(duplicate.Gate), formatTimeKey(duplicate.CreatedAt), formatTimeKey(duplicate.UpdatedAt), dupData,
	); err == nil {
		t.Fatal("raw duplicate pipeline insert succeeded, want unique constraint error")
	}
}

func taskHitIDs(hits []store.TicketSearchHit) []core.TicketID {
	out := make([]core.TicketID, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.Ticket.ID)
	}
	return out
}

func hitIDs(hits []store.SearchHit) []core.ArtifactID {
	out := make([]core.ArtifactID, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.Artifact.ID)
	}
	return out
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
