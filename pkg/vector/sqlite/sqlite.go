// Package sqlite is the SQLite-backed vector side index. Vectors live here rather
// than in a storage backend's documents: they are a side index beside the store,
// keyed by item id and embedding model. Cosine similarity is computed in Go.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/khoinguyen/factotum/pkg/vector"
)

const schema = `
CREATE TABLE IF NOT EXISTS vectors (
  id    TEXT PRIMARY KEY,
  model TEXT NOT NULL,
  dim   INTEGER NOT NULL,
  vec   BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vectors_model ON vectors(model);
`

// Index is a SQLite vector index.
type Index struct {
	db *sql.DB
}

// Open opens (creating if needed) the side index at path.
func Open(path string) (*Index, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create vector index dir: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open vector index: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create vector schema: %w", err)
	}
	return &Index{db: db}, nil
}

func (i *Index) Close() error { return i.db.Close() }

func (i *Index) Put(ctx context.Context, id, model string, vec []float32) error {
	if id == "" {
		return fmt.Errorf("vector: empty id")
	}
	if len(vec) == 0 {
		return fmt.Errorf("vector: empty vector for %s", id)
	}
	_, err := i.db.ExecContext(ctx,
		"INSERT INTO vectors (id, model, dim, vec) VALUES (?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET model = excluded.model, dim = excluded.dim, vec = excluded.vec",
		id, model, len(vec), encode(vec))
	if err != nil {
		return fmt.Errorf("put vector: %w", err)
	}
	return nil
}

func (i *Index) Delete(ctx context.Context, id string) error {
	if _, err := i.db.ExecContext(ctx, "DELETE FROM vectors WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete vector: %w", err)
	}
	return nil
}

func (i *Index) Search(ctx context.Context, model string, query []float32, limit int) ([]vector.Hit, error) {
	rows, err := i.db.QueryContext(ctx, "SELECT id, vec FROM vectors WHERE model = ?", model)
	if err != nil {
		return nil, fmt.Errorf("search vectors: %w", err)
	}
	defer func() { _ = rows.Close() }()
	hits := make([]vector.Hit, 0)
	for rows.Next() {
		var id string
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return nil, err
		}
		hits = append(hits, vector.Hit{ID: id, Score: vector.Cosine(query, decode(blob))})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return vector.Rank(hits, limit), nil
}

func (i *Index) Models(ctx context.Context) ([]string, error) {
	rows, err := i.db.QueryContext(ctx, "SELECT DISTINCT model FROM vectors ORDER BY model")
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var models []string
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			return nil, err
		}
		models = append(models, model)
	}
	return models, rows.Err()
}

func encode(vec []float32) []byte {
	out := make([]byte, len(vec)*4)
	for i, value := range vec {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(value))
	}
	return out
}

func decode(blob []byte) []float32 {
	out := make([]float32, len(blob)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return out
}
