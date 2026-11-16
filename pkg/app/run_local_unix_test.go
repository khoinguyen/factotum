//go:build unix

package app

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/isolation/local"
)

// TestRunTearsDownWorkerLeftByAnExitedHarness drives the leak through the real
// local backend at the run level: a harness that forks a worker and then exits on
// its own must not leave that worker running once Run returns. The command's own
// exit is the trigger - nothing calls Stop here - so the run's teardown is what
// has to signal the group the exited command owned.
func TestRunTearsDownWorkerLeftByAnExitedHarness(t *testing.T) {
	f := newRunFixture(t)
	backend := local.New(local.Options{AllowHost: true, Warn: io.Discard})
	pidFile := filepath.Join(t.TempDir(), "worker.pid")

	if _, err := f.run(t, RunInput{
		Backend: backend,
		Harness: shellHarness{script: "sleep 60 >/dev/null 2>&1 & echo $! > " + pidFile + "; printf 'done\\n'"},
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	pid := readPIDFile(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if !waitForGone(pid) {
		t.Fatalf("worker pid %d outlived the run", pid)
	}
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid file %s never appeared", path)
	return 0
}

func waitForGone(pid int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
