//go:build unix

package cli

import (
	"context"
	"errors"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
)

// blockingBackend stands in for a harness that is still running: Exec signals
// that the run reached its harness, then waits for the run's context to end,
// exactly as a real backend's command does once an interrupt kills it.
type blockingBackend struct {
	*isofake.Backend
	started chan struct{}
	once    sync.Once
}

func newBlockingBackend() *blockingBackend {
	return &blockingBackend{Backend: isofake.New("sandbox"), started: make(chan struct{})}
}

func (b *blockingBackend) Exec(ctx context.Context, _ isolation.Handle, _ isolation.Command) (isolation.Execution, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestRunCommandInterruptRetiresItsSession pins that INT and TERM during
// `ft run` are handled by the run rather than ending the process on the spot:
// the run unwinds through its normal path, so the session it minted - for one
// task or for a whole --goal loop - is retired exactly once instead of staying
// alive forever, while a session the caller inherited is never retired by the
// run it was lent to.
func TestRunCommandInterruptRetiresItsSession(t *testing.T) {
	const inherited = "sess-chief"
	tests := []struct {
		name      string
		signal    os.Signal
		inherited string
		goal      bool
	}{
		{name: "SIGINT retires the minted session", signal: os.Interrupt},
		{name: "SIGTERM retires the minted session", signal: syscall.SIGTERM},
		{name: "an interrupt leaves the inherited session alone", signal: os.Interrupt, inherited: inherited},
		{name: "an interrupted goal loop retires its minted session", signal: os.Interrupt, goal: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRunner(t)
			if tt.inherited != "" {
				r.getenv = func(key string) string {
					if key == "FACTOTUM_SESSION" {
						return tt.inherited
					}
					return ""
				}
			}
			args := []string{"run"}
			if tt.goal {
				_, _, goal := loopContext(t, r)
				args = append(args, "--goal", goal)
			} else {
				_, taskID := runContext(t, r)
				args = append(args, taskID)
			}
			args = append(args, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())

			backend := newBlockingBackend()
			r.runBackend = backend
			r.runHarness = harnessfake.New("opencode")

			errs := make(chan error, 1)
			go func() {
				_, _, err := r.runSplitErr(args...)
				errs <- err
			}()
			select {
			case <-backend.started:
			case <-time.After(30 * time.Second):
				t.Fatal("the run never reached its harness")
			}

			if err := syscall.Kill(syscall.Getpid(), tt.signal.(syscall.Signal)); err != nil {
				t.Fatalf("send %s: %v", tt.signal, err)
			}
			select {
			case err := <-errs:
				if !errors.Is(err, errRunInterrupted) {
					t.Fatalf("interrupted run error = %v, want the interruption reported", err)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("the interrupted run did not return")
			}

			sessions := map[string]struct{ started, retired int }{}
			for _, event := range listEvents(t, r) {
				if event.Session == "" {
					continue
				}
				state := sessions[event.Session]
				switch event.Kind {
				case "session.started":
					state.started++
				case "session.retired":
					state.retired++
				}
				sessions[event.Session] = state
			}
			if len(sessions) != 1 {
				t.Fatalf("recorded sessions = %v, want exactly the one the run used", sessions)
			}
			if tt.inherited != "" {
				if state := sessions[tt.inherited]; state.started != 0 || state.retired != 0 {
					t.Fatalf("inherited session %q started %d retired %d, want it untouched", tt.inherited, state.started, state.retired)
				}
				return
			}
			for sid, state := range sessions {
				if state.started != 1 || state.retired != 1 {
					t.Fatalf("minted session %q started %d retired %d, want exactly one of each", sid, state.started, state.retired)
				}
			}
		})
	}
}

// sandboxBackend stands in for an isolating backend whose environment is real
// state on disk: Prepare creates the scratch directory a container's writable
// layer amounts to here, and Delete is the only thing that removes it. An
// interrupted run that skipped its teardown would leave that directory behind,
// where the test can see it.
type sandboxBackend struct {
	*isofake.Backend
	root    string
	started chan struct{}
	once    sync.Once

	mu        sync.Mutex
	dirs      []string
	dirByID   map[string]string
	deletes   int
	cancelled int
}

func newSandboxBackend(root string) *sandboxBackend {
	return &sandboxBackend{
		Backend: isofake.New("sandbox"),
		root:    root,
		started: make(chan struct{}),
		dirByID: map[string]string{},
	}
}

func (b *sandboxBackend) Prepare(ctx context.Context, spec isolation.Spec) (isolation.Handle, error) {
	h, err := b.Backend.Prepare(ctx, spec)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(b.root, "sandbox-")
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.dirs = append(b.dirs, dir)
	b.dirByID[h.ID()] = dir
	b.mu.Unlock()
	return h, nil
}

// Exec blocks for the run's context the way a containerised harness does, so an
// interrupt reaches the backend while the environment is still prepared.
func (b *sandboxBackend) Exec(ctx context.Context, _ isolation.Handle, _ isolation.Command) (isolation.Execution, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

// Delete removes the environment's state. It records a Delete that arrived on an
// already-cancelled context: a backend that shells out to remove a container
// cannot do so on a dead context, so a teardown that ignored that detail would
// be unreliable even though it was called.
func (b *sandboxBackend) Delete(ctx context.Context, h isolation.Handle) error {
	b.mu.Lock()
	dir := b.dirByID[h.ID()]
	b.deletes++
	if ctx.Err() != nil {
		b.cancelled++
	}
	b.mu.Unlock()

	if dir != "" {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return b.Backend.Delete(ctx, h)
}

// sandboxes returns the environment directories Prepare created, in order.
func (b *sandboxBackend) sandboxes() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.dirs...)
}

// teardown reports how many Deletes ran and how many of those arrived on a
// cancelled context.
func (b *sandboxBackend) teardown() (deletes, cancelled int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.deletes, b.cancelled
}

// TestRunCommandInterruptRemovesItsSandbox pins that an interrupt tears down the
// isolation environment, not just the session: the run unwinds through its
// normal path, so the backend Delete that removes the container and its scratch
// state still runs - once, on a live context - even though the run's own context
// is already cancelled by then. An interrupt that skipped it would leave an
// orphan container behind on the operator's machine.
func TestRunCommandInterruptRemovesItsSandbox(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)
	workspace := t.TempDir()
	backend := newSandboxBackend(t.TempDir())
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	errs := make(chan error, 1)
	go func() {
		_, _, err := r.runSplitErr("run", taskID, "--sandbox", "fake", "--harness", "fake", "--workspace", workspace)
		errs <- err
	}()
	select {
	case <-backend.started:
	case <-time.After(30 * time.Second):
		t.Fatal("the run never reached its harness")
	}

	sandboxes := backend.sandboxes()
	if len(sandboxes) != 1 {
		t.Fatalf("prepared sandboxes = %v, want exactly the one the run is using", sandboxes)
	}
	if _, err := os.Stat(sandboxes[0]); err != nil {
		t.Fatalf("the running sandbox %s is not on disk: %v", sandboxes[0], err)
	}

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT: %v", err)
	}
	select {
	case err := <-errs:
		if !errors.Is(err, errRunInterrupted) {
			t.Fatalf("interrupted run error = %v, want the interruption reported", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the interrupted run did not return")
	}

	for _, dir := range backend.sandboxes() {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("interrupt left the sandbox %s behind (stat error %v)", dir, err)
		}
	}
	if deletes, cancelled := backend.teardown(); deletes != 1 || cancelled != 0 {
		t.Fatalf("sandbox Delete ran %d times (%d on a cancelled context), want exactly one on a live context", deletes, cancelled)
	}
}
