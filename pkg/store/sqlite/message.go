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

// migrateV6 adds the messages and runs tables. Messages extract the columns the
// delivery protocol queries (project, address, state, task, run, lease) while
// the full entity lives in data; runs keep their session key as columns so
// register is an indexed upsert.
func migrateV6(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS messages (
  id           TEXT PRIMARY KEY,
  project_id   TEXT NOT NULL,
  to_addr      TEXT NOT NULL,
  state        TEXT NOT NULL,
  task_id      TEXT,
  run_id       TEXT,
  created_at   TEXT NOT NULL,
  lease_until  TEXT,
  updated_at   TEXT NOT NULL,
  data         TEXT NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_project ON messages(project_id)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_delivery ON messages(state, created_at, id)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_task ON messages(task_id)`,
		`CREATE TABLE IF NOT EXISTS runs (
  id         TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  actor_id   TEXT NOT NULL,
  host       TEXT NOT NULL,
  pid        INTEGER NOT NULL,
  data       TEXT NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS idx_runs_project ON runs(project_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_runs_session ON runs(project_id, actor_id, host, pid)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("messages schema: %w", err)
		}
	}
	return nil
}

type messageRepo struct{ db *sql.DB }

func (r *messageRepo) Create(ctx context.Context, message *core.Message) error {
	if err := message.Validate(0); err != nil {
		return err
	}
	exists, err := rowExists(ctx, r.db, "SELECT 1 FROM messages WHERE id = ?", string(message.ID))
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%w: message %s", core.ErrAlreadyExists, message.ID)
	}
	data, err := encode(message)
	if err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx,
		"INSERT INTO messages (id, project_id, to_addr, state, task_id, run_id, created_at, lease_until, updated_at, data) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		string(message.ID), string(message.ProjectID), string(message.To), string(message.State), nullString(taskIDValue(message.TaskID)), nullString(runIDValue(message.RunID)), formatTimeKey(message.CreatedAt), nullTime(message.LeaseUntil), formatTimeKey(message.UpdatedAt), data); err != nil {
		return fmt.Errorf("insert message: %w", err)
	}
	return nil
}

func (r *messageRepo) Get(ctx context.Context, id core.MessageID) (*core.Message, error) {
	var data string
	err := r.db.QueryRowContext(ctx, "SELECT data FROM messages WHERE id = ?", string(id)).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: message %s", core.ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("get message: %w", err)
	}
	return decodeMessage(data)
}

func (r *messageRepo) List(ctx context.Context, filter store.MessageFilter) ([]*core.Message, error) {
	query := "SELECT data FROM messages"
	conditions, args := messageFilterConditions(filter)
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
		return nil, fmt.Errorf("list messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]*core.Message, 0)
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		message, err := decodeMessage(data)
		if err != nil {
			return nil, err
		}
		out = append(out, message)
	}
	return out, rows.Err()
}

func messageFilterConditions(filter store.MessageFilter) ([]string, []any) {
	var conditions []string
	var args []any
	if filter.ProjectID != "" {
		conditions = append(conditions, "project_id = ?")
		args = append(args, string(filter.ProjectID))
	}
	if filter.To != nil {
		conditions = append(conditions, "to_addr = ?")
		args = append(args, string(*filter.To))
	}
	if filter.Actor != nil {
		conditions = append(conditions, "to_addr = ?")
		args = append(args, string(core.ActorAddress(*filter.Actor)))
	}
	if filter.Run != nil {
		conditions = append(conditions, "to_addr = ?")
		args = append(args, string(core.RunAddress(*filter.Run)))
	}
	if filter.Task != nil {
		conditions = append(conditions, "task_id = ?")
		args = append(args, string(*filter.Task))
	}
	if len(filter.States) > 0 {
		conditions = append(conditions, "state IN ("+placeholders(len(filter.States))+")")
		for _, state := range filter.States {
			args = append(args, string(state))
		}
	}
	if filter.Since != nil {
		conditions = append(conditions, "created_at >= ?")
		args = append(args, formatTimeKey(*filter.Since))
	}
	return conditions, args
}

func (r *messageRepo) Update(ctx context.Context, message *core.Message) error {
	if err := message.Validate(0); err != nil {
		return err
	}
	exists, err := rowExists(ctx, r.db, "SELECT 1 FROM messages WHERE id = ?", string(message.ID))
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: message %s", core.ErrNotFound, message.ID)
	}
	data, err := encode(message)
	if err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx,
		"UPDATE messages SET project_id = ?, to_addr = ?, state = ?, task_id = ?, run_id = ?, created_at = ?, lease_until = ?, updated_at = ?, data = ? WHERE id = ?",
		string(message.ProjectID), string(message.To), string(message.State), nullString(taskIDValue(message.TaskID)), nullString(runIDValue(message.RunID)), formatTimeKey(message.CreatedAt), nullTime(message.LeaseUntil), formatTimeKey(message.UpdatedAt), data, string(message.ID)); err != nil {
		return fmt.Errorf("update message: %w", err)
	}
	return nil
}

