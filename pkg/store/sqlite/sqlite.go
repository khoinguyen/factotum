// Package sqlite is the SQLite storage backend, built on the pure-Go
// modernc.org/sqlite driver. Entities are stored as JSON documents in typed
// columns; filter columns are extracted for querying.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// DefaultPath is the database path used when no path option is configured.
const DefaultPath = ".factotum/factotum.db"

const schema = `
CREATE TABLE IF NOT EXISTS projects (
  id   TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  data TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tasks (
  id         TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  repo       TEXT,
  kind       TEXT NOT NULL,
  status     TEXT NOT NULL,
  data       TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS actors (
  id   TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,
  data TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS artifacts (
  id         TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  task_id    TEXT,
  kind       TEXT NOT NULL,
  data       TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
  seq        INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT,
  task_id    TEXT,
  kind       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  data       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tasks_project ON tasks(project_id);
CREATE INDEX IF NOT EXISTS idx_actors_name ON actors(name);
CREATE INDEX IF NOT EXISTS idx_artifacts_project ON artifacts(project_id);
CREATE INDEX IF NOT EXISTS idx_events_project ON events(project_id);
`

// backupRetention is how many pre-migration backups to keep per database.
const backupRetention = 5

// currentSchemaVersion is the schema version this binary writes. It is a var so
// tests can exercise pending and failing migrations.
var currentSchemaVersion = 4

// nowFunc is overridable in tests so backup names are deterministic.
var nowFunc = time.Now

// migration is one ordered, transactional schema step (version-1 -> version).
type migration struct {
	version int
	apply   func(ctx context.Context, tx *sql.Tx) error
}

var migrations = []migration{
	{version: 1, apply: migrateV1},
	{version: 2, apply: migrateV2},
	{version: 3, apply: migrateV3},
	{version: 4, apply: migrateV4},
}

// migrateV1 creates the base schema and the pre-release additive columns.
func migrateV1(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	// Pre-release databases may lack tasks.repo.
	if _, err := tx.ExecContext(ctx, "ALTER TABLE tasks ADD COLUMN repo TEXT"); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("tasks.repo: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_tasks_repo ON tasks(repo)"); err != nil {
		return fmt.Errorf("index tasks.repo: %w", err)
	}
	return nil
}

// migrateV2 adds the FTS5 index over artifact titles and bodies and backfills
// it from existing rows.
func migrateV2(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, "CREATE VIRTUAL TABLE IF NOT EXISTS artifacts_fts USING fts5(title, body)"); err != nil {
		return fmt.Errorf("artifacts_fts: %w", err)
	}
	rows, err := tx.QueryContext(ctx, "SELECT rowid, data FROM artifacts")
	if err != nil {
		return fmt.Errorf("read artifacts: %w", err)
	}
	type entry struct {
		rowid int64
		title string
		body  string
	}
	var entries []entry
	for rows.Next() {
		var rowid int64
		var data string
		if err := rows.Scan(&rowid, &data); err != nil {
			_ = rows.Close()
			return err
		}
		artifact, err := decodeArtifact(data)
		if err != nil {
			_ = rows.Close()
			return err
		}
		entries = append(entries, entry{rowid: rowid, title: artifact.Title, body: artifact.Body})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, item := range entries {
		if _, err := tx.ExecContext(ctx, "INSERT INTO artifacts_fts(rowid, title, body) VALUES (?, ?, ?)", item.rowid, item.title, item.body); err != nil {
			return fmt.Errorf("index artifact: %w", err)
		}
	}
	return nil
}

