//go:build unix

package opencode

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestProbeVariantKillsTheHungTree pins what the bound leaves behind. Ending the
// output drain is not the same as ending the processes: a launcher's grandchild
// inherits the stdout pipe and survives a kill of the direct child, so a probe
// that only bounds its drain abandons a live process on every hung `--version`.
// The probe owns no terminal, so it can put the binary in its own process group
// and signal the group; nothing it started may outlive the answer.
func TestProbeVariantKillsTheHungTree(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "bg.pid")
	bin := filepath.Join(dir, "opencode")
	script := "#!/bin/sh\nsleep 30 &\necho $! > " + pidFile + "\nwait\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// The bound is a hang bound, so it must not race the fake binary's startup:
	// the shell has to reach `echo` before the probe cuts it off, or the test
	// asserts nothing (t-izluv7llq6). Five seconds is far past the milliseconds a
	// shell needs to fork, and the point of the bound here is the teardown, not
	// its tightness.
	if got := probeVariant(bin, 5*time.Second); got != V1 {
		t.Fatalf("probeVariant(hung binary) = %v, want V1", got)
	}

	pid := waitForPID(t, pidFile)
	if !waitGone(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("grandchild pid %d survived the probe's bound", pid)
	}
}

// TestProbeVariantEndsAWorkerLeftByAnExitedBinary pins the probe's other trigger:
// the binary can answer and exit on its own while a worker it forked is still in
// the group. No bound expires then, so Cancel is never called and the group has
// to be ended anyway - otherwise every successful `--version` of a
// launcher-shaped binary abandons a live process.
func TestProbeVariantEndsAWorkerLeftByAnExitedBinary(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "bg.pid")
	bin := filepath.Join(dir, "opencode")
	// The worker's output is redirected away from the inherited pipe, so the
	// probe returns on the direct child's exit rather than on the worker.
	script := "#!/bin/sh\nsleep 30 >/dev/null 2>&1 &\necho $! > " + pidFile + "\necho 1.19.0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if got := probeVariant(bin, 5*time.Second); got != V1 {
		t.Fatalf("probeVariant(answering binary) = %v, want V1", got)
	}

	pid := waitForPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if !waitGone(pid) {
		t.Fatalf("grandchild pid %d survived the probe's own exit", pid)
	}
}

// waitForPID reads a pid the fake binary dropped in a file, waiting for it to
// appear because the probe's bound can expire while the shell is still starting.
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

// waitGone reports whether pid stopped existing within the wait window.
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
