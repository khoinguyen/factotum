package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// migrateV7 adds the pipelines table. The columns the factory queries (project,
// capture, state, gate) are extracted while the full entity lives in data.
func migrateV7(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS pipelines (
  id         TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  capture_id TEXT NOT NULL,
  state      TEXT NOT NULL,
  gate       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  data       TEXT NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS idx_pipelines_project ON pipelines(project_id)`,
		`CREATE INDEX IF NOT EXISTS idx_pipelines_claim ON pipelines(state, created_at, id)`,
		`CREATE INDEX IF NOT EXISTS idx_pipelines_capture ON pipelines(capture_id)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("pipelines schema: %w", err)
		}
	}
	return nil
}

type pipelineRepo struct{ db *sql.DB }

func (r *pipelineRepo) Create(ctx context.Context, pipeline *core.Pipeline) error {
	if err := pipeline.Validate(); err != nil {
		return err
	}
	exists, err := rowExists(ctx, r.db, "SELECT 1 FROM pipelines WHERE id = ?", string(pipeline.ID))
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%w: pipeline %s", core.ErrAlreadyExists, pipeline.ID)
	}
	data, err := encode(pipeline)
	if err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx,
		"INSERT INTO pipelines (id, project_id, capture_id, state, gate, created_at, updated_at, data) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		string(pipeline.ID), string(pipeline.ProjectID), string(pipeline.CaptureID), string(pipeline.State), string(pipeline.Gate), formatTimeKey(pipeline.CreatedAt), formatTimeKey(pipeline.UpdatedAt), data); err != nil {
		return fmt.Errorf("insert pipeline: %w", err)
	}
	return nil
}

func (r *pipelineRepo) Get(ctx context.Context, id core.PipelineID) (*core.Pipeline, error) {
	var data string
	err := r.db.QueryRowContext(ctx, "SELECT data FROM pipelines WHERE id = ?", string(id)).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: pipeline %s", core.ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("get pipeline: %w", err)
	}
	return decodePipeline(data)
}

func (r *pipelineRepo) List(ctx context.Context, filter store.PipelineFilter) ([]*core.Pipeline, error) {
	query := "SELECT data FROM pipelines"
	conditions, args := pipelineFilterConditions(filter)
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY created_at, id"
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list pipelines: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]*core.Pipeline, 0)
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		pipeline, err := decodePipeline(data)
		if err != nil {
			return nil, err
		}
		out = append(out, pipeline)
	}
	return out, rows.Err()
}

func pipelineFilterConditions(filter store.PipelineFilter) ([]string, []any) {
	var conditions []string
	var args []any
	if filter.ProjectID != "" {
		conditions = append(conditions, "project_id = ?")
		args = append(args, string(filter.ProjectID))
	}
	if filter.CaptureID != nil {
		conditions = append(conditions, "capture_id = ?")
		args = append(args, string(*filter.CaptureID))
	}
	if len(filter.States) > 0 {
		conditions = append(conditions, "state IN ("+placeholders(len(filter.States))+")")
		for _, state := range filter.States {
			args = append(args, string(state))
		}
	}
	if len(filter.Gates) > 0 {
		conditions = append(conditions, "gate IN ("+placeholders(len(filter.Gates))+")")
		for _, gate := range filter.Gates {
			args = append(args, string(gate))
		}
	}
	return conditions, args
}

func (r *pipelineRepo) Update(ctx context.Context, pipeline *core.Pipeline) error {
	if err := pipeline.Validate(); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin pipeline update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var currentState string
	err = tx.QueryRowContext(ctx, "SELECT state FROM pipelines WHERE id = ?", string(pipeline.ID)).Scan(&currentState)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: pipeline %s", core.ErrNotFound, pipeline.ID)
	}
	if err != nil {
		return fmt.Errorf("get pipeline: %w", err)
	}
	if core.PipelineState(currentState).Terminal() {
		return fmt.Errorf("%w: pipeline %s is %s and immutable", core.ErrConflict, pipeline.ID, currentState)
	}

	data, err := encode(pipeline)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE pipelines SET project_id = ?, capture_id = ?, state = ?, gate = ?, created_at = ?, updated_at = ?, data = ? WHERE id = ?",
		string(pipeline.ProjectID), string(pipeline.CaptureID), string(pipeline.State), string(pipeline.Gate), formatTimeKey(pipeline.CreatedAt), formatTimeKey(pipeline.UpdatedAt), data, string(pipeline.ID)); err != nil {
		return fmt.Errorf("update pipeline: %w", err)
	}
	return tx.Commit()
}

func (r *pipelineRepo) Claim(ctx context.Context, req store.PipelineClaimRequest) (*core.Pipeline, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	conditions := []string{"state = 'queued'"}
	args := []any{}
	if req.ProjectID != "" {
		conditions = append(conditions, "project_id = ?")
		args = append(args, string(req.ProjectID))
	}

	var data string
	err = tx.QueryRowContext(ctx,
		"SELECT data FROM pipelines WHERE "+strings.Join(conditions, " AND ")+" ORDER BY created_at, id LIMIT 1", args...).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: no queued pipeline for project %s", core.ErrNotFound, req.ProjectID)
	}
	if err != nil {
		return nil, fmt.Errorf("select claimable pipeline: %w", err)
	}
	pipeline, err := decodePipeline(data)
	if err != nil {
		return nil, err
	}
	pipeline.State = core.PipelineGrooming
	pipeline.UpdatedAt = time.Now().UTC()
	encoded, err := encode(pipeline)
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx,
		"UPDATE pipelines SET state = ?, updated_at = ?, data = ? WHERE id = ? AND state = 'queued'",
		string(pipeline.State), formatTimeKey(pipeline.UpdatedAt), encoded, string(pipeline.ID))
	if err != nil {
		return nil, fmt.Errorf("claim pipeline: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, fmt.Errorf("%w: no queued pipeline for project %s", core.ErrNotFound, req.ProjectID)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit claim: %w", err)
	}
	return pipeline, nil
}

func decodePipeline(data string) (*core.Pipeline, error) {
	var pipeline core.Pipeline
	if err := json.Unmarshal([]byte(data), &pipeline); err != nil {
		return nil, fmt.Errorf("decode pipeline: %w", err)
	}
	return &pipeline, nil
}