// migrateV3 rebuilds the artifact FTS index with the brief column. FTS5 tables
// cannot add a column, so it drops and recreates the index and backfills it.
func migrateV3(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS artifacts_fts"); err != nil {
		return fmt.Errorf("drop artifacts_fts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "CREATE VIRTUAL TABLE artifacts_fts USING fts5(title, brief, body)"); err != nil {
		return fmt.Errorf("artifacts_fts: %w", err)
	}
	rows, err := tx.QueryContext(ctx, "SELECT rowid, data FROM artifacts")
	if err != nil {
		return fmt.Errorf("read artifacts: %w", err)
	}
	type entry struct {
		rowid int64
		title string
		brief string
		body  string
	}
	var entries []entry
	for rows.Next() {
		var rowid int64
		var data string
		if err := rows.Scan(&rowid, &data); err != nil {
			_ = rows.Close()
			return err
		}
		artifact, err := decodeArtifact(data)
		if err != nil {
			_ = rows.Close()
			return err
		}
		entries = append(entries, entry{rowid: rowid, title: artifact.Title, brief: artifact.Brief, body: artifact.Body})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, item := range entries {
		if _, err := tx.ExecContext(ctx, "INSERT INTO artifacts_fts(rowid, title, brief, body) VALUES (?, ?, ?, ?)", item.rowid, item.title, item.brief, item.body); err != nil {
			return fmt.Errorf("index artifact: %w", err)
		}
	}
	return nil
}

// migrateV4 adds the FTS5 index over task titles, descriptions, and note bodies
// and backfills it from existing rows.
func migrateV4(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, "CREATE VIRTUAL TABLE IF NOT EXISTS tasks_fts USING fts5(title, description, notes)"); err != nil {
		return fmt.Errorf("tasks_fts: %w", err)
	}
	rows, err := tx.QueryContext(ctx, "SELECT rowid, data FROM tasks")
	if err != nil {
		return fmt.Errorf("read tasks: %w", err)
	}
	type entry struct {
		rowid int64
		title string
		body  string
		notes string
	}
	var entries []entry
	for rows.Next() {
		var rowid int64
		var data string
		if err := rows.Scan(&rowid, &data); err != nil {
			_ = rows.Close()
			return err
		}
		var task core.Task
		if err := json.Unmarshal([]byte(data), &task); err != nil {
			_ = rows.Close()
			return fmt.Errorf("decode task: %w", err)
		}
		entries = append(entries, entry{rowid: rowid, title: task.Title, body: task.Description, notes: notesText(&task)})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, item := range entries {
		if _, err := tx.ExecContext(ctx, "INSERT INTO tasks_fts(rowid, title, description, notes) VALUES (?, ?, ?, ?)", item.rowid, item.title, item.body, item.notes); err != nil {
			return fmt.Errorf("index task: %w", err)
		}
	}
	return nil
}

type Backend struct {
	db      *sql.DB
	path    string
	noticef func(format string, args ...any)
}

// notice emits a human-readable migration notice, if a sink is configured.
func (b *Backend) notice(format string, args ...any) {
	if b.noticef != nil {
		b.noticef(format, args...)
	}
}

func Open(ctx context.Context, cfg store.Config) (store.Backend, error) {
	path := cfg.Option("path")
	if path == "" {
		path = DefaultPath
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create state dir: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	backend := &Backend{db: db, path: path, noticef: cfg.Noticef}
	if err := backend.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return backend, nil
}

// Migrate brings the database up to currentSchemaVersion. It refuses a database
// from a newer binary, backs the file up before changing an existing database,
// and applies each pending step in its own transaction.
func (b *Backend) Migrate(ctx context.Context) error {
	version, err := b.schemaVersion(ctx)
	if err != nil {
		return err
	}
	if version > currentSchemaVersion {
		return fmt.Errorf("%w: database schema version %d is newer than this binary supports (%d)", core.ErrInvalid, version, currentSchemaVersion)
	}
	if version == currentSchemaVersion {
		return nil
	}
	existing, err := b.hasUserTables(ctx)
	if err != nil {
		return err
	}
	if existing {
		backupPath, err := b.backup(version)
		if err != nil {
			return fmt.Errorf("backup before migration: %w", err)
		}
		b.notice("database schema v%d is older than v%d; backing up to %s and migrating", version, currentSchemaVersion, backupPath)
	}
	for _, step := range migrations {
		if step.version <= version {
			continue
		}
		if err := b.applyMigration(ctx, step); err != nil {
			return fmt.Errorf("migrate to schema version %d: %w", step.version, err)
		}
	}
	if existing {
		b.notice("database schema migrated to v%d", currentSchemaVersion)
	}
	return nil
}

func (b *Backend) schemaVersion(ctx context.Context) (int, error) {
	var version int
	if err := b.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}

func (b *Backend) hasUserTables(ctx context.Context) (bool, error) {
	var count int
	if err := b.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'").Scan(&count); err != nil {
		return false, fmt.Errorf("inspect schema: %w", err)
	}
	return count > 0, nil
}

func (b *Backend) applyMigration(ctx context.Context, step migration) error {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := step.apply(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", step.version)); err != nil {
		return fmt.Errorf("stamp schema version: %w", err)
	}
	return tx.Commit()
}

// backup copies the database file next to itself before a migration and keeps
// the most recent backupRetention copies. It returns the backup path ("" when
// there is nothing to back up).
func (b *Backend) backup(fromVersion int) (string, error) {
	if b.path == "" || b.path == ":memory:" {
		return "", nil
	}
	data, err := os.ReadFile(b.path)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(b.path)
	base := filepath.Base(b.path)
	backupPath := filepath.Join(dir, fmt.Sprintf("%s.bak-v%d-%s", base, fromVersion, nowFunc().UTC().Format("20060102T150405Z")))
	if err := os.WriteFile(backupPath, data, 0o600); err != nil {
		return "", err
	}
	if err := pruneBackups(dir, base); err != nil {
		return "", err
	}
	return backupPath, nil
}

func pruneBackups(dir, base string) error {
	matches, err := filepath.Glob(filepath.Join(dir, base+".bak-*"))
	if err != nil {
		return err
	}
	if len(matches) <= backupRetention {
		return nil
	}
	sort.Strings(matches)
	for _, stale := range matches[:len(matches)-backupRetention] {
		if err := os.Remove(stale); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) Close() error { return b.db.Close() }

func (b *Backend) Projects() store.ProjectRepo { return &projectRepo{db: b.db} }
func (b *Backend) Tasks() store.TaskRepo       { return &taskRepo{db: b.db} }
func (b *Backend) Actors() store.ActorRepo     { return &actorRepo{db: b.db} }
func (b *Backend) Artifacts() store.ArtifactRepo {
	return &artifactRepo{db: b.db}
}
func (b *Backend) Events() store.EventRepo { return &eventRepo{db: b.db} }

type projectRepo struct{ db *sql.DB }

func (r *projectRepo) Create(ctx context.Context, project *core.Project) error {
	data, err := encode(project)
	if err != nil {
		return err
	}
	exists, err := rowExists(ctx, r.db, "SELECT 1 FROM projects WHERE id = ?", string(project.ID))
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%w: project %s", core.ErrAlreadyExists, project.ID)
	}
	if _, err := r.db.ExecContext(ctx, "INSERT INTO projects (id, name, data) VALUES (?, ?, ?)", string(project.ID), project.Name, data); err != nil {
		return fmt.Errorf("insert project: %w", err)
	}
	return nil
}

func (r *projectRepo) Get(ctx context.Context, id core.ProjectID) (*core.Project, error) {
	var data string
	err := r.db.QueryRowContext(ctx, "SELECT data FROM projects WHERE id = ?", string(id)).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: project %s", core.ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	var project core.Project
	if err := json.Unmarshal([]byte(data), &project); err != nil {
		return nil, fmt.Errorf("decode project: %w", err)
	}
	return &project, nil
}

func (r *projectRepo) List(ctx context.Context) ([]*core.Project, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT data FROM projects ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]*core.Project, 0)
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var project core.Project
		if err := json.Unmarshal([]byte(data), &project); err != nil {
			return nil, fmt.Errorf("decode project: %w", err)
		}
		out = append(out, &project)
	}
	return out, rows.Err()
}

