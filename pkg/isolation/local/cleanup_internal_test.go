package local

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/khoinguyen/factotum/pkg/isolation"
)

// TestDeleteRemovesOwnedWorkspace is white-box: the concrete handle carries the
// backend-created workspace root, which the port deliberately does not expose.
// Delete must clean up a root the backend created (empty Spec.Workdir).
func TestDeleteRemovesOwnedWorkspace(t *testing.T) {
	b := New(Options{AllowHost: true, Warn: io.Discard})
	h, err := b.Prepare(context.Background(), isolation.Spec{})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	env, err := b.lookup(h)
	if err != nil {
		t.Fatalf("lookup() error = %v", err)
	}
	if !env.owned {
		t.Fatal("environments created from an empty workdir must be owned")
	}
	if _, err := os.Stat(env.root); err != nil {
		t.Fatalf("workspace %s not created: %v", env.root, err)
	}
	if err := b.Delete(context.Background(), h); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := os.Stat(env.root); !os.IsNotExist(err) {
		t.Fatalf("owned workspace %s survived Delete (stat err = %v)", env.root, err)
	}
}

// TestDeletedTombstonesAreBounded pins that a backend which prepares and deletes
// many environments does not retain a tombstone per handle forever. Idempotent
// Delete only needs to remember recently deleted handles, so the set is capped.
func TestDeletedTombstonesAreBounded(t *testing.T) {
	b := New(Options{AllowHost: true, Warn: io.Discard})
	ctx := context.Background()
	for i := 0; i < deletedMax*3; i++ {
		h, err := b.Prepare(ctx, isolation.Spec{})
		if err != nil {
			t.Fatalf("Prepare() error = %v", err)
		}
		if err := b.Delete(ctx, h); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
	}

	b.mu.Lock()
	got := len(b.deleted)
	b.mu.Unlock()
	if got > deletedMax {
		t.Fatalf("deleted tombstones = %d, want <= %d", got, deletedMax)
	}
}

// TestDeleteIdempotentForRecentHandle pins that capping the tombstones keeps the
// common case — deleting the same handle twice — idempotent.
func TestDeleteIdempotentForRecentHandle(t *testing.T) {
	b := New(Options{AllowHost: true, Warn: io.Discard})
	ctx := context.Background()
	h, err := b.Prepare(ctx, isolation.Spec{})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if err := b.Delete(ctx, h); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if err := b.Delete(ctx, h); err != nil {
		t.Fatalf("second Delete() error = %v, want idempotent nil", err)
	}
}
