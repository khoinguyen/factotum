package local

import (
	"context"
	"io"
	"testing"

	"github.com/khoinguyen/factotum/pkg/isolation"
)

// TestExitedCommandStaysTracked is white-box: it pins that a command is tracked
// for the run's lifetime, not the command's. Once its direct child exits, the
// process group is still the run's, so a worker the command forked is still in
// it; Stop and Delete must keep a handle on that group rather than find nothing
// tracked, which is what made a normal exit leak.
func TestExitedCommandStaysTracked(t *testing.T) {
	b := New(Options{AllowHost: true, Warn: io.Discard})
	ctx := context.Background()
	h, err := b.Prepare(ctx, isolation.Spec{})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	env, err := b.lookup(h)
	if err != nil {
		t.Fatalf("lookup() error = %v", err)
	}
	ex, err := b.Exec(ctx, h, isolation.Command{Argv: []string{"true"}})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if _, err := ex.Wait(ctx); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}

	if got := tracked(env); got != 1 {
		t.Fatalf("tracked processes after the command exited = %d, want 1: the run keeps its group", got)
	}
}

func tracked(e *environment) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.procs)
}