func (r *projectRepo) Update(ctx context.Context, project *core.Project) error {
	exists, err := rowExists(ctx, r.db, "SELECT 1 FROM projects WHERE id = ?", string(project.ID))
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: project %s", core.ErrNotFound, project.ID)
	}
	data, err := encode(project)
	if err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, "UPDATE projects SET name = ?, data = ? WHERE id = ?", project.Name, data, string(project.ID)); err != nil {
		return fmt.Errorf("update project: %w", err)
	}
	return nil
}

func (r *projectRepo) Delete(ctx context.Context, id core.ProjectID) error {
	result, err := r.db.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", string(id))
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: project %s", core.ErrNotFound, id)
	}
	return nil
}

type taskRepo struct{ db *sql.DB }

func (r *taskRepo) Create(ctx context.Context, task *core.Task) error {
	exists, err := rowExists(ctx, r.db, "SELECT 1 FROM tasks WHERE id = ?", string(task.ID))
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%w: task %s", core.ErrAlreadyExists, task.ID)
	}
	data, err := encode(task)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin task create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO tasks (id, project_id, repo, kind, status, data) VALUES (?, ?, ?, ?, ?, ?)",
		string(task.ID), string(task.ProjectID), task.Repo, string(task.Kind), string(task.Status), data); err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	if err := indexTask(ctx, tx, task); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *taskRepo) Get(ctx context.Context, id core.TaskID) (*core.Task, error) {
	var data string
	err := r.db.QueryRowContext(ctx, "SELECT data FROM tasks WHERE id = ?", string(id)).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: task %s", core.ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}
	var task core.Task
	if err := json.Unmarshal([]byte(data), &task); err != nil {
		return nil, fmt.Errorf("decode task: %w", err)
	}
	return &task, nil
}

