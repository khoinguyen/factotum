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
