//go:build unix

package local

import (
	"os"
	"os/exec"
	"testing"
)

// TestKillProcessGroupIgnoresGoneGroup pins that killing a process group that no
// longer exists is a no-op, not an error: Cancel can race a process that already
// exited on its own, and the desired state (nothing left running) already holds.
func TestKillProcessGroupIgnoresGoneGroup(t *testing.T) {
	c := &exec.Cmd{Process: &os.Process{Pid: 1 << 30}}
	if err := killProcessGroup(c); err != nil {
		t.Fatalf("killProcessGroup(gone group) = %v, want nil", err)
	}
}
