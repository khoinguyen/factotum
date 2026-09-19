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
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

const defaultPath = ".factotum/factotum.db"

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

type Backend struct {
	db *sql.DB
}

func Open(ctx context.Context, cfg store.Config) (store.Backend, error) {
	path := cfg.Option("path")
	if path == "" {
		path = defaultPath
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
	backend := &Backend{db: db}
	if err := backend.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return backend, nil
}

func (b *Backend) Migrate(ctx context.Context) error {
	if _, err := b.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// Pre-release schema evolution: add columns older databases may lack.
	if _, err := b.db.ExecContext(ctx, "ALTER TABLE tasks ADD COLUMN repo TEXT"); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("migrate tasks.repo: %w", err)
	}
	if _, err := b.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_tasks_repo ON tasks(repo)"); err != nil {
		return fmt.Errorf("index tasks.repo: %w", err)
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
	_, err = r.db.ExecContext(ctx,
		"INSERT INTO tasks (id, project_id, repo, kind, status, data) VALUES (?, ?, ?, ?, ?, ?)",
		string(task.ID), string(task.ProjectID), task.Repo, string(task.Kind), string(task.Status), data)
	if err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	return nil
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

func (r *taskRepo) List(ctx context.Context, filter store.TaskFilter) ([]*core.Task, error) {
	query := "SELECT data FROM tasks"
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
		out = append(out, &task)
	}
	return out, rows.Err()
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
	_, err = r.db.ExecContext(ctx,
		"UPDATE tasks SET project_id = ?, repo = ?, kind = ?, status = ?, data = ? WHERE id = ?",
		string(task.ProjectID), task.Repo, string(task.Kind), string(task.Status), data, string(task.ID))
	if err != nil {
		return fmt.Errorf("update task: %w", err)
	}
	return nil
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
	return tx.Commit()
}

func (r *taskRepo) Delete(ctx context.Context, id core.TaskID) error {
	result, err := r.db.ExecContext(ctx, "DELETE FROM tasks WHERE id = ?", string(id))
	if err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: task %s", core.ErrNotFound, id)
	}
	return nil
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
	exists, err := rowExists(ctx, r.db, "SELECT 1 FROM artifacts WHERE id = ?", string(artifact.ID))
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%w: artifact %s", core.ErrAlreadyExists, artifact.ID)
	}
	data, err := encode(artifact)
	if err != nil {
		return err
	}
	var taskID any
	if artifact.TaskID != nil {
		taskID = string(*artifact.TaskID)
	}
	if _, err := r.db.ExecContext(ctx,
		"INSERT INTO artifacts (id, project_id, task_id, kind, data) VALUES (?, ?, ?, ?, ?)",
		string(artifact.ID), string(artifact.ProjectID), taskID, string(artifact.Kind), data); err != nil {
		return fmt.Errorf("insert artifact: %w", err)
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

func (r *artifactRepo) Update(ctx context.Context, artifact *core.Artifact) error {
	exists, err := rowExists(ctx, r.db, "SELECT 1 FROM artifacts WHERE id = ?", string(artifact.ID))
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: artifact %s", core.ErrNotFound, artifact.ID)
	}
	data, err := encode(artifact)
	if err != nil {
		return err
	}
	var taskID any
	if artifact.TaskID != nil {
		taskID = string(*artifact.TaskID)
	}
	if _, err := r.db.ExecContext(ctx,
		"UPDATE artifacts SET project_id = ?, task_id = ?, kind = ?, data = ? WHERE id = ?",
		string(artifact.ProjectID), taskID, string(artifact.Kind), data, string(artifact.ID)); err != nil {
		return fmt.Errorf("update artifact: %w", err)
	}
	return nil
}

func (r *artifactRepo) Delete(ctx context.Context, id core.ArtifactID) error {
	result, err := r.db.ExecContext(ctx, "DELETE FROM artifacts WHERE id = ?", string(id))
	if err != nil {
		return fmt.Errorf("delete artifact: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: artifact %s", core.ErrNotFound, id)
	}
	return nil
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