// taskFilterConditions builds the WHERE conditions for a task filter. Labels
// are a JSON array column, so they are applied in Go via store.MatchLabels.
func taskFilterConditions(filter store.TaskFilter) ([]string, []any) {
	var conditions []string
	var args []any
	if filter.ProjectID != "" {
		conditions = append(conditions, "project_id = ?")
		args = append(args, string(filter.ProjectID))
	}
	if filter.Repo != nil {
		conditions = append(conditions, "repo = ?")
		args = append(args, *filter.Repo)
	}
	if len(filter.Statuses) > 0 {
		conditions = append(conditions, "status IN ("+placeholders(len(filter.Statuses))+")")
		for _, status := range filter.Statuses {
			args = append(args, string(status))
		}
	}
	if filter.Kind != nil {
		conditions = append(conditions, "kind = ?")
		args = append(args, string(*filter.Kind))
	}
	return conditions, args
}

func (r *taskRepo) List(ctx context.Context, filter store.TaskFilter) ([]*core.Task, error) {
	query := "SELECT data FROM tasks"
	conditions, args := taskFilterConditions(filter)
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY id"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]*core.Task, 0)
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var task core.Task
		if err := json.Unmarshal([]byte(data), &task); err != nil {
			return nil, fmt.Errorf("decode task: %w", err)
		}
		if !store.MatchLabels(task, filter.Labels) {
			continue
		}
		out = append(out, &task)
	}
	return out, rows.Err()
}