func (r *messageRepo) Claim(ctx context.Context, req store.ClaimRequest) (*core.Message, error) {
	return store.AwaitClaim(ctx, req.Wait, func() (*core.Message, error) { return r.claimOnce(ctx, req) })
}

func (r *messageRepo) claimOnce(ctx context.Context, req store.ClaimRequest) (*core.Message, error) {
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
	addresses := []string{"to_addr = ?"}
	args = append(args, string(core.RunAddress(req.RunID)))
	if req.Recipient.ActorID != nil {
		addresses = append(addresses, "to_addr = ?")
		args = append(args, string(core.ActorAddress(*req.Recipient.ActorID)))
	}
	if req.Recipient.TaskID != nil {
		addresses = append(addresses, "to_addr = ?")
		args = append(args, string(core.TaskAddress(*req.Recipient.TaskID)))
	}
	conditions = append(conditions, "("+strings.Join(addresses, " OR ")+")")

	var data string
	err = tx.QueryRowContext(ctx,
		"SELECT data FROM messages WHERE "+strings.Join(conditions, " AND ")+" ORDER BY created_at, id LIMIT 1", args...).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select claimable message: %w", err)
	}
	message, err := decodeMessage(data)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	lease := req.Lease
	if lease <= 0 {
		lease = store.DefaultMessageLease
	}
	until := now.Add(lease)
	runID := req.RunID
	message.State = core.MessageDelivered
	message.RunID = &runID
	message.LeaseUntil = &until
	message.DeliveredAt = &now
	message.Attempts++
	message.UpdatedAt = now
	encoded, err := encode(message)
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx,
		"UPDATE messages SET state = ?, run_id = ?, lease_until = ?, updated_at = ?, data = ? WHERE id = ? AND state = 'queued'",
		string(message.State), string(runID), formatTimeKey(until), formatTimeKey(now), encoded, string(message.ID))
	if err != nil {
		return nil, fmt.Errorf("claim message: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit claim: %w", err)
	}
	return message, nil
}

func (r *messageRepo) Ack(ctx context.Context, req store.AckRequest) error {
	message, err := r.Get(ctx, req.ID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	switch req.State {
	case core.MessageRead:
		if message.State == core.MessageRead {
			return nil
		}
		if err := checkAckOwner(*message, req.RunID); err != nil {
			return err
		}
		message.State = core.MessageRead
		message.ReadAt = &now
	case core.MessageFailed:
		if message.State == core.MessageFailed {
			return nil
		}
		if err := checkAckOwner(*message, req.RunID); err != nil {
			return err
		}
		message.State = core.MessageFailed
		message.Error = req.Error
	default:
		return fmt.Errorf("%w: ack state %q must be read or failed", core.ErrInvalid, req.State)
	}
	message.UpdatedAt = now
	return r.Update(ctx, message)
}

func checkAckOwner(message core.Message, runID core.RunID) error {
	if message.State != core.MessageDelivered {
		return fmt.Errorf("%w: message %s is %s, not delivered", core.ErrInvalid, message.ID, message.State)
	}
	if message.RunID == nil || *message.RunID != runID {
		return fmt.Errorf("%w: message %s is not owned by run %s", core.ErrInvalid, message.ID, runID)
	}
	return nil
}

func (r *messageRepo) Nack(ctx context.Context, id core.MessageID, runID core.RunID, reason string) error {
	message, err := r.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := checkAckOwner(*message, runID); err != nil {
		return err
	}
	message.Error = reason
	message.UpdatedAt = time.Now().UTC()
	if message.Attempts >= store.MaxMessageAttempts {
		message.State = core.MessageFailed
	} else {
		message.State = core.MessageQueued
		message.RunID = nil
		message.LeaseUntil = nil
	}
	return r.Update(ctx, message)
}

func (r *messageRepo) RequeueExpired(ctx context.Context, now time.Time, maxAttempts int) (int, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT id, data FROM messages WHERE state = ? AND lease_until IS NOT NULL", string(core.MessageDelivered))
	if err != nil {
		return 0, fmt.Errorf("list expired messages: %w", err)
	}
	type expired struct {
		data string
	}
	var found []expired
	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			_ = rows.Close()
			return 0, err
		}
		found = append(found, expired{data: data})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()

	moved := 0
	for _, item := range found {
		message, err := decodeMessage(item.data)
		if err != nil {
			return moved, err
		}
		if message.LeaseUntil == nil || message.LeaseUntil.After(now) {
			continue
		}
		if message.Attempts >= maxAttempts {
			message.State = core.MessageFailed
			message.Error = "lease expired"
		} else {
			message.State = core.MessageQueued
			message.RunID = nil
			message.LeaseUntil = nil
		}
		message.UpdatedAt = now
		if err := r.Update(ctx, message); err != nil {
			return moved, err
		}
		moved++
	}
	return moved, nil
}

