//go:build unix

package local_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/isolation"
	"github.com/khoinguyen/factotum/pkg/isolation/local"
)

// TestStopKillsProcessGroup pins that Stop ends the whole process group, not
// just the direct child: a harness that forks workers must not leave orphans.
func TestStopKillsProcessGroup(t *testing.T) {
	b, _ := newBackend(t, local.Options{})
	dir := t.TempDir()
	h := prepare(t, b, isolation.Spec{Workdir: dir})
	if _, err := b.Exec(context.Background(), h, isolation.Command{
		Argv:    []string{"sh", "-c", "sleep 30 & echo $! > child.pid; wait"},
		Workdir: dir,
	}); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}

	pid := waitForPID(t, filepath.Join(dir, "child.pid"))
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	if err := b.Stop(context.Background(), h); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !waitGone(pid) {
		t.Fatalf("grandchild pid %d survived Stop", pid)
	}
}

func waitForPID(t *testing.T, path string) int {
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
	t.Fatalf("child pid file %s never appeared", path)
	return 0
}

func waitGone(pid int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