func (r *taskRepo) Search(ctx context.Context, filter store.TaskFilter, query string) ([]store.TaskSearchHit, error) {
	terms := store.LexicalTerms(query)
	conditions, args := taskFilterConditions(filter)

	statement := "SELECT t.data FROM tasks t"
	queryArgs := args
	hasWhere := false
	if len(terms) > 0 {
		// FTS5 selects the candidate set (every term must appear as a prefix).
		// The shared LexicalTaskScore orders it, so sqlite ranks exactly like the
		// scan backends and bm25's term-frequency factor cannot promote a
		// lower-ranked column over a title hit.
		statement = "SELECT t.data FROM tasks_fts JOIN tasks t ON t.rowid = tasks_fts.rowid WHERE tasks_fts MATCH ?"
		queryArgs = append([]any{ftsQuery(terms)}, args...)
		hasWhere = true
	}
	if len(conditions) > 0 {
		if hasWhere {
			statement += " AND " + strings.Join(conditions, " AND ")
		} else {
			statement += " WHERE " + strings.Join(conditions, " AND ")
		}
	}
	statement += " ORDER BY t.id"

	rows, err := r.db.QueryContext(ctx, statement, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("search tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	hits := make([]store.TaskSearchHit, 0)
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var task core.Task
		if err := json.Unmarshal([]byte(data), &task); err != nil {
			return nil, fmt.Errorf("decode task: %w", err)
		}
		if !store.MatchLabels(task, filter.Labels) {
			continue
		}
		score, matched := store.LexicalTaskScore(&task, terms)
		if !matched {
			continue
		}
		hits = append(hits, store.TaskSearchHit{Task: &task, Score: score})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	store.SortTaskSearchHits(hits)
	return hits, nil
}

func (r *taskRepo) Update(ctx context.Context, task *core.Task) error {
	exists, err := rowExists(ctx, r.db, "SELECT 1 FROM tasks WHERE id = ?", string(task.ID))
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: task %s", core.ErrNotFound, task.ID)
	}
	data, err := encode(task)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin task update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		"UPDATE tasks SET project_id = ?, repo = ?, kind = ?, status = ?, data = ? WHERE id = ?",
		string(task.ProjectID), task.Repo, string(task.Kind), string(task.Status), data, string(task.ID)); err != nil {
		return fmt.Errorf("update task: %w", err)
	}
	if err := indexTask(ctx, tx, task); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *taskRepo) UpdateExpected(ctx context.Context, task *core.Task, expected time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin task update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var data string
	err = tx.QueryRowContext(ctx, "SELECT data FROM tasks WHERE id = ?", string(task.ID)).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: task %s", core.ErrNotFound, task.ID)
	}
	if err != nil {
		return fmt.Errorf("get task: %w", err)
	}
	var stored core.Task
	if err := json.Unmarshal([]byte(data), &stored); err != nil {
		return fmt.Errorf("decode task: %w", err)
	}
	if !stored.UpdatedAt.Equal(expected) {
		return fmt.Errorf("%w: task %s was modified", core.ErrConflict, task.ID)
	}
	encoded, err := encode(task)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE tasks SET project_id = ?, repo = ?, kind = ?, status = ?, data = ? WHERE id = ?",
		string(task.ProjectID), task.Repo, string(task.Kind), string(task.Status), encoded, string(task.ID)); err != nil {
		return fmt.Errorf("update task: %w", err)
	}
	if err := indexTask(ctx, tx, task); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *taskRepo) Delete(ctx context.Context, id core.TaskID) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin task delete: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var rowid int64
	err = tx.QueryRowContext(ctx, "SELECT rowid FROM tasks WHERE id = ?", string(id)).Scan(&rowid)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: task %s", core.ErrNotFound, id)
	}
	if err != nil {
		return fmt.Errorf("find task: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM tasks WHERE id = ?", string(id)); err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM tasks_fts WHERE rowid = ?", rowid); err != nil {
		return fmt.Errorf("unindex task: %w", err)
	}
	return tx.Commit()
}