func (r *messageRepo) Prune(ctx context.Context, before time.Time, states []core.MessageState) (int, error) {
	if len(states) == 0 {
		return 0, nil
	}
	placeholdersForStates := placeholders(len(states))
	args := []any{formatTimeKey(before)}
	for _, state := range states {
		args = append(args, string(state))
	}
	result, err := r.db.ExecContext(ctx, "DELETE FROM messages WHERE updated_at < ? AND state IN ("+placeholdersForStates+")", args...)
	if err != nil {
		return 0, fmt.Errorf("prune messages: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

func decodeMessage(data string) (*core.Message, error) {
	var message core.Message
	if err := json.Unmarshal([]byte(data), &message); err != nil {
		return nil, fmt.Errorf("decode message: %w", err)
	}
	return &message, nil
}

type runRepo struct{ db *sql.DB }

func (r *runRepo) Register(ctx context.Context, run *core.Run) (*core.Run, error) {
	if err := run.Validate(); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin register: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var existingData string
	err = tx.QueryRowContext(ctx,
		"SELECT data FROM runs WHERE project_id = ? AND actor_id = ? AND host = ? AND pid = ?",
		string(run.ProjectID), string(run.ActorID), run.Host, run.PID).Scan(&existingData)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		data, err := encode(run)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO runs (id, project_id, actor_id, host, pid, data) VALUES (?, ?, ?, ?, ?, ?)",
			string(run.ID), string(run.ProjectID), string(run.ActorID), run.Host, run.PID, data); err != nil {
			return nil, fmt.Errorf("insert run: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		cloned := *run
		return &cloned, nil
	case err != nil:
		return nil, fmt.Errorf("find run: %w", err)
	}

	existing, err := decodeRun(existingData)
	if err != nil {
		return nil, err
	}
	existing.TaskID = run.TaskID
	existing.Harness = run.Harness
	existing.CanInject = run.CanInject
	existing.SeenAt = run.SeenAt
	existing.LeaseUntil = run.LeaseUntil
	data, err := encode(existing)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE runs SET data = ? WHERE id = ?", data, string(existing.ID)); err != nil {
		return nil, fmt.Errorf("update run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return existing, nil
}

func (r *runRepo) Heartbeat(ctx context.Context, id core.RunID, now time.Time, lease time.Duration) error {
	run, err := r.get(ctx, id)
	if err != nil {
		return err
	}
	run.SeenAt = now
	run.LeaseUntil = now.Add(lease)
	data, err := encode(run)
	if err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, "UPDATE runs SET data = ? WHERE id = ?", data, string(id)); err != nil {
		return fmt.Errorf("heartbeat run: %w", err)
	}
	return nil
}

func (r *runRepo) get(ctx context.Context, id core.RunID) (*core.Run, error) {
	var data string
	err := r.db.QueryRowContext(ctx, "SELECT data FROM runs WHERE id = ?", string(id)).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: run %s", core.ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("get run: %w", err)
	}
	return decodeRun(data)
}

func (r *runRepo) List(ctx context.Context, filter store.RunFilter) ([]*core.Run, error) {
	query := "SELECT data FROM runs"
	conditions, args := runFilterConditions(filter)
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY id"

	now := filter.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]*core.Run, 0)
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		run, err := decodeRun(data)
		if err != nil {
			return nil, err
		}
		if !store.MatchRunFilter(run, filter, now) {
			continue
		}
		out = append(out, run)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out, rows.Err()
}

func runFilterConditions(filter store.RunFilter) ([]string, []any) {
	var conditions []string
	var args []any
	if filter.ProjectID != "" {
		conditions = append(conditions, "project_id = ?")
		args = append(args, string(filter.ProjectID))
	}
	if filter.ActorID != nil {
		conditions = append(conditions, "actor_id = ?")
		args = append(args, string(*filter.ActorID))
	}
	return conditions, args
}

func (r *runRepo) Delete(ctx context.Context, id core.RunID) error {
	result, err := r.db.ExecContext(ctx, "DELETE FROM runs WHERE id = ?", string(id))
	if err != nil {
		return fmt.Errorf("delete run: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: run %s", core.ErrNotFound, id)
	}
	return nil
}

func decodeRun(data string) (*core.Run, error) {
	var run core.Run
	if err := json.Unmarshal([]byte(data), &run); err != nil {
		return nil, fmt.Errorf("decode run: %w", err)
	}
	return &run, nil
}

func taskIDValue(id *core.TicketID) string {
	if id == nil {
		return ""
	}
	return string(*id)
}

func runIDValue(id *core.RunID) string {
	if id == nil {
		return ""
	}
	return string(*id)
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTimeKey(*value)
}

// timeKeyFormat is a fixed-width UTC timestamp so SQL text comparison and
// ORDER BY match chronological order even with sub-second precision.
const timeKeyFormat = "2006-01-02T15:04:05.000000000Z07:00"

func formatTimeKey(t time.Time) string { return t.UTC().Format(timeKeyFormat) }
