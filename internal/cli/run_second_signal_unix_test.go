//go:build unix

package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

const secondSignalChildEnv = "FT_RUN_SECOND_SIGNAL_CHILD"

// TestRunSecondSignalChild is the process the second-signal test starts: it sets
// up the run's interrupt handling, then behaves like a harness that ignores the
// cancellation a first signal asks for. It skips in an ordinary run.
func TestRunSecondSignalChild(t *testing.T) {
	if os.Getenv(secondSignalChildEnv) == "" {
		t.Skip("only runs as the child process of TestRunSecondSignalSkipsTheGracefulPath")
	}

	cmd := &cobra.Command{Use: "run"}
	cmd.SetContext(context.Background())
	err := withRunInterrupts(cmd, func() error {
		// The handler is installed before the dispatch runs, so a signal from here
		// on is handled rather than fatal.
		fmt.Fprintln(os.Stderr, "ready")
		// A stuck harness: cancellation arrives, and the run refuses to let it
		// end the graceful path.
		<-cmd.Context().Done()
		fmt.Fprintln(os.Stderr, "cancelled")
		for {
			time.Sleep(time.Second)
		}
	})
	fmt.Fprintf(os.Stderr, "returned: %v\n", err)
	os.Exit(0)
}

// TestRunSecondSignalSkipsTheGracefulPath pins the escape hatch: one INT or TERM
// cancels the run gracefully, but a second signal is left to its default
// disposition, so a harness that ignores cancellation cannot make ft
// uninterruptible. A second signal is deliberately not graceful - the process
// ends on the spot, so the run's deferred teardown does not run: the backend
// Delete that removes its container and scratch state is skipped and the
// session retirement is skipped, leaving both behind for the operator to clean
// up. It runs in a child process because the behaviour under test is the
// termination of that process.
func TestRunSecondSignalSkipsTheGracefulPath(t *testing.T) {
	tests := []struct {
		name   string
		signal syscall.Signal
	}{
		{name: "a second SIGINT ends ft", signal: syscall.SIGINT},
		{name: "a second SIGTERM ends ft", signal: syscall.SIGTERM},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			child := startSecondSignalChild(t)
			if err := child.Process.Signal(tt.signal); err != nil {
				t.Fatalf("send the first signal: %v", err)
			}
			expectChildLine(t, child.lines, "cancelled")
			// The graceful path is stuck on purpose; only a second signal can end it.
			if err := child.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatalf("the child ended on the first signal, want the stuck graceful path still running: %v", err)
			}
			if err := child.Process.Signal(tt.signal); err != nil {
				t.Fatalf("send the second signal: %v", err)
			}

			waited := make(chan error, 1)
			go func() { waited <- child.Wait() }()
			select {
			case waitErr := <-waited:
				child.running = false
				state := child.ProcessState
				status, ok := state.Sys().(syscall.WaitStatus)
				if !ok || !status.Signaled() || status.Signal() != tt.signal {
					t.Fatalf("the second signal did not end ft by its default disposition: state=%v err=%v", state, waitErr)
				}
			case <-time.After(30 * time.Second):
				child.running = false
				_ = child.Process.Kill()
				<-waited
				t.Fatalf("the second %s did not end ft; a harness that ignores cancellation would be uninterruptible", tt.signal)
			}
		})
	}
}

// secondSignalChild is a started child process whose stderr is being watched:
// the second-signal test signals it and reads the state it reports.
type secondSignalChild struct {
	*exec.Cmd
	lines <-chan string
	// running says the child is still unreaped, so a failed test kills it
	// instead of leaving an orphan process behind.
	running bool
}

// startSecondSignalChild re-executes this test binary as the same stuck run the
// interrupt test uses, and waits until its handler is installed.
func startSecondSignalChild(t *testing.T) *secondSignalChild {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunSecondSignalChild$", "-test.timeout=60s")
	cmd.Env = append(os.Environ(), secondSignalChildEnv+"=1")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("pipe the child's stderr: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the child: %v", err)
	}
	child := &secondSignalChild{Cmd: cmd, lines: childLines(stderr), running: true}
	t.Cleanup(func() {
		if child.running {
			_ = child.Process.Kill()
		}
	})
	// The handler is installed before the dispatch runs, so the child is ready
	// to be signalled once it says so.
	expectChildLine(t, child.lines, "ready")
	return child
}

// childLines streams the child's stderr lines, so the test can send each signal
// only once the child has reached the state that makes it meaningful.
func childLines(r io.Reader) <-chan string {
	lines := make(chan string, 8)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	return lines
}

func expectChildLine(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	timeout := time.After(30 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("the child exited before printing %q", want)
			}
			if line == want {
				return
			}
		case <-timeout:
			t.Fatalf("the child never printed %q", want)
		}
	}
}
