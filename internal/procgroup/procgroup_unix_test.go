//go:build unix

package procgroup_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/internal/procgroup"
)

// TestKillGroupKillsDescendants pins the guarantee an isolated command buys: the
// group signal reaches a worker the command forked, not just the command.
func TestKillGroupKillsDescendants(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("sh", "-c", "sleep 30 & echo $! > child.pid; wait")
	cmd.Dir = dir
	procgroup.Isolate(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	pid := waitForPID(t, filepath.Join(dir, "child.pid"))
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	if err := procgroup.KillGroup(cmd); err != nil {
		t.Fatalf("KillGroup() error = %v", err)
	}
	if !waitGone(pid) {
		t.Fatalf("descendant pid %d survived the group kill", pid)
	}
}

// TestKillGroupIgnoresGoneGroup pins that killing a group that no longer exists
// is a no-op, not an error: cancellation races a command that exited on its own,
// and the intended state (nothing left running) already holds.
func TestKillGroupIgnoresGoneGroup(t *testing.T) {
	cmd := &exec.Cmd{Process: &os.Process{Pid: 1 << 30}}
	if err := procgroup.KillGroup(cmd); err != nil {
		t.Fatalf("KillGroup(gone group) = %v, want nil", err)
	}
}

// TestKillTreeKillsDescendants pins the interactive path's teardown: a command
// that stays in the caller's process group cannot be signalled by group (that
// would signal the caller), so its tree is walked instead.
func TestKillTreeKillsDescendants(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("sh", "-c", "sleep 30 & echo $! > child.pid; wait")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	childPid := cmd.Process.Pid
	pid := waitForPID(t, filepath.Join(dir, "child.pid"))
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	t.Cleanup(func() { _ = cmd.Wait() })

	if err := procgroup.KillTree(childPid); err != nil {
		t.Fatalf("KillTree() error = %v", err)
	}
	if !waitGone(pid) {
		t.Fatalf("descendant pid %d survived the tree kill", pid)
	}
	// The direct child is a zombie until reaped, so reap before asking whether
	// its pid still exists.
	_ = cmd.Wait()
	if !waitGone(childPid) {
		t.Fatalf("pid %d survived the tree kill", childPid)
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
	t.Fatalf("pid file %s never appeared", path)
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