// indexTask (re)writes the task's row in the FTS index.
func indexTask(ctx context.Context, tx *sql.Tx, task *core.Task) error {
	var rowid int64
	if err := tx.QueryRowContext(ctx, "SELECT rowid FROM tasks WHERE id = ?", string(task.ID)).Scan(&rowid); err != nil {
		return fmt.Errorf("task rowid: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM tasks_fts WHERE rowid = ?", rowid); err != nil {
		return fmt.Errorf("unindex task: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO tasks_fts(rowid, title, description, notes) VALUES (?, ?, ?, ?)", rowid, task.Title, task.Description, notesText(task)); err != nil {
		return fmt.Errorf("index task: %w", err)
	}
	return nil
}

// notesText joins the task's note bodies for the FTS notes column.
func notesText(task *core.Task) string {
	parts := make([]string, 0, len(task.Notes))
	for _, note := range task.Notes {
		parts = append(parts, note.Body)
	}
	return strings.Join(parts, "\n")
}

type actorRepo struct{ db *sql.DB }

func (r *actorRepo) Create(ctx context.Context, actor *core.Actor) error {
	exists, err := rowExists(ctx, r.db, "SELECT 1 FROM actors WHERE id = ?", string(actor.ID))
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%w: actor %s", core.ErrAlreadyExists, actor.ID)
	}
	data, err := encode(actor)
	if err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, "INSERT INTO actors (id, name, kind, data) VALUES (?, ?, ?, ?)",
		string(actor.ID), actor.Name, string(actor.Kind), data); err != nil {
		return fmt.Errorf("insert actor: %w", err)
	}
	return nil
}

func (r *actorRepo) Get(ctx context.Context, id core.ActorID) (*core.Actor, error) {
	var data string
	err := r.db.QueryRowContext(ctx, "SELECT data FROM actors WHERE id = ?", string(id)).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: actor %s", core.ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("get actor: %w", err)
	}
	return decodeActor(data)
}

func (r *actorRepo) FindByName(ctx context.Context, name string) (*core.Actor, error) {
	var data string
	err := r.db.QueryRowContext(ctx, "SELECT data FROM actors WHERE name = ?", name).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: actor named %q", core.ErrNotFound, name)
	}
	if err != nil {
		return nil, fmt.Errorf("find actor: %w", err)
	}
	return decodeActor(data)
}

func (r *actorRepo) List(ctx context.Context) ([]*core.Actor, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT data FROM actors ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("list actors: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]*core.Actor, 0)
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		actor, err := decodeActor(data)
		if err != nil {
			return nil, err
		}
		out = append(out, actor)
	}
	return out, rows.Err()
}

func (r *actorRepo) Update(ctx context.Context, actor *core.Actor) error {
	exists, err := rowExists(ctx, r.db, "SELECT 1 FROM actors WHERE id = ?", string(actor.ID))
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: actor %s", core.ErrNotFound, actor.ID)
	}
	data, err := encode(actor)
	if err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, "UPDATE actors SET name = ?, kind = ?, data = ? WHERE id = ?",
		actor.Name, string(actor.Kind), data, string(actor.ID)); err != nil {
		return fmt.Errorf("update actor: %w", err)
	}
	return nil
}

func (r *actorRepo) Delete(ctx context.Context, id core.ActorID) error {
	result, err := r.db.ExecContext(ctx, "DELETE FROM actors WHERE id = ?", string(id))
	if err != nil {
		return fmt.Errorf("delete actor: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: actor %s", core.ErrNotFound, id)
	}
	return nil
}

func decodeActor(data string) (*core.Actor, error) {
	var actor core.Actor
	if err := json.Unmarshal([]byte(data), &actor); err != nil {
		return nil, fmt.Errorf("decode actor: %w", err)
	}
	return &actor, nil
}

type artifactRepo struct{ db *sql.DB }

func (r *artifactRepo) Create(ctx context.Context, artifact *core.Artifact) error {
	data, err := encode(artifact)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	exists, err := rowExistsTx(ctx, tx, "SELECT 1 FROM artifacts WHERE id = ?", string(artifact.ID))
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%w: artifact %s", core.ErrAlreadyExists, artifact.ID)
	}
	var taskID any
	if artifact.TaskID != nil {
		taskID = string(*artifact.TaskID)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO artifacts (id, project_id, task_id, kind, data) VALUES (?, ?, ?, ?, ?)",
		string(artifact.ID), string(artifact.ProjectID), taskID, string(artifact.Kind), data); err != nil {
		return fmt.Errorf("insert artifact: %w", err)
	}
	if err := indexArtifact(ctx, tx, artifact); err != nil {
		return err
	}
	return tx.Commit()
}

