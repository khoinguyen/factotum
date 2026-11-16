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
