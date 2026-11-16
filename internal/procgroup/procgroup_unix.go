//go:build unix

package procgroup

import (
	"errors"
	"os/exec"
	"syscall"
)

// Isolate places cmd in its own process group, so KillGroup reaches everything
// it forks instead of the direct child alone.
func Isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// KillGroup SIGKILLs cmd's process group. A group that is already gone is not an
// error: cancellation races a command that exited on its own, and the intended
// state - nothing left running - already holds. That makes it the teardown a
// command's own exit also needs: its pid remains the group id while a worker it
// forked is still in the group, so the same call reaches that worker after Wait
// has reaped the direct child.
func KillGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
