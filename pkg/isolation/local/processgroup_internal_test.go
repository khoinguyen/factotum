package local

import (
	"context"
	"io"
	"testing"

	"github.com/khoinguyen/factotum/pkg/isolation"
)

// TestExitedCommandIsUntracked is white-box: a command's own exit ends its
// process group, and the entry is dropped with it. A retained entry would buy
// nothing - the group has already been signalled - and would keep an armed
// kill(-pgid) for a group id the kernel is free to reuse, which Stop or Delete
// would then fire at a group this run does not own. Stop after the exit stays a
// safe no-op.
func TestExitedCommandIsUntracked(t *testing.T) {
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

	if got := tracked(env); got != 0 {
		t.Fatalf("tracked processes after the command exited = %d, want 0: the exit already ended its group", got)
	}
	if err := b.Stop(ctx, h); err != nil {
		t.Fatalf("Stop() after the command exited = %v, want a no-op", err)
	}
}

func tracked(e *environment) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.procs)
}
