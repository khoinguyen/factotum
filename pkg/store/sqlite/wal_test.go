package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
)

// TestStaleWAL pins the detection rule: a -shm that is not a whole number of
// WAL-index regions, or a non-empty -wal shorter than its header, is malformed.
// Such a sidecar is what SQLite memory-maps and then faults on, so the backend
// must recognize it before it hands the file to the driver.
func TestStaleWAL(t *testing.T) {
	cases := []struct {
		name    string
		shm     int64 // -1 means absent
		wal     int64 // -1 means absent
		wantErr bool
	}{
		{"no sidecars", -1, -1, false},
		{"healthy after checkpoint", shmRegionSize, 0, false},
		{"healthy with wal frames", 2 * shmRegionSize, 45352, false},
		{"truncated shm", 0, 45352, true},
		{"partial shm", 100, 45352, true},
		{"unaligned shm", 3 * shmRegionSize / 2, 45352, true},
		{"truncated wal header", shmRegionSize, 10, true},
		{"empty wal is fine", shmRegionSize, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "factotum.db")
			if err := os.WriteFile(path, []byte("SQLite format 3\x00"), 0o600); err != nil {
				t.Fatalf("write db: %v", err)
			}
			writeSized(t, path+"-shm", tc.shm)
			writeSized(t, path+"-wal", tc.wal)

			err := staleWAL(path)
			if tc.wantErr != (err != nil) {
				t.Fatalf("staleWAL() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// TestBackendReturnsErrorOnStaleWAL is the regression: with the WAL index
// truncated under a live connection (what is left after the database file is
// truncated), the next operation must return an error. Before the guard, the
// query memory-maps the stale -shm and the whole process dies with SIGBUS.
func TestBackendReturnsErrorOnStaleWAL(t *testing.T) {
	ctx := context.Background()
	backend, path := openFileBackend(t)
	if err := backend.Actors().Create(ctx, &core.Actor{ID: "a-1", Kind: core.ActorAgent, Name: "claude"}); err != nil {
		t.Fatalf("seed actor: %v", err)
	}
	if _, err := os.Stat(path + "-shm"); err != nil {
		t.Fatalf("expected a live -shm sidecar: %v", err)
	}
	if err := os.Truncate(path+"-shm", 0); err != nil {
		t.Fatalf("truncate shm: %v", err)
	}

	if _, err := backend.Actors().List(ctx); err == nil {
		t.Fatal("List() after a truncated -shm = nil error, want a stale-WAL error")
	}
}

func writeSized(t *testing.T, file string, size int64) {
	t.Helper()
	if size < 0 {
		return
	}
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	if err := os.Truncate(file, size); err != nil {
		t.Fatalf("truncate %s: %v", file, err)
	}
}