// indexArtifact (re)writes the artifact's row in the FTS index.
func indexArtifact(ctx context.Context, tx *sql.Tx, artifact *core.Artifact) error {
	var rowid int64
	if err := tx.QueryRowContext(ctx, "SELECT rowid FROM artifacts WHERE id = ?", string(artifact.ID)).Scan(&rowid); err != nil {
		return fmt.Errorf("artifact rowid: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM artifacts_fts WHERE rowid = ?", rowid); err != nil {
		return fmt.Errorf("unindex artifact: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO artifacts_fts(rowid, title, brief, body) VALUES (?, ?, ?, ?)", rowid, artifact.Title, artifact.Brief, artifact.Body); err != nil {
		return fmt.Errorf("index artifact: %w", err)
	}
	return nil
}

func (r *artifactRepo) Get(ctx context.Context, id core.ArtifactID) (*core.Artifact, error) {
	var data string
	err := r.db.QueryRowContext(ctx, "SELECT data FROM artifacts WHERE id = ?", string(id)).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: artifact %s", core.ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("get artifact: %w", err)
	}
	return decodeArtifact(data)
}

func (r *artifactRepo) List(ctx context.Context, filter store.ArtifactFilter) ([]*core.Artifact, error) {
	query := "SELECT data FROM artifacts"
	var conditions []string
	var args []any
	if filter.ProjectID != "" {
		conditions = append(conditions, "project_id = ?")
		args = append(args, string(filter.ProjectID))
	}
	if filter.TaskID != nil {
		conditions = append(conditions, "task_id = ?")
		args = append(args, string(*filter.TaskID))
	}
	if filter.Kind != nil {
		conditions = append(conditions, "kind = ?")
		args = append(args, string(*filter.Kind))
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY id"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list artifacts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]*core.Artifact, 0)
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		artifact, err := decodeArtifact(data)
		if err != nil {
			return nil, err
		}
		out = append(out, artifact)
	}
	return out, rows.Err()
}

func (r *artifactRepo) Search(ctx context.Context, filter store.ArtifactFilter, query string) ([]store.SearchHit, error) {
	terms := store.LexicalTerms(query)
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if filter.ProjectID != "" {
		conditions = append(conditions, "a.project_id = ?")
		args = append(args, string(filter.ProjectID))
	}
	if filter.TaskID != nil {
		conditions = append(conditions, "a.task_id = ?")
		args = append(args, string(*filter.TaskID))
	}
	if filter.Kind != nil {
		conditions = append(conditions, "a.kind = ?")
		args = append(args, string(*filter.Kind))
	}

	statement := "SELECT a.data FROM artifacts a"
	queryArgs := args
	hasWhere := false
	if len(terms) > 0 {
		// FTS5 matches token prefixes; every term must appear (implicit AND).
		statement = "SELECT a.data, bm25(artifacts_fts, 10.0, 5.0, 1.0) AS rank FROM artifacts_fts JOIN artifacts a ON a.rowid = artifacts_fts.rowid WHERE artifacts_fts MATCH ?"
		queryArgs = append([]any{ftsQuery(terms)}, args...)
		hasWhere = true
	}
	if len(conditions) > 0 {
		if hasWhere {
			statement += " AND " + strings.Join(conditions, " AND ")
		} else {
			statement += " WHERE " + strings.Join(conditions, " AND ")
		}
	}
	statement += " ORDER BY a.id"

	rows, err := r.db.QueryContext(ctx, statement, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("search artifacts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	hits := make([]store.SearchHit, 0)
	for rows.Next() {
		var data string
		score := 0.0
		if len(terms) > 0 {
			var rank float64
			if err := rows.Scan(&data, &rank); err != nil {
				return nil, err
			}
			score = -rank
		} else if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		artifact, err := decodeArtifact(data)
		if err != nil {
			return nil, err
		}
		hits = append(hits, store.SearchHit{Artifact: artifact, Score: score})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	store.SortSearchHits(hits)
	return hits, nil
}

// ftsQuery turns lexical terms into an FTS5 prefix query (implicit AND).
func ftsQuery(terms []string) string {
	parts := make([]string, 0, len(terms))
	for _, term := range terms {
		parts = append(parts, term+"*")
	}
	return strings.Join(parts, " ")
}

func (r *artifactRepo) Update(ctx context.Context, artifact *core.Artifact) error {
	data, err := encode(artifact)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	exists, err := rowExistsTx(ctx, tx, "SELECT 1 FROM artifacts WHERE id = ?", string(artifact.ID))
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: artifact %s", core.ErrNotFound, artifact.ID)
	}
	var taskID any
	if artifact.TaskID != nil {
		taskID = string(*artifact.TaskID)
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE artifacts SET project_id = ?, task_id = ?, kind = ?, data = ? WHERE id = ?",
		string(artifact.ProjectID), taskID, string(artifact.Kind), data, string(artifact.ID)); err != nil {
		return fmt.Errorf("update artifact: %w", err)
	}
	if err := indexArtifact(ctx, tx, artifact); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *artifactRepo) Delete(ctx context.Context, id core.ArtifactID) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var rowid int64
	err = tx.QueryRowContext(ctx, "SELECT rowid FROM artifacts WHERE id = ?", string(id)).Scan(&rowid)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: artifact %s", core.ErrNotFound, id)
	}
	if err != nil {
		return fmt.Errorf("find artifact: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM artifacts WHERE id = ?", string(id)); err != nil {
		return fmt.Errorf("delete artifact: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM artifacts_fts WHERE rowid = ?", rowid); err != nil {
		return fmt.Errorf("unindex artifact: %w", err)
	}
	return tx.Commit()
}

func decodeArtifact(data string) (*core.Artifact, error) {
	var artifact core.Artifact
	if err := json.Unmarshal([]byte(data), &artifact); err != nil {
		return nil, fmt.Errorf("decode artifact: %w", err)
	}
	return &artifact, nil
}

type eventRepo struct{ db *sql.DB }

func (r *eventRepo) Append(ctx context.Context, event *core.Event) error {
	data, err := encode(event)
	if err != nil {
		return err
	}
	var projectID, taskID any
	if event.ProjectID != "" {
		projectID = string(event.ProjectID)
	}
	if event.TaskID != nil {
		taskID = string(*event.TaskID)
	}
	_, err = r.db.ExecContext(ctx,
		"INSERT INTO events (project_id, task_id, kind, created_at, data) VALUES (?, ?, ?, ?, ?)",
		projectID, taskID, string(event.Kind), formatTime(event.CreatedAt), data)
	if err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	return nil
}

func (r *eventRepo) List(ctx context.Context, filter store.EventFilter) ([]*core.Event, error) {
	query := "SELECT data FROM events"
	var conditions []string
	var args []any
	if filter.ProjectID != "" {
		conditions = append(conditions, "project_id = ?")
		args = append(args, string(filter.ProjectID))
	}
	if filter.TaskID != nil {
		conditions = append(conditions, "task_id = ?")
		args = append(args, string(*filter.TaskID))
	}
	if len(filter.Kinds) > 0 {
		conditions = append(conditions, "kind IN ("+placeholders(len(filter.Kinds))+")")
		for _, kind := range filter.Kinds {
			args = append(args, string(kind))
		}
	}
	if filter.Since != nil {
		conditions = append(conditions, "created_at >= ?")
		args = append(args, formatTime(*filter.Since))
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY seq DESC"
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]*core.Event, 0)
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var event core.Event
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return nil, fmt.Errorf("decode event: %w", err)
		}
		out = append(out, &event)
	}
	return out, rows.Err()
}

func rowExists(ctx context.Context, db *sql.DB, query string, args ...any) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query: %w", err)
	}
	return true, nil
}

func rowExistsTx(ctx context.Context, tx *sql.Tx, query string, args ...any) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query: %w", err)
	}
	return true, nil
}

func placeholders(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}

func encode(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode: %w", err)
	}
	return string(data), nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}
